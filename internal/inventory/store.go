package inventory

import (
	"context"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Store reads inventory within the pinned operator.
type Store struct {
	db *tenant.DB
}

// NewStore constructs a Store over the tenant-pinned handle.
func NewStore(d *tenant.DB) *Store { return &Store{db: d} }

// ListSites returns the operator's sites. No tenant filter appears in the
// query: the policy applies it, and a WHERE clause here would be a second,
// silently divergent implementation of the same rule.
func (s *Store) ListSites(ctx context.Context) ([]Site, error) {
	var out []Site
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`select id::text, name, slug, coalesce(municipality, ''),
			        country, timezone,
			        to_char(check_in_time, 'HH24:MI'),
			        to_char(check_out_time, 'HH24:MI')
			   from sites order by name`)
		if err != nil {
			return fmt.Errorf("query sites: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var st Site
			if err := rows.Scan(
				&st.ID, &st.Name, &st.Slug, &st.Municipality,
				&st.Country, &st.Timezone, &st.CheckInTime, &st.CheckOutTime,
			); err != nil {
				return fmt.Errorf("scan site: %w", err)
			}
			out = append(out, st)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Site{}
	}
	return out, nil
}
