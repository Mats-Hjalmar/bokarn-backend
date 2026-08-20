package e2e

import (
	"testing"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
)

func TestPricingIsTenantIsolated(t *testing.T) {
	cases := []struct {
		tenant string
		name   string
		plans  int
	}{
		{storsand, "storsand", 3},
		{hamnviken, "hamnviken", 1},
	}

	for _, c := range cases {
		tx := begin(t, c.tenant)

		var plans, days, seasons int
		if err := tx.QueryRow(t.Context(), `
			select (select count(*) from rate_plan),
			       (select count(*) from rate_day),
			       (select count(*) from rate_season)`,
		).Scan(&plans, &days, &seasons); err != nil {
			t.Fatalf("count pricing as %s: %v", c.name, err)
		}
		if plans != c.plans {
			t.Errorf("%s sees %d rate plans, want %d", c.name, plans, c.plans)
		}
		if seasons == 0 {
			t.Errorf("%s sees no seasons at all", c.name)
		}
		// Hamnviken's calendar is deliberately never compiled by the seed, so
		// asserting a count here would test the seed rather than isolation.
		if c.tenant == storsand && days == 0 {
			t.Error("storsand's rate calendar is empty")
		}
	}
}

func TestOneOperatorCannotRepriceAnother(t *testing.T) {
	var foreign string
	func() {
		tx := begin(t, storsand)
		if err := tx.QueryRow(
			t.Context(),
			`select id::text from rate_season limit 1`,
		).Scan(&foreign); err != nil {
			t.Fatalf("find a storsand season: %v", err)
		}
	}()

	tx := begin(t, hamnviken)
	tag, err := tx.Exec(t.Context(),
		`update rate_season set base_minor = 1 where id = $1`, foreign)
	if err != nil {
		t.Fatalf("update errored rather than matching nothing: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Errorf(
			"repriced %d of another operator's seasons",
			tag.RowsAffected(),
		)
	}
}

// Every compiled night must name the season it came from. Without that, "why
// does this date cost this" has no answer once the seasons have been edited.
func TestEveryCompiledNightNamesItsSeason(t *testing.T) {
	tx := begin(t, storsand)

	var orphans int
	if err := tx.QueryRow(t.Context(), `
		select count(*) from rate_day d
		 where not exists (
		     select 1 from rate_season s where s.id = d.source_season_id
		 )`).Scan(&orphans); err != nil {
		t.Fatalf("count orphaned rate days: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d compiled nights have no source season", orphans)
	}
}

// A night can be compiled from exactly one season. Two rows for one date would
// mean the price depends on which the query happened to read first.
func TestOneRatePerPlanPerNight(t *testing.T) {
	tx := begin(t, storsand)

	var duplicates int
	if err := tx.QueryRow(t.Context(), `
		select count(*) from (
		    select rate_plan_id, day from rate_day
		     group by rate_plan_id, day having count(*) > 1
		) as d`).Scan(&duplicates); err != nil {
		t.Fatalf("check for duplicate nights: %v", err)
	}
	if duplicates != 0 {
		t.Errorf("%d plan/night pairs have more than one rate", duplicates)
	}
}

// The quote table stores the breakdown rather than the inputs, so a later rate
// change cannot reach a price a guest was already shown. This is the property
// the whole freeze depends on.
func TestStoredQuoteSurvivesARateChange(t *testing.T) {
	tx := begin(t, storsand)

	var quoteID string
	var before int64
	err := tx.QueryRow(t.Context(), `
		insert into quote (
		    site_id, category_id, rate_plan_id, arrival, departure, currency,
		    engine_version, input_hash, breakdown_hash, payload,
		    total_gross_minor, total_net_minor, total_vat_minor, expires_at)
		select s.id, c.id, p.id, '2027-07-04', '2027-07-11', 'SEK', 1,
		       '\x00', '\x00', '{"totals":{"gross_minor":390645}}'::jsonb,
		       390645, 348790, 41855, now() + interval '30 minutes'
		  from sites s
		  join unit_category c on c.site_id = s.id and c.code = 'pitch_el'
		  join rate_plan p on p.category_id = c.id
		 limit 1
		returning id::text, total_gross_minor`).Scan(&quoteID, &before)
	if err != nil {
		t.Fatalf("insert quote: %v", err)
	}

	if _, err := tx.Exec(t.Context(), `
		update rate_day set base_minor = base_minor + 20000
		 where day between '2027-07-04' and '2027-07-10'`); err != nil {
		t.Fatalf("reprice: %v", err)
	}

	var after int64
	if err := tx.QueryRow(t.Context(),
		`select total_gross_minor from quote where id = $1`, quoteID,
	).Scan(&after); err != nil {
		t.Fatalf("reread quote: %v", err)
	}
	if after != before {
		t.Errorf("stored quote moved from %d to %d after a rate change",
			before, after)
	}
}

// A quote's totals must agree with themselves. The CHECK is what stops a
// rounding change from writing an internally inconsistent row.
func TestQuoteTotalsMustReconcile(t *testing.T) {
	tx := begin(t, storsand)

	_, err := tx.Exec(t.Context(), `
		insert into quote (
		    site_id, category_id, rate_plan_id, arrival, departure, currency,
		    engine_version, input_hash, breakdown_hash, payload,
		    total_gross_minor, total_net_minor, total_vat_minor, expires_at)
		select s.id, c.id, p.id, '2027-07-04', '2027-07-11', 'SEK', 1,
		       '\x00', '\x00', '{}'::jsonb, 1000, 900, 50, now()
		  from sites s
		  join unit_category c on c.site_id = s.id and c.code = 'pitch_el'
		  join rate_plan p on p.category_id = c.id
		 limit 1`)
	if err == nil {
		t.Fatal("a quote whose net + vat != gross was accepted")
	}
	if got := sqlState(err); got != db.CheckViolation {
		t.Errorf("sqlstate = %q, want %q", got, db.CheckViolation)
	}
}
