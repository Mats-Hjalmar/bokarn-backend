package booking

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/assignment"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/guest"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/outbox"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/pricing"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ConfirmRequest is a guest turning a quote into a booking.
//
// The stay is echoed rather than read from the quote, and that is the point.
// The server recomputes the input hash from what the caller says it is
// confirming and refuses if it differs from what was priced. Trusting the quote
// id alone would make the freeze unfalsifiable — a client could quote a cheap
// stay and confirm an expensive one.
type ConfirmRequest struct {
	QuoteID        string
	HoldToken      string
	IdempotencyKey string

	CategoryCode string
	Arrival      string
	Departure    string
	Adults       int
	Children     []string
	Pets         int
	Vehicles     int
	CampaignCode string

	ElectricityAmp int
	Accessible     bool

	Guest            GuestDetails
	Locale           string
	Channel          string
	MarketingConsent bool
	Notes            string
}

// GuestDetails is who to write to and who is arriving.
type GuestDetails struct {
	GivenNames         string
	Surname            string
	Email              string
	Phone              string
	CountryOfResidence string
}

// Confirmed is what the guest is shown, and the only time the access token
// exists in plain text.
type Confirmed struct {
	Reference string `json:"reference"`
	// AccessToken lets the confirmation page be reloaded and bookmarked. Only
	// its hash is stored, so this is the last moment it can be read; the
	// confirmation email mints its own rather than being handed this one.
	AccessToken  string `json:"access_token"`
	State        string `json:"state"`
	CategoryCode string `json:"category_code"`
	Arrival      string `json:"arrival"`
	Departure    string `json:"departure"`
	Nights       int    `json:"nights"`
	Currency     string `json:"currency"`
	TotalGross   int64  `json:"total_gross_minor"`
	TotalNet     int64  `json:"total_net_minor"`
	TotalVAT     int64  `json:"total_vat_minor"`
	GuestName    string `json:"guest_name"`
	Email        string `json:"email"`
	// Replayed is true when this request had already been processed under the
	// same idempotency key. The caller returns 200 rather than 201 for it, so a
	// guest double-tapping "Boka" sees their booking instead of a second one.
	Replayed bool `json:"-"`
}

