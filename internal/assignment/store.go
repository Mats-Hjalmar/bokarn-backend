package assignment

import (
	"context"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Store finds the units a stay could go on.
type Store struct {
	db *tenant.DB
}

// NewStore constructs a Store over the tenant-pinned handle.
func NewStore(d *tenant.DB) *Store { return &Store{db: d} }

// Query is the stay looking for a pitch.
type Query struct {
	CategoryCode   string
	Arrival        string
	Departure      string
	Guests         int
	Pets           int
	ElectricityAmp int
	Accessible     bool
}

// Candidates returns every unit in the category that could host the stay,
// scored and ordered best first, along with the site and category it resolved.
//
// The hard filters are here rather than in Cost because a unit that cannot
// host the stay is not a worse choice, it is not a choice: a party of six does
// not get a four-berth cabin at a penalty. What reaches Cost is a list of units
// that would all be correct, differing only in how wasteful each would be.
//
// Nothing is locked. The exclusion constraint is the concurrency authority, so
// the caller walks this list inserting until one insert survives; a candidate
// another request took in the meantime raises 23P01 and the loop moves on.
// Locking here would serialise every booking in the category behind one another
// to prevent a conflict Postgres already prevents.
func (s *Store) Candidates(
	ctx context.Context,
	query Query,
) (cands []Candidate, siteID, categoryID string, err error) {
	err = s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		cands, siteID, categoryID, err = s.CandidatesOn(ctx, tx, query)
		return err
	})
	if err != nil {
		return nil, "", "", err
	}
	return cands, siteID, categoryID, nil
}

// CandidatesOn is Candidates on a transaction the caller already holds. The
// confirm path needs it: re-running admission there has to see what the insert
// that follows will see, and a separate read transaction would not.
func (s *Store) CandidatesOn(
	ctx context.Context,
	q db.TX,
	query Query,
) (cands []Candidate, siteID, categoryID string, err error) {
	err = q.QueryRow(ctx, `
		select c.site_id::text, c.id::text
		  from unit_category c where c.code = $1`, query.CategoryCode,
	).Scan(&siteID, &categoryID)
	if err != nil {
		return nil, "", "", fmt.Errorf(
			"resolve category %q: %w", query.CategoryCode, err)
	}

	stay := Daterange(query.Arrival, query.Departure)

	rows, err := q.Query(ctx, `
		select u.id::text, u.sort_order,
		       coalesce(u.electricity_amp, 0), u.max_occupancy,
		       u.accessible, u.sanitary,
		       u.view is not null and u.view <> '',
		       coalesce((
		           select lower($2::daterange) - max(upper(a.stay))
		             from unit_allocation a
		            where a.unit_id = u.id
		              and a.state in ('held', 'confirmed',
		                              'checked_in', 'checked_out')
		              and upper(a.stay) <= lower($2::daterange)
		       ), $3::int),
		       coalesce((
		           select min(lower(a.stay)) - upper($2::daterange)
		             from unit_allocation a
		            where a.unit_id = u.id
		              and a.state in ('held', 'confirmed',
		                              'checked_in', 'checked_out')
		              and lower(a.stay) >= upper($2::daterange)
		       ), $3::int)
		  from unit u
		 where u.category_id = $1 and u.status = 'active'
		   and (select range_agg(sn.period)
		          from unit_season sn where sn.unit_id = u.id) @> $2::daterange
		   and u.max_occupancy >= $4
		   and ($5::int = 0 or u.pets_allowed)
		   and ($6::int = 0 or coalesce(u.electricity_amp, 0) >= $6)
		   and ($7::boolean is not true or u.accessible)
		   and not exists (
		       select 1 from unit_allocation a
		        where a.unit_id = u.id
		          and a.state in ('held', 'confirmed',
		                          'checked_in', 'checked_out')
		          and a.stay && $2::daterange
		   )`,
		categoryID, stay, MaxLookahead, query.Guests, query.Pets,
		query.ElectricityAmp, query.Accessible)
	if err != nil {
		return nil, "", "", fmt.Errorf("query candidates: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var c Candidate
		if err := rows.Scan(
			&c.UnitID, &c.SortOrder, &c.ElectricityAmp, &c.MaxOccupancy,
			&c.Accessible, &c.Sanitary, &c.HasView,
			&c.FreeBefore, &c.FreeAfter,
		); err != nil {
			return nil, "", "", fmt.Errorf("scan candidate: %w", err)
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", "", err
	}

	Order(cands, Request{
		Guests:          query.Guests,
		ElectricityAmp:  query.ElectricityAmp,
		NeedsAccessible: query.Accessible,
	})
	return cands, siteID, categoryID, nil
}

// Daterange formats the half-open literal Postgres canonicalises to. Departure
// is the exclusive upper bound, never the last night.
func Daterange(arrival, departure string) string {
	return fmt.Sprintf("[%s,%s)", arrival, departure)
}
