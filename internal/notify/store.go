package notify

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Store reads templates and records what was sent.
type Store struct {
	db *tenant.DB
}

// NewStore constructs a Store over the tenant-pinned handle.
func NewStore(d *tenant.DB) *Store { return &Store{db: d} }

// Template loads the operator's wording for a key and locale on the caller's
// transaction. There is no fallback to another locale: writing to a German
// guest in Swedish because nobody translated the template is a decision the
// operator has to make, not one this package makes for them.
func (s *Store) Template(
	ctx context.Context,
	q db.TX,
	key, locale string,
) (Template, error) {
	t := Template{Key: key, Locale: locale}
	err := q.QueryRow(ctx,
		`select subject, body from message_template
		  where key = $1 and locale = $2 and channel = 'email'`, key, locale,
	).Scan(&t.Subject, &t.Body)
	switch {
	case err == nil:
		return t, nil
	case errors.Is(err, pgx.ErrNoRows):
		return Template{}, fmt.Errorf("%w: %s/%s", ErrNoTemplate, key, locale)
	default:
		return Template{}, fmt.Errorf("read template: %w", err)
	}
}

// LogSend claims the right to send, on the caller's transaction.
//
// It is written before the transport is called, not after. The unique index on
// outbox_message_id means a second dispatcher working the same message loses
// here and gets ErrAlreadySent, which is the whole idempotency mechanism: after
// the transport has run there is nothing left to serialise on.
func (s *Store) LogSend(
	ctx context.Context,
	q db.TX,
	outboxMessageID, bookingID, to, key, locale, subject string,
) error {
	_, err := q.Exec(ctx, `
		insert into message_log
		    (channel, to_address, template_key, locale, booking_id,
		     outbox_message_id, subject)
		values ('email', $1, $2, $3, nullif($4, '')::uuid, $5, $6)`,
		to, key, locale, bookingID, outboxMessageID, subject)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == db.UniqueViolation {
			return ErrAlreadySent
		}
		return fmt.Errorf("log send: %w", err)
	}
	return nil
}
