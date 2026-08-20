package inventory

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CreateBlock takes a unit out of service for a date range.
//
// A block is an ordinary occupancy row, so the same exclusion constraint that
// stops two bookings colliding stops a block landing on top of one. That is why
// no availability check happens here: the constraint is the authority, and
// asking first would be a race as well as a duplicate rule.
func (s *Store) CreateBlock(
	ctx context.Context,
	in NewBlock,
) (Allocation, error) {
	var a Allocation
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`insert into unit_allocation
			     (site_id, category_id, unit_id, kind, state, stay, block_reason)
			 select u.site_id, u.category_id, u.id, 'block', 'confirmed',
			        daterange($2::date, $3::date), nullif($4, '')
			   from unit u where u.id = $1
			 returning id::text, unit_id::text, kind, state,
			           arrival_date::text, departure_date::text,
			           block_reason, unit_pinned`,
			in.UnitID, in.Arrival, in.Departure, in.Reason,
		).Scan(&a.ID, &a.UnitID, &a.Kind, &a.State,
			&a.Arrival, &a.Departure, &a.BlockReason, &a.UnitPinned)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == db.ExclusionViolation {
			return Allocation{}, ErrOverlap
		}
		// A malformed id is the caller's mistake, not a fault. Postgres reports
		// it as a cast failure, which would otherwise reach the client as a 500.
		if errors.Is(err, pgx.ErrNoRows) || db.IsBadInput(err) {
			return Allocation{}, ErrNotFound
		}
		return Allocation{}, fmt.Errorf("insert block: %w", err)
	}
	return a, nil
}

// DeleteBlock removes a maintenance block. It refuses anything that is not a
// block: a booking is cancelled through the booking domain, which has a
// cancellation policy to apply and a guest to notify.
func (s *Store) DeleteBlock(ctx context.Context, id string) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`delete from unit_allocation where id = $1 and kind = 'block'`, id)
		if err != nil {
			if db.IsBadInput(err) {
				return ErrNotFound
			}
			return fmt.Errorf("delete block: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
