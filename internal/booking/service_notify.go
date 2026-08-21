package booking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/notify"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/outbox"
)

// ConfirmationHandler renders and sends a booking confirmation.
//
// It lives here rather than in notify because it knows what a booking is:
// notify owns templates, transports and the exactly-once record, and would have
// to import this package to know what to put in one. The direction of that
// dependency is the whole reason a confirmation email is not a notify concern.
//
// The email carries its own freshly minted access token. The one returned to
// the browser at confirmation is never persisted in plain text and so cannot be
// put in an email later; minting a second one costs a row and keeps every
// stored token a hash.
func (s *Store) ConfirmationHandler(
	transport notify.Transport,
	guestBaseURL func(tenantSlug string) string,
) outbox.Handler {
	return func(ctx context.Context, q db.TX, m outbox.Pending) error {
		var payload ConfirmedPayload
		if err := json.Unmarshal(m.Payload, &payload); err != nil {
			return wrap("decode confirmation payload", err)
		}

		fields, to, locale, err := s.confirmationFields(
			ctx, q, payload.BookingID, guestBaseURL)
		if err != nil {
			return err
		}

		template, err := s.notify.Template(
			ctx, q, notify.KeyBookingConfirmed, locale)
		if err != nil {
			return err
		}

		message, err := notify.Render(template, fields)
		if err != nil {
			return err
		}
		message.To = to

		// The log row is written before the transport runs, so a second
		// dispatcher working the same message loses the unique index here rather
		// than sending a second confirmation. Already sent is success: the
		// effect asked for has happened, which is all at-least-once delivery can
		// promise.
		err = s.notify.LogSend(ctx, q, m.ID, payload.BookingID, to,
			notify.KeyBookingConfirmed, locale, message.Subject)
		switch {
		case err == nil:
		case isAlreadySent(err):
			return nil
		default:
			return err
		}

		return transport.Send(ctx, message)
	}
}

// confirmationFields assembles what the template may refer to.
//
// Money is formatted here rather than in the template because a template that
// can do arithmetic is a template that can get VAT wrong, and an operator
// editing wording should not be able to change a total.
func (s *Store) confirmationFields(
	ctx context.Context,
	q db.TX,
	bookingID string,
	guestBaseURL func(tenantSlug string) string,
) (fields map[string]string, to, locale string, err error) {
	var (
		reference, guestName, categoryName, siteName string
		arrival, departure, currency, slug           string
		nights                                       int
		gross                                        int64
	)

	// Given names, not the surname: the email opens with a greeting, and
	// "Hej Andersson" is how a debt collector writes to somebody.
	err = q.QueryRow(ctx, `
		select b.reference, g.given_names, g.email, b.locale,
		       cat.name, s.name, t.slug,
		       a.arrival_date::text, a.departure_date::text,
		       a.departure_date - a.arrival_date,
		       b.currency, b.total_gross_minor
		  from booking b
		  join unit_allocation a on a.booking_id = b.id
		  join unit_category cat on cat.id = b.category_id
		  join sites s on s.id = b.site_id
		  join tenants t on t.id = b.tenant_id
		  join guest_identity g on g.id = b.guest_id
		 where b.id = $1`, bookingID,
	).Scan(&reference, &guestName, &to, &locale,
		&categoryName, &siteName, &slug,
		&arrival, &departure, &nights, &currency, &gross)
	if err != nil {
		return nil, "", "", wrap("read booking for confirmation", err)
	}

	token, hash, err := newAccessToken()
	if err != nil {
		return nil, "", "", err
	}
	_, err = q.Exec(ctx, `
		insert into booking_access_token (booking_id, token_hash, expires_at)
		values ($1, $2, now() + $3::interval)`,
		bookingID, hash, AccessTokenTTL.String())
	if err != nil {
		return nil, "", "", wrap("insert email access token", err)
	}

	link := fmt.Sprintf("%s/%s/bokning/%s?token=%s",
		guestBaseURL(slug), locale, reference, token)

	return map[string]string{
		"Reference": reference,
		"GuestName": guestName,
		"SiteName":  siteName,
		"Category":  categoryName,
		"Arrival":   arrival,
		"Departure": departure,
		"Nights":    strconv.Itoa(nights),
		"Total":     formatMinor(gross, currency),
		"Link":      link,
	}, to, locale, nil
}

// formatMinor renders minor units the way a Swedish invoice does: space as the
// thousands separator, comma for the decimal, currency last.
func formatMinor(minor int64, currency string) string {
	negative := minor < 0
	if negative {
		minor = -minor
	}
	whole := strconv.FormatInt(minor/100, 10)

	var grouped []byte
	for i, digit := range []byte(whole) {
		if i > 0 && (len(whole)-i)%3 == 0 {
			grouped = append(grouped, ' ')
		}
		grouped = append(grouped, digit)
	}

	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%s,%02d %s", sign, grouped, minor%100, currency)
}

func isAlreadySent(err error) bool {
	return errors.Is(err, notify.ErrAlreadySent)
}
