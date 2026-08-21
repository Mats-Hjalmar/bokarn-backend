package guest

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Store reads and writes guest identities.
type Store struct {
	db *tenant.DB
}

// NewStore constructs a Store over the tenant-pinned handle.
func NewStore(d *tenant.DB) *Store { return &Store{db: d} }

// Upsert returns the operator's record for this email, creating it if this is
// the first time they have booked here and refreshing the details if not.
//
// It joins the caller's transaction because a guest row only ever comes into
// existence as part of confirming a booking; created on its own and then
// orphaned by a failed confirm, it would be personal data held for no purpose.
//
// The update deliberately keeps the later of the two purge dates. A guest who
// books a second stay has extended the purpose their data is held for, and
// taking the newer date unconditionally would shorten retention for a guest who
// booked a distant stay first.
func (s *Store) Upsert(
	ctx context.Context,
	q db.TX,
	in NewIdentity,
) (Identity, error) {
	var g Identity
	err := q.QueryRow(ctx, `
		insert into guest_identity
		    (given_names, surname, email, phone, country_of_residence,
		     purge_after, marketing_consent_at)
		values ($1, $2, $3, $4, $5, $6::date,
		        case when $7 then now() else null end)
		on conflict (tenant_id, lower(email)) do update
		   set given_names          = excluded.given_names,
		       surname              = excluded.surname,
		       phone                = excluded.phone,
		       country_of_residence = excluded.country_of_residence,
		       purge_after          = greatest(guest_identity.purge_after,
		                                       excluded.purge_after),
		       marketing_consent_at = coalesce(excluded.marketing_consent_at,
		                                       guest_identity.marketing_consent_at)
		returning id::text, given_names, surname, email, phone,
		          country_of_residence, coalesce(citizenship, '')`,
		in.GivenNames, in.Surname, in.Email, in.Phone, in.CountryOfResidence,
		in.PurgeAfter.Format("2006-01-02"), in.MarketingConsent,
	).Scan(&g.ID, &g.GivenNames, &g.Surname, &g.Email, &g.Phone,
		&g.CountryOfResidence, &g.Citizenship)
	if err != nil {
		return Identity{}, fmt.Errorf("upsert guest: %w", err)
	}
	return g, nil
}

// RecordConsent appends a consent decision. Both answers are recorded, because
// "they never agreed" and "we never asked" are different facts and only the
// first is a defence.
func (s *Store) RecordConsent(
	ctx context.Context,
	q db.TX,
	guestID, kind string,
	granted bool,
	source string,
) error {
	_, err := q.Exec(ctx,
		`insert into consent (guest_id, kind, granted, source)
		 values ($1, $2, $3, $4)`, guestID, kind, granted, source)
	if err != nil {
		return fmt.Errorf("insert consent: %w", err)
	}
	return nil
}

// ByEmail finds the operator's record for an email address.
func (s *Store) ByEmail(ctx context.Context, email string) (Identity, error) {
	var g Identity
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			select id::text, given_names, surname, email, phone,
			       country_of_residence, coalesce(citizenship, '')
			  from guest_identity where lower(email) = lower($1)`, email,
		).Scan(&g.ID, &g.GivenNames, &g.Surname, &g.Email, &g.Phone,
			&g.CountryOfResidence, &g.Citizenship)
	})
	switch {
	case err == nil:
		return g, nil
	case errors.Is(err, pgx.ErrNoRows):
		return Identity{}, ErrNotFound
	default:
		return Identity{}, fmt.Errorf("read guest: %w", err)
	}
}
