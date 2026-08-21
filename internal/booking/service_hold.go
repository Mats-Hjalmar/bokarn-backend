package booking

import (
	"context"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/assignment"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
)

// HoldRequest is a stay somebody wants reserved while they finish checking out.
type HoldRequest struct {
	CategoryCode   string
	Arrival        string
	Departure      string
	Adults         int
	Children       int
	Pets           int
	ElectricityAmp int
	Accessible     bool
}

// Guests is the party size the pitch has to seat.
func (r HoldRequest) Guests() int { return r.Adults + r.Children }

// Hold is a reserved pitch with a deadline.
type Hold struct {
	// Token is the allocation's own id. It is deliberately not a credential:
	// presenting somebody else's hold lets you take the pitch they were about
	// to take, which is the same thing you could do by booking first, and
	// exposes nothing about them. A separate secret would be ceremony that
	// protects nothing.
	Token        string `json:"hold_token"`
	CategoryCode string `json:"category_code"`
	UnitCode     string `json:"unit_code"`
	Arrival      string `json:"arrival"`
	Departure    string `json:"departure"`
	ExpiresAt    string `json:"expires_at"`
}

// Hold reserves a physical unit for the stay.
//
// The guest bought a category and will never be told which pitch this is until
// they arrive, but a unit is assigned now regardless. That is the v1 bet: with
// a unit always assigned, the exclusion constraint is the entire concurrency
// story — no inventory counter to keep in step, no lock ordering, no rows in a
// half-sold state to reconcile. Two guests racing for the last cabin produce
// exactly one hold and one refusal, decided by Postgres.
func (s *Store) Hold(
	ctx context.Context,
	req HoldRequest,
	now time.Time,
) (Hold, error) {
	query := assignment.Query{
		CategoryCode:   req.CategoryCode,
		Arrival:        req.Arrival,
		Departure:      req.Departure,
		Guests:         req.Guests(),
		Pets:           req.Pets,
		ElectricityAmp: req.ElectricityAmp,
		Accessible:     req.Accessible,
	}

	var out Hold
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		// Expiry first, and it has to be first: an expired hold still occupies
		// its unit until something writes the new state, so a candidate list
		// read before this would hide pitches that are in fact free.
		if err := s.expireStale(ctx, tx, req.CategoryCode); err != nil {
			return err
		}

		cands, siteID, categoryID, err := s.assign.CandidatesOn(ctx, tx, query)
		if err != nil {
			return err
		}
		if len(cands) == 0 {
			return ErrNoUnitAvailable
		}

		expires := now.Add(HoldTTL)
		id, unitCode, err := s.claim(ctx, tx, claim{
			candidates: cands,
			siteID:     siteID,
			categoryID: categoryID,
			stay:       assignment.Daterange(req.Arrival, req.Departure),
			kind:       "hold",
			state:      StateHeld,
			expiresAt:  &expires,
			adults:     req.Adults,
			children:   req.Children,
			pets:       req.Pets,
		})
		if err != nil {
			return err
		}

		out = Hold{
			Token:        id,
			CategoryCode: req.CategoryCode,
			UnitCode:     unitCode,
			Arrival:      req.Arrival,
			Departure:    req.Departure,
			ExpiresAt:    expires.Format(time.RFC3339),
		}
		return nil
	})
	if err != nil {
		return Hold{}, err
	}
	return out, nil
}

// claim is one attempt at putting an occupancy row on a unit.
type claim struct {
	candidates []assignment.Candidate
	siteID     string
	categoryID string
	stay       string
	kind       string
	state      string
	expiresAt  *time.Time
	bookingID  *string
	adults     int
	children   int
	pets       int
}

// claim walks the candidate list inserting until one insert survives.
//
// Each attempt runs inside a savepoint. That is the point of doing it this way
// rather than in separate transactions: an exclusion violation aborts the
// current subtransaction, not the caller's, so a confirmation that has already
// written a guest row and a price breakdown can lose a race for one pitch and
// take the next without starting over.
func (s *Store) claim(
	ctx context.Context,
	tx pgx.Tx,
	c claim,
) (allocationID, unitCode string, err error) {
	for _, cand := range c.candidates {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return "", "", wrap("open savepoint", err)
		}

		err = sp.QueryRow(ctx, `
			insert into unit_allocation
			    (site_id, category_id, unit_id, booking_id, kind, state, stay,
			     expires_at, adults, children, pets)
			values ($1, $2, $3, $4, $5, $6, $7::daterange, $8, $9, $10, $11)
			returning id::text,
			          (select code from unit where id = $3)`,
			c.siteID, c.categoryID, cand.UnitID, c.bookingID, c.kind, c.state,
			c.stay, c.expiresAt, c.adults, c.children, c.pets,
		).Scan(&allocationID, &unitCode)
		if err != nil {
			_ = sp.Rollback(ctx)
			if isOverlap(err) {
				continue
			}
			return "", "", wrap("insert allocation", err)
		}
		if err := sp.Commit(ctx); err != nil {
			return "", "", wrap("release savepoint", err)
		}
		return allocationID, unitCode, nil
	}
	return "", "", ErrNoUnitAvailable
}

// ReleaseHold expires a hold the guest abandoned, so the pitch comes back
// without waiting for the sweeper. Unknown or already-gone holds are not an
// error: the caller is telling us it is finished with a hold, and both answers
// to "was it still there?" satisfy that.
func (s *Store) ReleaseHold(ctx context.Context, token string) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			update unit_allocation set state = 'expired'
			 where id = $1 and kind = 'hold' and state = 'held'`, token)
		if err != nil {
			if db.IsBadInput(err) {
				return nil
			}
			return wrap("release hold", err)
		}
		return nil
	})
}

// SweepHolds expires every hold past its deadline for the pinned operator and
// reports how many it released.
//
// The sweeper exists because opportunistic expiry only fires when somebody
// searches. Camping demand is bursty: a category can sit untouched from
// midnight to breakfast, and inventory that recovers only under traffic
// recovers last thing at night and first thing in the morning, never between.
func (s *Store) SweepHolds(ctx context.Context) (int64, error) {
	var released int64
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update unit_allocation set state = 'expired'
			 where kind = 'hold' and state = 'held' and expires_at <= now()`)
		if err != nil {
			return wrap("sweep holds", err)
		}
		released = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return released, nil
}
