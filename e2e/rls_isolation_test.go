package e2e

import (
	"errors"
	"testing"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pin sets the transaction-local tenant exactly as internal/tenant does.
func pin(t *testing.T, tx pgx.Tx, id string) {
	t.Helper()
	var applied string
	if err := tx.QueryRow(t.Context(),
		`select set_config('app.tenant_id', $1, true)`, id,
	).Scan(&applied); err != nil {
		t.Fatalf("pin tenant: %v", err)
	}
	if applied != id {
		t.Fatalf("pin tenant: applied %q, want %q", applied, id)
	}
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// Without a tenant pinned the database must look empty rather than open. This
// is the property that makes a forgotten pin a bug that shows up immediately
// instead of a leak that shows up never.
func TestUnpinnedReadsSeeNothing(t *testing.T) {
	pool := appPool(t, 0)

	for _, table := range []string{"tenants", "sites", "users", "roles"} {
		var n int
		if err := pool.QueryRow(t.Context(),
			`select count(*) from `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("unpinned read of %s returned %d rows, want 0", table, n)
		}
	}
}

func TestUnpinnedWriteIsRefused(t *testing.T) {
	pool := appPool(t, 0)

	_, err := pool.Exec(t.Context(),
		`insert into sites (name, slug, country) values ('x', 'x', 'SE')`)
	if err == nil {
		t.Fatal("unpinned insert succeeded, want a policy violation")
	}
	// The WITH CHECK predicate is evaluated before the NOT NULL constraint, so
	// an unpinned insert surfaces as 42501 rather than 23502. Should RLS ever
	// be disabled, the NOT NULL still refuses the write as 23502 — the second
	// line of defence, and what the mutation script observes.
	if got := sqlState(err); got != db.InsufficientPrivilege &&
		got != db.NotNullViolation {
		t.Errorf("sqlstate = %q, want %q or %q",
			got, db.InsufficientPrivilege, db.NotNullViolation)
	}
}

func TestPinnedReadsSeeOnlyTheirOwnOperator(t *testing.T) {
	pool := appPool(t, 0)

	cases := []struct {
		tenant string
		want   string
	}{{storsand, "storsand"}, {hamnviken, "hamnviken"}}

	for _, c := range cases {
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatalf("begin: %v", err)
		}

		pin(t, tx, c.tenant)

		var slug string
		if err := tx.QueryRow(t.Context(),
			`select slug from tenants`).Scan(&slug); err != nil {
			t.Fatalf("read tenants as %s: %v", c.want, err)
		}
		if slug != c.want {
			t.Errorf("tenants visible = %q, want %q", slug, c.want)
		}

		var sites int
		if err := tx.QueryRow(t.Context(),
			`select count(*) from sites`).Scan(&sites); err != nil {
			t.Fatalf("count sites: %v", err)
		}
		if sites == 0 {
			t.Errorf("%s sees no sites of its own", c.want)
		}

		var others int
		if err := tx.QueryRow(t.Context(),
			`select count(*) from sites where tenant_id <> $1`,
			c.tenant).Scan(&others); err != nil {
			t.Fatalf("count foreign sites: %v", err)
		}
		if others != 0 {
			t.Errorf("%s can see %d sites of another operator", c.want, others)
		}

		_ = tx.Rollback(t.Context())
	}
}

func TestWritingIntoAnotherOperatorIsRefused(t *testing.T) {
	pool := appPool(t, 0)

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()

	pin(t, tx, storsand)

	_, err = tx.Exec(t.Context(),
		`insert into sites (tenant_id, name, slug, country)
		 values ($1, 'Smuggel', 'smuggel', 'SE')`, hamnviken)
	if err == nil {
		t.Fatal("cross-tenant insert succeeded, want a policy violation")
	}
	if got := sqlState(err); got != db.InsufficientPrivilege {
		t.Errorf("sqlstate = %q, want %q", got, db.InsufficientPrivilege)
	}
}

// A tenant_id that is simply omitted must land on the pinned operator, which is
// why every table defaults it. A store that never names the column cannot write
// to the wrong operator even if a request body asks it to.
func TestOmittedTenantDefaultsToThePinnedOperator(t *testing.T) {
	pool := appPool(t, 0)

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()

	pin(t, tx, hamnviken)

	var got string
	err = tx.QueryRow(t.Context(),
		`insert into sites (name, slug, country)
		 values ('Default Test', 'default-test', 'SE')
		 returning tenant_id::text`).Scan(&got)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got != hamnviken {
		t.Errorf("tenant_id defaulted to %q, want %q", got, hamnviken)
	}
}

// Foreign keys are checked with RLS bypassed, so a single-column reference to a
// parent would let one operator confirm the existence of another's row by
// watching which inserts fail. Composite keys close that.
func TestForeignKeysAreComposite(t *testing.T) {
	pool := appPool(t, 0)

	rows, err := pool.Query(t.Context(), `
		select c.conrelid::regclass::text, c.conname, array_length(c.conkey, 1)
		  from pg_constraint c
		  join pg_class rel on rel.oid = c.conrelid
		  join pg_namespace n on n.oid = rel.relnamespace
		 where c.contype = 'f' and n.nspname = 'public'
		   and exists (
		       select 1 from pg_attribute a
		        where a.attrelid = c.conrelid and a.attname = 'tenant_id'
		          and a.attnum > 0 and not a.attisdropped
		   )
		   and exists (
		       select 1 from pg_attribute a
		        where a.attrelid = c.confrelid and a.attname = 'tenant_id'
		          and a.attnum > 0 and not a.attisdropped
		   )`)
	if err != nil {
		t.Fatalf("query constraints: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var table, name string
		var cols int
		if err := rows.Scan(&table, &name, &cols); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if cols < 2 {
			t.Errorf(
				"%s.%s references a tenant-scoped parent on %d column(s): "+
					"tenant-scoped references must be composite", table, name, cols)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
