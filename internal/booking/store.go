package booking

import (
	"context"
	"errors"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/assignment"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/guest"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/notify"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/pricing"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Store is the booking domain's one entry point to the database.
//
// It holds the other stores it needs rather than reaching for them, because a
// confirmation is a single transaction spanning guest, pricing and occupancy,
// and a domain that cannot pass a transaction to its collaborators would have
// to commit each part separately.
type Store struct {
	db      *tenant.DB
	assign  *assignment.Store
	guests  *guest.Store
	pricing *pricing.Store
	notify  *notify.Store
}

// NewStore constructs a Store.
func NewStore(
	d *tenant.DB,
	assign *assignment.Store,
	guests *guest.Store,
	prices *pricing.Store,
	notifications *notify.Store,
) *Store {
	return &Store{
		db:      d,
		assign:  assign,
		guests:  guests,
		pricing: prices,
		notify:  notifications,
	}
}

// isOverlap reports whether an insert lost the race for a unit.
//
// The constraint is named so this can be specific: any exclusion violation on
// unit_allocation means an overlap, but keying on the name documents which
// constraint the retry loop is written against, and would fail loudly rather
// than silently swallow a different one added later.
func isOverlap(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == db.ExclusionViolation &&
		pgErr.ConstraintName == "unit_allocation_no_overlap"
}

// isNoRows separates "there is no such row" from a failure. Every caller of it
// has a specific answer for the empty case, which is why none of them treats
// pgx.ErrNoRows as an error to wrap.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func isNoUnit(err error) bool { return errors.Is(err, ErrNoUnitAvailable) }

func isDuplicate(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == db.UniqueViolation &&
		pgErr.ConstraintName == constraint
}

// expireStale marks holds whose deadline has passed, on the caller's
// transaction.
//
// This is the opportunistic half of hold expiry. It runs at the top of every
// path that needs to know what is free, because the index predicate on
// unit_allocation must be IMMUTABLE and therefore cannot mention now(): an
// expired hold still occupies its unit until something writes the new state.
// Scoped to one category so a busy site does not rewrite its whole occupancy
// table on every search.
func (s *Store) expireStale(
	ctx context.Context,
	q db.TX,
	categoryCode string,
) error {
	_, err := q.Exec(ctx, `
		update unit_allocation set state = 'expired'
		 where kind = 'hold' and state = 'held' and expires_at <= now()
		   and category_id = (select id from unit_category where code = $1)`,
		categoryCode)
	if err != nil {
		return wrap("expire stale holds", err)
	}
	return nil
}

// event appends to reservation_event on the caller's transaction.
func (s *Store) event(
	ctx context.Context,
	q db.TX,
	bookingID, kind, actor string,
	actorUserID *string,
	detail []byte,
) error {
	_, err := q.Exec(ctx, `
		insert into reservation_event
		    (booking_id, kind, actor, actor_user_id, detail)
		values ($1, $2, $3, $4, $5)`,
		bookingID, kind, actor, actorUserID, detail)
	if err != nil {
		return wrap("insert reservation event", err)
	}
	return nil
}
