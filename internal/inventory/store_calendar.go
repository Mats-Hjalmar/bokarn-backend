package inventory

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Calendar returns every unit and whatever occupies it inside the window, which
// is the tape chart's entire read model.
//
// Two queries rather than one join: a join would repeat each unit's twenty-odd
// attribute columns once per allocation, and the window is wide by design.
func (s *Store) Calendar(
	ctx context.Context,
	from, to string,
) ([]CalendarRow, error) {
	rows := []CalendarRow{}
	index := map[string]int{}

	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		unitRows, err := tx.Query(
			ctx,
			unitSelect+` where u.status = 'active' order by u.sort_order, u.code`,
		)
		if err != nil {
			return fmt.Errorf("query units: %w", err)
		}
		defer unitRows.Close()

		for unitRows.Next() {
			u, err := scanUnit(unitRows)
			if err != nil {
				return err
			}
			index[u.ID] = len(rows)
			rows = append(
				rows,
				CalendarRow{Unit: u, Allocations: []Allocation{}},
			)
		}
		if err := unitRows.Err(); err != nil {
			return err
		}
		unitRows.Close()

		// && rather than a pair of comparisons: it is what the GiST index on
		// (tenant_id, unit_id, stay) can actually answer.
		allocRows, err := tx.Query(ctx,
			`select id::text, unit_id::text, kind, state,
			        arrival_date::text, departure_date::text,
			        block_reason, unit_pinned
			   from unit_allocation
			  where unit_id is not null
			    and state in ('held','confirmed','checked_in','checked_out')
			    and stay && daterange($1::date, $2::date)
			  order by arrival_date`, from, to)
		if err != nil {
			return fmt.Errorf("query allocations: %w", err)
		}
		defer allocRows.Close()

		for allocRows.Next() {
			var a Allocation
			if err := allocRows.Scan(
				&a.ID, &a.UnitID, &a.Kind, &a.State,
				&a.Arrival, &a.Departure, &a.BlockReason, &a.UnitPinned,
			); err != nil {
				return fmt.Errorf("scan allocation: %w", err)
			}
			// A unit that is retired mid-season still has allocations; it is
			// simply not shown, so an orphan here is expected rather than an
			// error.
			if i, ok := index[a.UnitID]; ok {
				rows[i].Allocations = append(rows[i].Allocations, a)
			}
		}
		return allocRows.Err()
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}
