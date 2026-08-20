package pricing

import (
	"context"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
)

// Plan is a rate plan as the dashboard lists it.
type Plan struct {
	ID           string   `json:"id"`
	Code         string   `json:"code"`
	Name         string   `json:"name"`
	CategoryCode string   `json:"category_code"`
	Currency     string   `json:"currency"`
	VATCode      string   `json:"vat_code"`
	Seasons      []Season `json:"seasons"`
}

// Season is the authoring surface staff edit.
type Season struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	StartsOn        string `json:"starts_on"`
	EndsOn          string `json:"ends_on"`
	Priority        int    `json:"priority"`
	BaseMinor       int64  `json:"base_minor"`
	IncludedAdults  int    `json:"included_adults"`
	AdultExtraMinor int64  `json:"adult_extra_minor"`
	PetMinor        int64  `json:"pet_minor"`
	MinStay         int    `json:"min_stay"`
	ArrivalMask     int    `json:"arrival_mask"`
}

// CalendarDay is one compiled night, with the season that produced it — which
// is the answer to "why does this date cost this".
type CalendarDay struct {
	Day         string `json:"day"`
	BaseMinor   int64  `json:"base_minor"`
	MinStay     int    `json:"min_stay"`
	ArrivalMask int    `json:"arrival_mask"`
	Closed      bool   `json:"closed"`
	SeasonID    string `json:"season_id"`
	SeasonName  string `json:"season_name"`
}

// SeasonUpdate is the editable subset. Dates and priority are deliberately not
// editable here: moving a season is a different operation from repricing one,
// and conflating them makes an accidental overlap look like a price change.
type SeasonUpdate struct {
	BaseMinor       int64 `json:"base_minor"`
	IncludedAdults  int   `json:"included_adults"`
	AdultExtraMinor int64 `json:"adult_extra_minor"`
	PetMinor        int64 `json:"pet_minor"`
	MinStay         int   `json:"min_stay"`
}

// ListPlans returns every plan with its seasons.
func (s *Store) ListPlans(ctx context.Context) ([]Plan, error) {
	plans := []Plan{}
	index := map[string]int{}

	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			select p.id::text, p.code, p.name, c.code, p.currency, p.vat_code
			  from rate_plan p
			  join unit_category c on c.id = p.category_id
			 where p.is_active
			 order by c.sort_order, p.code`)
		if err != nil {
			return fmt.Errorf("query rate plans: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var p Plan
			if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.CategoryCode,
				&p.Currency, &p.VATCode); err != nil {
				return fmt.Errorf("scan rate plan: %w", err)
			}
			p.Seasons = []Season{}
			index[p.ID] = len(plans)
			plans = append(plans, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		seasonRows, err := tx.Query(ctx, `
			select rate_plan_id::text, id::text, name,
			       starts_on::text, ends_on::text, priority, base_minor,
			       included_adults, adult_extra_minor, pet_minor,
			       min_stay, arrival_mask
			  from rate_season
			 order by starts_on, priority`)
		if err != nil {
			return fmt.Errorf("query seasons: %w", err)
		}
		defer seasonRows.Close()
		for seasonRows.Next() {
			var planID string
			var v Season
			if err := seasonRows.Scan(&planID, &v.ID, &v.Name, &v.StartsOn,
				&v.EndsOn, &v.Priority, &v.BaseMinor, &v.IncludedAdults,
				&v.AdultExtraMinor, &v.PetMinor, &v.MinStay,
				&v.ArrivalMask); err != nil {
				return fmt.Errorf("scan season: %w", err)
			}
			if i, ok := index[planID]; ok {
				plans[i].Seasons = append(plans[i].Seasons, v)
			}
		}
		return seasonRows.Err()
	})
	if err != nil {
		return nil, err
	}
	return plans, nil
}

// Calendar returns the compiled nights for a plan, each naming its season.
func (s *Store) Calendar(
	ctx context.Context,
	planID, from, to string,
) ([]CalendarDay, error) {
	out := []CalendarDay{}
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			select d.day::text, d.base_minor, d.min_stay, d.arrival_mask,
			       d.closed, d.source_season_id::text, s.name
			  from rate_day d
			  join rate_season s on s.id = d.source_season_id
			 where d.rate_plan_id = $1
			   and d.day >= $2::date and d.day < $3::date
			 order by d.day`, planID, from, to)
		if err != nil {
			return fmt.Errorf("query rate calendar: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d CalendarDay
			if err := rows.Scan(&d.Day, &d.BaseMinor, &d.MinStay,
				&d.ArrivalMask, &d.Closed, &d.SeasonID,
				&d.SeasonName); err != nil {
				return fmt.Errorf("scan rate day: %w", err)
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateSeason reprices a season. The caller recompiles afterwards; the two are
// separate so a batch of edits costs one compile rather than one each.
func (s *Store) UpdateSeason(
	ctx context.Context,
	id string,
	u SeasonUpdate,
) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update rate_season
			   set base_minor = $2, included_adults = $3,
			       adult_extra_minor = $4, pet_minor = $5, min_stay = $6
			 where id = $1`,
			id, u.BaseMinor, u.IncludedAdults, u.AdultExtraMinor,
			u.PetMinor, u.MinStay)
		if err != nil {
			// A value outside its column's range is bad input, not a fault:
			// min_stay of 99999 does not fit a smallint.
			if db.IsBadInput(err) {
				return ErrInvalidSeason
			}
			return fmt.Errorf("update season: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrSeasonNotFound
		}
		return nil
	})
}

// ErrSeasonNotFound is returned when no season matches the id.
var ErrSeasonNotFound = fmt.Errorf("pricing: season not found")

// ErrInvalidSeason is returned when a value does not fit the schema.
var ErrInvalidSeason = fmt.Errorf("pricing: season values out of range")

// PlanIDForSeason resolves which plan a season belongs to, so an edit can
// recompile exactly that plan.
func (s *Store) PlanIDForSeason(
	ctx context.Context,
	id string,
) (string, error) {
	var planID string
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`select rate_plan_id::text from rate_season where id = $1`, id,
		).Scan(&planID)
	})
	if err != nil {
		if db.IsBadInput(err) {
			return "", ErrSeasonNotFound
		}
		return "", fmt.Errorf("resolve season plan: %w", err)
	}
	return planID, nil
}
