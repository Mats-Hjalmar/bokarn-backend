package availability

import (
	"context"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Store answers availability from the occupancy table.
type Store struct {
	db *tenant.DB
}

// NewStore constructs a Store over the tenant-pinned handle.
func NewStore(d *tenant.DB) *Store { return &Store{db: d} }

// Search returns every category that can host the stay, with the number of
// units free for its whole length.
//
// The query carries no tenant filter: the policy applies it. A unit counts as
// free when nothing overlapping occupies it, where "occupies" excludes
// cancelled and expired rows — the same state list the exclusion constraint
// uses, so the count and the constraint can never disagree about what a
// conflict is.
//
// Seasons are checked with range_agg because a unit may be open across several
// disjoint periods, and the stay has to fall wholly inside their union.
func (s *Store) Search(
	ctx context.Context,
	q Query,
) ([]CategoryOffer, error) {
	out := []CategoryOffer{}

	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			select c.code, c.name, c.kind, c.max_occupancy,
			       count(u.id) filter (where not exists (
			           select 1 from unit_allocation a
			            where a.unit_id = u.id
			              and a.state in ('held', 'confirmed',
			                              'checked_in', 'checked_out')
			              and a.stay && daterange($1::date, $2::date)
			       ))
			  from unit_category c
			  join unit u
			    on u.category_id = c.id
			   and u.status = 'active'
			 where (select range_agg(sn.period)
			          from unit_season sn where sn.unit_id = u.id)
			           @> daterange($1::date, $2::date)
			   and u.max_occupancy >= $3
			   and ($4::int = 0 or u.pets_allowed)
			   and ($5::int = 0 or u.electricity_amp >= $5)
			   and ($6::boolean is not true or u.accessible)
			 group by c.code, c.name, c.kind, c.max_occupancy, c.sort_order
			having count(u.id) > 0
			 order by c.sort_order, c.code`,
			q.Arrival, q.Departure, q.Guests(), q.Pets,
			q.ElectricityAmp, q.Accessible)
		if err != nil {
			return fmt.Errorf("query availability: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var o CategoryOffer
			if err := rows.Scan(
				&o.Code, &o.Name, &o.Kind, &o.MaxOccupancy, &o.Free,
			); err != nil {
				return fmt.Errorf("scan offer: %w", err)
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
