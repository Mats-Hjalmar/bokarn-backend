package e2e

import (
	"testing"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
)

// unitID returns one of the operator's units by code, inside the caller's
// already-pinned transaction.
func unitID(t *testing.T, tx pgx.Tx, code string) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(
		t.Context(),
		`select id::text from unit where code = $1`,
		code,
	).Scan(&id); err != nil {
		t.Fatalf("look up unit %s: %v", code, err)
	}
	return id
}

func begin(t *testing.T, tenant string) pgx.Tx {
	t.Helper()
	pool := appPool(t, 0)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(t.Context()) })
	pin(t, tx, tenant)
	return tx
}

func insertBlock(
	t *testing.T,
	tx pgx.Tx,
	unit, from, to, state string,
) error {
	t.Helper()
	_, err := tx.Exec(t.Context(),
		`insert into unit_allocation
		     (site_id, category_id, unit_id, kind, state, stay)
		 select u.site_id, u.category_id, u.id, 'block', $4,
		        daterange($2::date, $3::date)
		   from unit u where u.id = $1`, unit, from, to, state)
	return err
}

// The guarantee that two stays cannot share a unit lives in the database, not
// in Go. If this ever passes with the constraint removed, the constraint was
// never doing the work.
func TestOverlappingAllocationsAreRefused(t *testing.T) {
	tx := begin(t, storsand)
	a01 := unitID(t, tx, "A01")

	if err := insertBlock(
		t,
		tx,
		a01,
		"2027-05-02",
		"2027-05-06",
		"confirmed",
	); err != nil {
		t.Fatalf("first block: %v", err)
	}

	_, err := tx.Exec(t.Context(), `savepoint sp`)
	if err != nil {
		t.Fatal(err)
	}
	err = insertBlock(t, tx, a01, "2027-05-04", "2027-05-08", "confirmed")
	if err == nil {
		t.Fatal("an overlapping allocation was accepted")
	}
	if got := sqlState(err); got != db.ExclusionViolation {
		t.Errorf("sqlstate = %q, want %q", got, db.ExclusionViolation)
	}
}

// daterange canonicalises to [), so the departure day is the next guest's
// arrival day. Getting this wrong loses a night of revenue on every turnover.
func TestSameDayTurnoverIsAllowed(t *testing.T) {
	tx := begin(t, storsand)
	a02 := unitID(t, tx, "A02")

	if err := insertBlock(
		t,
		tx,
		a02,
		"2027-05-02",
		"2027-05-06",
		"confirmed",
	); err != nil {
		t.Fatalf("first block: %v", err)
	}
	if err := insertBlock(
		t,
		tx,
		a02,
		"2027-05-06",
		"2027-05-09",
		"confirmed",
	); err != nil {
		t.Errorf("adjacent allocation refused: %v", err)
	}
}

// The constraint's predicate lists the states that actually occupy a unit. A
// cancelled row must not keep a pitch off the market.
func TestCancelledAllocationsDoNotReserve(t *testing.T) {
	tx := begin(t, storsand)
	a03 := unitID(t, tx, "A03")

	if err := insertBlock(
		t,
		tx,
		a03,
		"2027-05-02",
		"2027-05-06",
		"cancelled",
	); err != nil {
		t.Fatalf("cancelled block: %v", err)
	}
	if err := insertBlock(
		t,
		tx,
		a03,
		"2027-05-03",
		"2027-05-05",
		"confirmed",
	); err != nil {
		t.Errorf("a cancelled allocation blocked a live one: %v", err)
	}
}

func TestInventoryIsTenantIsolated(t *testing.T) {
	cases := []struct {
		tenant     string
		name       string
		units      int
		categories int
	}{
		{storsand, "storsand", 48, 3},
		{hamnviken, "hamnviken", 12, 1},
	}

	for _, c := range cases {
		tx := begin(t, c.tenant)

		var units, categories int
		if err := tx.QueryRow(t.Context(),
			`select (select count(*) from unit),
			        (select count(*) from unit_category)`,
		).Scan(&units, &categories); err != nil {
			t.Fatalf("count inventory as %s: %v", c.name, err)
		}
		if units != c.units || categories != c.categories {
			t.Errorf(
				"%s sees %d units / %d categories, want %d / %d",
				c.name, units, categories, c.units, c.categories)
		}
	}
}

// The exclusion constraint is scoped by tenant_id, so it must never fire
// across operators — and more importantly, one operator must not be able to
// learn that another's unit is occupied by watching an insert fail.
func TestOneOperatorCannotAllocateAnothersUnit(t *testing.T) {
	var foreign string
	func() {
		tx := begin(t, storsand)
		foreign = unitID(t, tx, "A01")
	}()

	tx := begin(t, hamnviken)

	// The insert selects the unit by id; the policy hides it, so no row is
	// found and nothing is written. The failure is indistinguishable from the
	// unit not existing, which is the point.
	tag, err := tx.Exec(t.Context(),
		`insert into unit_allocation
		     (site_id, category_id, unit_id, kind, state, stay)
		 select u.site_id, u.category_id, u.id, 'block', 'confirmed',
		        daterange('2027-06-01','2027-06-03')
		   from unit u where u.id = $1`, foreign)
	if err != nil {
		t.Fatalf("insert errored rather than matching nothing: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Errorf(
			"wrote %d rows against another operator's unit, want 0",
			tag.RowsAffected())
	}
}