// Confirm turns a held pitch and a frozen price into a booking.
//
// Everything happens in one transaction, because the parts are not
// independently meaningful: a guest row with no booking is personal data held
// for no purpose, a booking with no price lines is unenforceable, and an
// occupancy row with no booking is a pitch nobody can be found for.
//
// The order inside is deliberate. Occupancy is claimed last, after the guest,
// the booking and the price lines are written, because claiming a pitch is the
// step that can fail for a reason nobody did anything wrong — somebody else got
// there first — and it is the step retried against other units. Doing it last
// means the retry loop has nothing to redo.
func (s *Store) Confirm(
	ctx context.Context,
	req ConfirmRequest,
	now time.Time,
) (Confirmed, error) {
	var out Confirmed

	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		existing, found, err := s.byIdempotencyKey(ctx, tx, req.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			existing.Replayed = true
			out = existing
			return nil
		}

		quote, err := s.pricing.LoadQuoteOn(ctx, tx, req.QuoteID)
		if err != nil {
			return err
		}
		if !quote.Expires.After(now) {
			return ErrQuoteExpired
		}

		priced := pricing.Request{
			Arrival:      req.Arrival,
			Departure:    req.Departure,
			Adults:       req.Adults,
			Children:     childGuests(req.Children),
			Pets:         req.Pets,
			Vehicles:     req.Vehicles,
			CampaignCode: req.CampaignCode,
		}
		want := pricing.InputHash(req.CategoryCode, priced)
		if fmt.Sprintf("%x", want) != quote.InputHash {
			return ErrQuoteMismatch
		}

		if err := s.expireStale(ctx, tx, quote.CategoryCode); err != nil {
			return err
		}

		g, err := s.guests.Upsert(ctx, tx, guest.NewIdentity{
			GivenNames:         req.Guest.GivenNames,
			Surname:            req.Guest.Surname,
			Email:              req.Guest.Email,
			Phone:              req.Guest.Phone,
			CountryOfResidence: req.Guest.CountryOfResidence,
			PurgeAfter:         purgeAfter(quote.Departure),
			MarketingConsent:   req.MarketingConsent,
		})
		if err != nil {
			return err
		}
		// Both answers are recorded. "They declined" and "we never asked" are
		// different facts, and only the first is a defence.
		err = s.guests.RecordConsent(
			ctx, tx, g.ID, "marketing", req.MarketingConsent, "web")
		if err != nil {
			return err
		}

		terms, err := s.pricing.CancellationTerms(ctx, tx, quote.RatePlanID)
		if err != nil {
			return err
		}

		bookingID, reference, err := s.insertBooking(ctx, tx, newBooking{
			req:   req,
			quote: quote,
			guest: g,
			terms: terms,
		})
		if err != nil {
			return err
		}

		if err := s.copyPriceLines(ctx, tx, bookingID, quote); err != nil {
			return err
		}
		if err := s.insertParty(ctx, tx, bookingID, req, g, quote); err != nil {
			return err
		}
		if err := s.insertRequirements(ctx, tx, bookingID, req); err != nil {
			return err
		}

		unitCode, err := s.claimForBooking(ctx, tx, req, quote, bookingID)
		if err != nil {
			return err
		}

		token, hash, err := newAccessToken()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			insert into booking_access_token (booking_id, token_hash, expires_at)
			values ($1, $2, $3)`,
			bookingID, hash, now.Add(AccessTokenTTL))
		if err != nil {
			return wrap("insert access token", err)
		}

		detail, err := json.Marshal(map[string]string{
			"unit":    unitCode,
			"channel": req.Channel,
		})
		if err != nil {
			return wrap("encode event detail", err)
		}
		err = s.event(
			ctx, tx, bookingID, EventConfirmed, "guest", nil, detail)
		if err != nil {
			return err
		}

		// Enqueued inside the same transaction as the booking, so a confirmed
		// booking cannot exist without its confirmation email being owed, and a
		// rolled-back one cannot leave one behind.
		err = outbox.Enqueue(ctx, tx, outbox.Message{
			Kind:           MessageBookingConfirmed,
			IdempotencyKey: bookingID,
			Payload: ConfirmedPayload{
				BookingID: bookingID,
				Reference: reference,
			},
		})
		if err != nil {
			return err
		}

		out = Confirmed{
			Reference:    reference,
			AccessToken:  token,
			State:        StateConfirmed,
			CategoryCode: quote.CategoryCode,
			Arrival:      quote.Arrival,
			Departure:    quote.Departure,
			Nights:       quote.Nights,
			Currency:     quote.Totals.Currency,
			TotalGross:   quote.Totals.GrossMinor,
			TotalNet:     quote.Totals.NetMinor,
			TotalVAT:     quote.Totals.VATMinor,
			GuestName:    g.Name(),
			Email:        g.Email,
		}
		return nil
	})
	if err != nil {
		return Confirmed{}, err
	}
	return out, nil
}

// MessageBookingConfirmed is the outbox kind for the confirmation email.
const MessageBookingConfirmed = "booking.confirmed"

// ConfirmedPayload is what the confirmation email handler is given. It carries
// identifiers only: the email is rendered from the booking as it stands when
// the message is delivered, not from a copy taken when it was enqueued.
type ConfirmedPayload struct {
	BookingID string `json:"booking_id"`
	Reference string `json:"reference"`
}

type newBooking struct {
	req   ConfirmRequest
	quote pricing.StoredQuote
	guest guest.Identity
	terms []byte
}

// insertBooking writes the booking, retrying on a reference collision.
//
// A six-character reference over a thirty-character alphabet collides about
// once in a hundred million, so the retry is not a hot path — but the unique
// constraint is the only thing that makes the reference safe to read out over
// the phone, and a collision must produce a second reference rather than a
// failed booking.
func (s *Store) insertBooking(
	ctx context.Context,
	tx pgx.Tx,
	in newBooking,
) (bookingID, reference string, err error) {
	const attempts = 5
	for range attempts {
		reference, err = NewReference()
		if err != nil {
			return "", "", err
		}

		sp, err := tx.Begin(ctx)
		if err != nil {
			return "", "", wrap("open savepoint", err)
		}
		err = sp.QueryRow(ctx, `
			insert into booking
			    (reference, site_id, category_id, guest_id, quote_id,
			     engine_version, input_hash, quote_hash, idempotency_key,
			     cancellation_policy, currency, total_gross_minor,
			     total_net_minor, total_vat_minor, vehicles, locale, channel,
			     notes)
			select $1, $2, $3, $4, $5, q.engine_version, q.input_hash,
			       q.breakdown_hash, $6, $7, q.currency, q.total_gross_minor,
			       q.total_net_minor, q.total_vat_minor, $8, $9, $10,
			       nullif($11, '')
			  from quote q where q.id = $5
			returning id::text`,
			reference, in.quote.SiteID, in.quote.CategoryID, in.guest.ID,
			in.quote.ID, in.req.IdempotencyKey, in.terms, in.req.Vehicles,
			in.req.Locale, in.req.Channel, in.req.Notes,
		).Scan(&bookingID)
		if err == nil {
			if err := sp.Commit(ctx); err != nil {
				return "", "", wrap("release savepoint", err)
			}
			return bookingID, reference, nil
		}

		_ = sp.Rollback(ctx)
		if isDuplicate(err, "booking_tenant_id_reference_key") {
			continue
		}
		return "", "", wrap("insert booking", err)
	}
	return "", "", fmt.Errorf(
		"insert booking: %d reference collisions in a row", attempts)
}

// copyPriceLines copies the frozen breakdown verbatim.
//
// The lines go in as the JSON the engine emitted, expanded by
// jsonb_to_recordset, so the copy cannot drift from what was quoted: there is
// no intermediate Go struct to add a field to on one side only.
func (s *Store) copyPriceLines(
	ctx context.Context,
	tx pgx.Tx,
	bookingID string,
	quote pricing.StoredQuote,
) error {
	if len(quote.Lines) == 0 {
		return fmt.Errorf("copy price lines: quote %s has none", quote.ID)
	}

	lines, err := json.Marshal(quote.Lines)
	if err != nil {
		return wrap("encode price lines", err)
	}

	_, err = tx.Exec(ctx, `
		insert into booking_price_line
		    (booking_id, amendment_id, seq, kind, stay_date, description, qty,
		     unit_gross_minor, gross_minor, net_minor, vat_minor, vat_code,
		     vat_rate_bp, vat_treatment)
		select $1, $2, l.seq, l.kind, l.stay_date, l.description, l.qty,
		       l.unit_gross_minor, l.gross_minor, l.net_minor, l.vat_minor,
		       l.vat_code, l.vat_rate_bp, l.vat_treatment
		  from jsonb_to_recordset($3::jsonb) as l(
		       seq int, kind text, stay_date date, description text, qty int,
		       unit_gross_minor bigint, gross_minor bigint, net_minor bigint,
		       vat_minor bigint, vat_code text, vat_rate_bp int,
		       vat_treatment text)`,
		bookingID, uuid.NewString(), lines)
	if err != nil {
		return wrap("insert price lines", err)
	}
	return nil
}

// insertParty records the people whose identity is actually known.
//
// The lead guest, and every child by date of birth because the age band that
// priced them is not recoverable from a count. Accompanying adults are a number
// on the allocation until reception asks their names at check-in: a row with no
// name in it records nothing and would only make the party look captured.
func (s *Store) insertParty(
	ctx context.Context,
	tx pgx.Tx,
	bookingID string,
	req ConfirmRequest,
	g guest.Identity,
	quote pricing.StoredQuote,
) error {
	purge := purgeAfter(quote.Departure).Format(time.DateOnly)

	_, err := tx.Exec(ctx, `
		insert into booking_party
		    (booking_id, role, given_names, surname, purge_after)
		values ($1, 'lead', $2, $3, $4::date)`,
		bookingID, g.GivenNames, g.Surname, purge)
	if err != nil {
		return wrap("insert lead party member", err)
	}

	for _, dob := range req.Children {
		_, err := tx.Exec(ctx, `
			insert into booking_party
			    (booking_id, role, date_of_birth, purge_after)
			values ($1, 'child', $2::date, $3::date)`, bookingID, dob, purge)
		if err != nil {
			return wrap("insert child party member", err)
		}
	}
	return nil
}

// insertRequirements records what this stay needs from a pitch, so a later
// reassignment honours it. Reception moving a guest with an accessibility need
// onto an ordinary pitch is the failure this prevents, and reception cannot
// avoid it if the need was only ever a search filter.
func (s *Store) insertRequirements(
	ctx context.Context,
	tx pgx.Tx,
	bookingID string,
	req ConfirmRequest,
) error {
	type requirement struct {
		key   string
		op    string
		value string
	}
	var reqs []requirement

	if req.Accessible {
		reqs = append(reqs, requirement{"accessible", "=", "true"})
	}
	if req.ElectricityAmp > 0 {
		reqs = append(reqs, requirement{
			"electricity_amp", ">=", fmt.Sprint(req.ElectricityAmp)})
	}
	if req.Pets > 0 {
		reqs = append(reqs, requirement{"pets_allowed", "=", "true"})
	}

	for _, r := range reqs {
		_, err := tx.Exec(ctx, `
			insert into allocation_requirement
			    (booking_id, attr_key, op, value)
			values ($1, $2, $3, $4)`, bookingID, r.key, r.op, r.value)
		if err != nil {
			return wrap("insert allocation requirement", err)
		}
	}
	return nil
}

// claimForBooking attaches occupancy to the booking.
//
// It tries to convert the hold the guest was given, and the guard is in the
// WHERE clause rather than in a preceding read: the hold's state and deadline
// are tested by Postgres at the moment of the write, so there is no window
// between checking the hold and using it. Zero rows updated means the hold is
// gone, and admission is re-run from scratch.
//
// That re-run is the important half. A hold expiring between the guest loading
// the checkout page and submitting it is ordinary, and the pitch may have been
// resold in the meantime. Trusting a stale hold is the commonest real-world
// source of a double-booked pitch on arrival day.
func (s *Store) claimForBooking(
	ctx context.Context,
	tx pgx.Tx,
	req ConfirmRequest,
	quote pricing.StoredQuote,
	bookingID string,
) (unitCode string, err error) {
	if req.HoldToken != "" {
		err := tx.QueryRow(ctx, `
			update unit_allocation
			   set kind = 'booking', state = 'confirmed', expires_at = null,
			       booking_id = $2
			 where id = $1 and kind = 'hold' and state = 'held'
			   and expires_at > now()
			   and stay = $3::daterange
			returning (select code from unit where id = unit_id)`,
			req.HoldToken, bookingID,
			assignment.Daterange(quote.Arrival, quote.Departure),
		).Scan(&unitCode)
		switch {
		case err == nil:
			return unitCode, nil
		case isNoRows(err), db.IsBadInput(err):
			// Fall through to re-admission. The hold is gone, expired, or names
			// a different stay than the quote does.
		default:
			return "", wrap("claim hold", err)
		}
	}

	cands, siteID, categoryID, err := s.assign.CandidatesOn(
		ctx, tx, assignment.Query{
			CategoryCode:   quote.CategoryCode,
			Arrival:        quote.Arrival,
			Departure:      quote.Departure,
			Guests:         req.Adults + len(req.Children),
			Pets:           req.Pets,
			ElectricityAmp: req.ElectricityAmp,
			Accessible:     req.Accessible,
		})
	if err != nil {
		return "", err
	}
	if len(cands) == 0 {
		return "", ErrHoldExpired
	}

	_, unitCode, err = s.claim(ctx, tx, claim{
		candidates: cands,
		siteID:     siteID,
		categoryID: categoryID,
		stay:       assignment.Daterange(quote.Arrival, quote.Departure),
		kind:       "booking",
		state:      StateConfirmed,
		bookingID:  &bookingID,
		adults:     req.Adults,
		children:   len(req.Children),
		pets:       req.Pets,
	})
	if isNoUnit(err) {
		return "", ErrHoldExpired
	}
	if err != nil {
		return "", err
	}
	return unitCode, nil
}

// byIdempotencyKey returns a booking already confirmed under this key.
//
// The lookup is a read inside the same transaction as the insert, and the
// unique constraint is what actually serialises two simultaneous replays: this
// only catches the sequential case cheaply. The concurrent case collides on
// booking_tenant_id_idempotency_key_key and is surfaced as the conflict it is,
// which the HTTP layer retries.
func (s *Store) byIdempotencyKey(
	ctx context.Context,
	tx pgx.Tx,
	key string,
) (Confirmed, bool, error) {
	var c Confirmed
	var g guest.Identity
	err := tx.QueryRow(ctx, `
		select b.reference, a.state, cat.code,
		       a.arrival_date::text, a.departure_date::text,
		       a.departure_date - a.arrival_date,
		       b.currency, b.total_gross_minor, b.total_net_minor,
		       b.total_vat_minor, g.given_names, g.surname, g.email
		  from booking b
		  join unit_allocation a on a.booking_id = b.id
		  join unit_category cat on cat.id = b.category_id
		  join guest_identity g on g.id = b.guest_id
		 where b.idempotency_key = $1`, key,
	).Scan(&c.Reference, &c.State, &c.CategoryCode, &c.Arrival, &c.Departure,
		&c.Nights, &c.Currency, &c.TotalGross, &c.TotalNet, &c.TotalVAT,
		&g.GivenNames, &g.Surname, &c.Email)
	switch {
	case err == nil:
		c.GuestName = g.Name()
		return c, true, nil
	case isNoRows(err):
		return Confirmed{}, false, nil
	default:
		return Confirmed{}, false, wrap("read booking by key", err)
	}
}

// newAccessToken mints a link credential and its stored hash.
//
// Only the hash is written. A dump of booking_access_token is therefore useless
// on its own, which matters because the token in an emailed link is the whole
// authorisation for reading a stranger's name, dates and total.
func newAccessToken() (token string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, wrap("read random", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

// HashAccessToken is how a presented token is looked up.
func HashAccessToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func childGuests(dobs []string) []pricing.Guest {
	out := make([]pricing.Guest, 0, len(dobs))
	for _, dob := range dobs {
		out = append(out, pricing.Guest{DateOfBirth: dob})
	}
	return out
}

// purgeAfter is when a guest's data must be gone, counted from the departure
// they booked rather than from today, so a booking made a year ahead does not
// shorten its own retention.
func purgeAfter(departure string) time.Time {
	d, err := time.Parse(time.DateOnly, departure)
	if err != nil {
		// The date came out of a date column, so this cannot happen without the
		// database having handed back something that is not a date.
		panic(fmt.Sprintf("booking: departure %q is not a date", departure))
	}
	return d.Add(guest.RetentionAfterDeparture)
}
