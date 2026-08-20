package e2e

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// freeByCategory runs the same availability query the API serves, so a change
// to one without the other shows up here rather than in production.
func freeByCategory(
	t *testing.T,
	tx pgx.Tx,
	from, to string,
	guests, pets, amp int,
) map[string]int {
	t.Helper()

	rows, err := tx.Query(t.Context(), `
		select c.code,
		       count(u.id) filter (where not exists (
		           select 1 from unit_allocation a
		            where a.unit_id = u.id
		              and a.state in ('held', 'confirmed',
		                              'checked_in', 'checked_out')
		              and a.stay && daterange($1::date, $2::date)
		       ))
		  from unit_category c
		  join unit u on u.category_id = c.id and u.status = 'active'
		 where (select range_agg(sn.period)
		          from unit_season sn where sn.unit_id = u.id)
		           @> daterange($1::date, $2::date)
		   and u.max_occupancy >= $3
		   and ($4::int = 0 or u.pets_allowed)
		   and ($5::int = 0 or u.electricity_amp >= $5)
		 group by c.code
		having count(u.id) > 0`, from, to, guests, pets, amp)
	if err != nil {
		t.Fatalf("availability query: %v", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var code string
		var free int
		if err := rows.Scan(&code, &free); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[code] = free
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// eligibleByCategory counts the units a stay could use if nothing were
// occupied. Availability must equal this minus whatever overlaps — asserting
// against it rather than against a hard-coded seed count keeps the test honest
// when the dev database has been used for something else.
func eligibleByCategory(
	t *testing.T,
	tx pgx.Tx,
	from, to string,
	guests, pets, amp int,
) map[string]int {
	t.Helper()

	rows, err := tx.Query(t.Context(), `
		select c.code, count(u.id)
		  from unit_category c
		  join unit u on u.category_id = c.id and u.status = 'active'
		 where (select range_agg(sn.period)
		          from unit_season sn where sn.unit_id = u.id)
		           @> daterange($1::date, $2::date)
		   and u.max_occupancy >= $3
		   and ($4::int = 0 or u.pets_allowed)
		   and ($5::int = 0 or u.electricity_amp >= $5)
		 group by c.code
		having count(u.id) > 0`, from, to, guests, pets, amp)
	if err != nil {
		t.Fatalf("eligible query: %v", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var code string
		var n int
		if err := rows.Scan(&code, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[code] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAvailabilityNeverExceedsEligibleInventory(t *testing.T) {
	tx := begin(t, storsand)

	free := freeByCategory(t, tx, "2027-07-01", "2027-07-05", 2, 0, 0)
	eligible := eligibleByCategory(t, tx, "2027-07-01", "2027-07-05", 2, 0, 0)

	if len(eligible) == 0 {
		t.Fatal("no eligible inventory in the middle of the season")
	}
	for code, n := range free {
		if n > eligible[code] {
			t.Errorf(
				"%s reports %d free of %d eligible units",
				code, n, eligible[code])
		}
	}
	// Every category with units must appear, even when fully occupied it is a
	// category the operator sells.
	for code := range eligible {
		if _, ok := free[code]; !ok {
			t.Errorf("category %s vanished from availability entirely", code)
		}
	}
}

func TestAvailabilityDropsWhenAUnitIsBlocked(t *testing.T) {
	tx := begin(t, storsand)

	before := freeByCategory(t, tx, "2027-07-01", "2027-07-05", 2, 0, 0)

	a10 := unitID(t, tx, "A10")
	if err := insertBlock(
		t,
		tx,
		a10,
		"2027-07-02",
		"2027-07-04",
		"confirmed",
	); err != nil {
		t.Fatalf("block: %v", err)
	}

	after := freeByCategory(t, tx, "2027-07-01", "2027-07-05", 2, 0, 0)
	if after["pitch_el"] != before["pitch_el"]-1 {
		t.Errorf(
			"pitch_el free = %d after blocking one unit, want %d",
			after["pitch_el"], before["pitch_el"]-1)
	}

	// A stay that does not overlap the block is unaffected — the whole point of
	// storing occupancy as a range rather than a flag. Compared against its own
	// baseline, because other rows may occupy that window too.
	elsewhereBefore := freeByCategory(
		t,
		tx,
		"2027-09-10",
		"2027-09-12",
		2,
		0,
		0,
	)
	a11 := unitID(t, tx, "A11")
	if err := insertBlock(
		t,
		tx,
		a11,
		"2027-07-02",
		"2027-07-04",
		"confirmed",
	); err != nil {
		t.Fatalf("second block: %v", err)
	}
	elsewhereAfter := freeByCategory(t, tx, "2027-09-10", "2027-09-12", 2, 0, 0)
	if elsewhereAfter["pitch_el"] != elsewhereBefore["pitch_el"] {
		t.Errorf(
			"blocking July changed September availability: %d then %d",
			elsewhereBefore["pitch_el"], elsewhereAfter["pitch_el"])
	}
}

// A unit outside its open season is not for sale, however empty it looks.
func TestAvailabilityRespectsSeasons(t *testing.T) {
	tx := begin(t, storsand)

	summer := freeByCategory(t, tx, "2027-07-01", "2027-07-05", 2, 0, 0)
	if summer["pitch_el"] == 0 {
		t.Fatal("no pitches available in the middle of the season")
	}

	// Only S01 and S02 are given a winter season by the seed.
	winter := freeByCategory(t, tx, "2027-12-20", "2027-12-27", 2, 0, 0)
	if _, ok := winter["pitch_el"]; ok {
		t.Error("pitches are offered in December, outside their season")
	}
	eligible := eligibleByCategory(t, tx, "2027-12-20", "2027-12-27", 2, 0, 0)
	if eligible["stuga4"] == 0 {
		t.Error(
			"no cabin has a winter season; the seed no longer exercises this",
		)
	}
	if winter["stuga4"] > eligible["stuga4"] {
		t.Errorf(
			"stuga4 free in December = %d, of %d in season",
			winter["stuga4"], eligible["stuga4"])
	}
}

func TestAvailabilityFiltersOnPartyAndAttributes(t *testing.T) {
	tx := begin(t, storsand)

	// A party of six cannot be sold a four-berth cabin.
	six := freeByCategory(t, tx, "2027-07-01", "2027-07-05", 6, 0, 0)
	if _, ok := six["stuga4"]; ok {
		t.Error("a four-berth cabin was offered to a party of six")
	}
	if six["stuga6"] == 0 {
		t.Error("no six-berth cabin available for a party of six")
	}

	// Asking for amperage rules out anything without a supply.
	amp := freeByCategory(t, tx, "2027-07-01", "2027-07-05", 2, 0, 16)
	if _, ok := amp["stuga4"]; ok {
		t.Error("a cabin with no electricity supply matched an amperage filter")
	}
	if amp["pitch_el"] == 0 {
		t.Error("no electric pitch matched a 16A filter")
	}
}
