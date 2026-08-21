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
			`select a.id::text, a.unit_id::text, a.kind, a.state,
			        a.arrival_date::text, a.departure_date::text,
			        a.block_reason, a.unit_pinned,
			        coalesce(b.id::text, ''), coalesce(b.reference, ''),
			        coalesce(nullif(g.surname, '') || coalesce(', ' ||
			                 nullif(g.given_names, ''), ''), '')
			   from unit_allocation a
			   left join booking b on b.id = a.booking_id
			   left join guest_identity g on g.id = b.guest_id
			  where a.unit_id is not null
			    and a.state in ('held','confirmed','checked_in','checked_out')
			    and a.stay && daterange($1::date, $2::date)
			  order by a.arrival_date`, from, to)
		if err != nil {
			return fmt.Errorf("query allocations: %w", err)
		}
		defer allocRows.Close()

		for allocRows.Next() {
			var a Allocation
			if err := allocRows.Scan(
				&a.ID, &a.UnitID, &a.Kind, &a.State,
				&a.Arrival, &a.Departure, &a.BlockReason, &a.UnitPinned,
				&a.BookingID, &a.Reference, &a.GuestName,
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
