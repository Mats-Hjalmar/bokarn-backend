package pricing

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// ErrNoRatePlan is returned when a category has no active plan. It is an error
// rather than an empty result: a category nobody can price is a configuration
// mistake that should be visible to staff, not a silent gap in search results.
var ErrNoRatePlan = errors.New("pricing: no active rate plan for that category")

// ErrAmbiguousCategory is returned when a category code exists at more than one
// of the operator's sites and the caller did not say which.
//
// Codes are unique per site, not per operator, so a two-site campsite can have
// "pitch_el" at both. Picking one would mean quoting the wrong site's price at
// the wrong site — silently, and differently depending on row order.
var ErrAmbiguousCategory = errors.New(
	"pricing: category exists at several sites; specify one",
)

// Store loads what the engine needs and persists what it produced.
type Store struct {
	db *tenant.DB
}

// NewStore constructs a Store over the tenant-pinned handle.
func NewStore(d *tenant.DB) *Store { return &Store{db: d} }

// LoadSnapshot assembles everything the engine may see for one category and
// stay. Nothing else reaches the engine, which is what keeps it pure.
func (s *Store) LoadSnapshot(
	ctx context.Context,
	siteID, categoryCode, arrival, departure, campaignCode string,
) (Snapshot, string, error) {
	var snap Snapshot
	var planID string

	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		// Refuse rather than choose. An ambiguous code means the caller has to
		// name a site; guessing produces a wrong price nobody can detect.
		var matches int
		if err := tx.QueryRow(ctx, `
			select count(*) from unit_category c
			 where c.code = $1 and ($2 = '' or c.site_id::text = $2)`,
			categoryCode, siteID).Scan(&matches); err != nil {
			return fmt.Errorf("count matching categories: %w", err)
		}
		if matches > 1 {
			return ErrAmbiguousCategory
		}

		err := tx.QueryRow(ctx, `
			select p.id::text, p.code, p.currency, p.vat_code,
			       coalesce(p.derive_op, ''), coalesce(p.derive_value_bp, 0),
			       p.min_price_minor, p.max_price_minor,
			       c.max_occupancy, c.pets_allowed
			  from rate_plan p
			  join unit_category c on c.id = p.category_id
			 where c.code = $1 and p.is_active
			   and ($2 = '' or c.site_id::text = $2)
			 order by p.priority desc, p.code
			 limit 1`, categoryCode, siteID,
		).Scan(&planID, &snap.Plan.Code, &snap.Plan.Currency,
			&snap.Plan.VATCode, &snap.Plan.DeriveOp, &snap.Plan.DeriveValueBP,
			&snap.Plan.MinPriceMinor, &snap.Plan.MaxPriceMinor,
			&snap.CategoryMaxOccupancy, &snap.CategoryPetsAllowed)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoRatePlan
			}
			return fmt.Errorf("load rate plan: %w", err)
		}
		snap.Plan.ID = planID

		snap.Days = map[string]RateDay{}
		rows, err := tx.Query(ctx, `
			select day::text, base_minor, included_adults, included_children,
			       adult_extra_minor, child_extra_minor, pet_minor,
			       vehicle_minor, min_stay, coalesce(max_stay, 0),
			       arrival_mask, closed, closed_to_arrival, closed_to_departure
			  from rate_day
			 where rate_plan_id = $1
			   and day >= $2::date and day < $3::date
			 order by day`, planID, arrival, departure)
		if err != nil {
			return fmt.Errorf("load rate days: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d RateDay
			if err := rows.Scan(&d.Day, &d.BaseMinor, &d.IncludedAdults,
				&d.IncludedChildren, &d.AdultExtraMinor, &d.ChildExtraMinor,
				&d.PetMinor, &d.VehicleMinor, &d.MinStay, &d.MaxStay,
				&d.ArrivalMask, &d.Closed, &d.ClosedToArrival,
				&d.ClosedToDeparture); err != nil {
				return fmt.Errorf("scan rate day: %w", err)
			}
			snap.Days[d.Day] = d
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		bandRows, err := tx.Query(ctx, `
			select code, age_from, age_to, price_per_night_minor
			  from rate_age_band where rate_plan_id = $1 order by age_from`,
			planID)
		if err != nil {
			return fmt.Errorf("load age bands: %w", err)
		}
		defer bandRows.Close()
		for bandRows.Next() {
			var b AgeBand
			if err := bandRows.Scan(
				&b.Code, &b.AgeFrom, &b.AgeTo, &b.PricePerNightMinor,
			); err != nil {
				return fmt.Errorf("scan age band: %w", err)
			}
			snap.AgeBands = append(snap.AgeBands, b)
		}
		if err := bandRows.Err(); err != nil {
			return err
		}
		bandRows.Close()

		adjRows, err := tx.Query(ctx, `
			select a.name, a.priority, a.trigger, a.factor_bp, a.delta_minor
			  from pricing_adjuster a
			  join unit_category c on c.id = a.category_id
			 where c.code = $1 and a.enabled
			 order by a.priority, a.name`, categoryCode)
		if err != nil {
			return fmt.Errorf("load adjusters: %w", err)
		}
		defer adjRows.Close()
		for adjRows.Next() {
			var a Adjuster
			var trigger map[string]any
			var factor *int
			var delta *int64
			if err := adjRows.Scan(
				&a.Name, &a.Priority, &trigger, &factor, &delta,
			); err != nil {
				return fmt.Errorf("scan adjuster: %w", err)
			}
			applyTrigger(&a, trigger)
			if factor != nil {
				a.UsesFactor, a.FactorBP = true, *factor
			}
			if delta != nil {
				a.DeltaMinor = *delta
			}
			snap.Adjusters = append(snap.Adjusters, a)
		}
		if err := adjRows.Err(); err != nil {
			return err
		}
		adjRows.Close()

		losRows, err := tx.Query(ctx, `
			select min_nights, percent_bp
			  from rate_los_discount where rate_plan_id = $1
			 order by min_nights`, planID)
		if err != nil {
			return fmt.Errorf("load length-of-stay discounts: %w", err)
		}
		defer losRows.Close()
		for losRows.Next() {
			var d LOSDiscount
			if err := losRows.Scan(&d.MinNights, &d.PercentBP); err != nil {
				return fmt.Errorf("scan length-of-stay discount: %w", err)
			}
			snap.LOS = append(snap.LOS, d)
		}
		if err := losRows.Err(); err != nil {
			return err
		}
		losRows.Close()

		snap.VATCodes = map[string]VATCode{}
		vatRows, err := tx.Query(ctx, `
			select distinct on (code) code, rate_bp, vat_treatment
			  from vat_codes
			 where valid_from <= $1::date
			   and (valid_to is null or valid_to > $1::date)
			 order by code, valid_from desc`, arrival)
		if err != nil {
			return fmt.Errorf("load vat codes: %w", err)
		}
		defer vatRows.Close()
		for vatRows.Next() {
			var v VATCode
			if err := vatRows.Scan(
				&v.Code,
				&v.RateBP,
				&v.Treatment,
			); err != nil {
				return fmt.Errorf("scan vat code: %w", err)
			}
			snap.VATCodes[v.Code] = v
		}
		if err := vatRows.Err(); err != nil {
			return err
		}
		vatRows.Close()

		if campaignCode != "" {
			var c Campaign
			err := tx.QueryRow(ctx, `
				select code, kind, value from campaign
				 where upper(code) = upper($1) and is_active
				   and (book_from is null or book_from <= current_date)
				   and (book_to is null or book_to >= current_date)
				   and (stay_from is null or stay_from <= $2::date)
				   and (stay_to is null or stay_to >= $2::date)`,
				campaignCode, arrival).Scan(&c.Code, &c.Kind, &c.Value)
			switch {
			case err == nil:
				snap.Campaign = &c
			case errors.Is(err, pgx.ErrNoRows):
				// An unknown or expired code is reported to the caller rather
				// than applied as nothing: a guest who typed one deserves to
				// know it did not work.
				return ErrUnknownCampaign
			default:
				return fmt.Errorf("load campaign: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		return Snapshot{}, "", err
	}
	return snap, planID, nil
}

// ErrUnknownCampaign is returned when a supplied code matches no live campaign.
var ErrUnknownCampaign = errors.New("pricing: unknown or expired campaign code")

// applyTrigger reads the documented keys off an adjuster's trigger. An
// unrecognised key is left alone deliberately — the CHECK on the table and the
// admin UI police the shape, and silently ignoring a key here is the same as
// pricing on a rule nobody wrote.
func applyTrigger(a *Adjuster, trigger map[string]any) {
	if v, ok := numberIn(trigger, "occupancy_from_bp"); ok {
		a.HasOccupancy, a.OccupancyFrom = true, v
	}
	if v, ok := numberIn(trigger, "occupancy_to_bp"); ok {
		a.HasOccupancy, a.OccupancyTo = true, v
	} else if a.HasOccupancy {
		a.OccupancyTo = 10000
	}
	if v, ok := numberIn(trigger, "lead_days_from"); ok {
		a.HasLeadDays, a.LeadDaysFrom = true, v
	}
	if v, ok := numberIn(trigger, "lead_days_to"); ok {
		a.HasLeadDays, a.LeadDaysTo = true, v
	} else if a.HasLeadDays {
		a.LeadDaysTo = 1 << 30
	}
}

func numberIn(m map[string]any, key string) (int, bool) {
	v, ok := m[key].(float64)
	return int(v), ok
}
