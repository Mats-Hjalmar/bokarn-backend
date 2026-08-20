package pricing

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Compile turns the authoring surface into the evaluation surface: every date
// in the window gets exactly one rate_day row on this plan, from the season
// that wins for that date.
//
// Overlap is resolved here, once, and the winner is recorded in
// source_season_id. Doing it at query time instead would make a price depend on
// when it was asked for, and leave nobody able to answer why a date costs what
// it costs after the seasons have been edited.
//
// The ordering is total — priority, then the later start, then the id — so two
// seasons that overlap perfectly still resolve the same way on every run.
func (s *Store) Compile(
	ctx context.Context,
	ratePlanID, from, to string,
) (int, error) {
	var compiled int

	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			insert into rate_day (
			    rate_plan_id, day, currency, base_minor,
			    included_adults, included_children,
			    adult_extra_minor, child_extra_minor,
			    pet_minor, vehicle_minor,
			    min_stay, max_stay, arrival_mask,
			    closed, closed_to_arrival, closed_to_departure,
			    source_season_id, compiled_at)
			select $1, g.day, p.currency, w.base_minor,
			       w.included_adults, w.included_children,
			       w.adult_extra_minor, w.child_extra_minor,
			       w.pet_minor, w.vehicle_minor,
			       w.min_stay, w.max_stay, w.arrival_mask,
			       w.closed, w.closed_to_arrival, w.closed_to_departure,
			       w.id, now()
			  from generate_series($2::date, $3::date, interval '1 day') as g(day)
			  join rate_plan p on p.id = $1
			  join lateral (
			      select rs.*
			        from rate_season rs
			       where rs.rate_plan_id = $1
			         and g.day::date between rs.starts_on and rs.ends_on
			         and (rs.weekday_mask
			              & (1 << (extract(isodow from g.day)::int - 1))) <> 0
			       order by rs.priority desc, rs.starts_on desc, rs.id
			       limit 1
			  ) w on true
			on conflict (tenant_id, rate_plan_id, day) do update set
			    currency = excluded.currency,
			    base_minor = excluded.base_minor,
			    included_adults = excluded.included_adults,
			    included_children = excluded.included_children,
			    adult_extra_minor = excluded.adult_extra_minor,
			    child_extra_minor = excluded.child_extra_minor,
			    pet_minor = excluded.pet_minor,
			    vehicle_minor = excluded.vehicle_minor,
			    min_stay = excluded.min_stay,
			    max_stay = excluded.max_stay,
			    arrival_mask = excluded.arrival_mask,
			    closed = excluded.closed,
			    closed_to_arrival = excluded.closed_to_arrival,
			    closed_to_departure = excluded.closed_to_departure,
			    source_season_id = excluded.source_season_id,
			    compiled_at = excluded.compiled_at`,
			ratePlanID, from, to)
		if err != nil {
			return fmt.Errorf("compile rate days: %w", err)
		}
		compiled = int(tag.RowsAffected())

		// A date the seasons no longer cover must stop being sellable. Leaving
		// a stale row behind would keep selling last year's price.
		if _, err := tx.Exec(ctx, `
			delete from rate_day d
			 where d.rate_plan_id = $1
			   and d.day between $2::date and $3::date
			   and not exists (
			       select 1 from rate_season rs
			        where rs.rate_plan_id = $1
			          and d.day between rs.starts_on and rs.ends_on
			          and (rs.weekday_mask
			               & (1 << (extract(isodow from d.day)::int - 1))) <> 0
			   )`, ratePlanID, from, to); err != nil {
			return fmt.Errorf("prune uncovered rate days: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return compiled, nil
}

// CompileAll recompiles every active plan over a window, which is what a season
// edit triggers.
func (s *Store) CompileAll(ctx context.Context, from, to string) (int, error) {
	var ids []string
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`select id::text from rate_plan where is_active order by code`)
		if err != nil {
			return fmt.Errorf("list rate plans: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}

	total := 0
	for _, id := range ids {
		n, err := s.Compile(ctx, id, from, to)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}
