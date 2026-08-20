package e2e

import (
	"testing"
)

// globalTables are the only relations allowed to exist without a tenant_id and
// without row-level security. Extending this list is a code review, which is
// the point of keeping it in Go rather than deriving it from the schema.
//
//	currencies, countries, permissions  fixed reference vocabularies
//	schema_version                      tern's own bookkeeping
//	platform_audit_log                  records reads that span operators, so
//	                                    it cannot belong to one of them
var globalTables = map[string]string{
	"currencies":         "reference data",
	"countries":          "reference data",
	"permissions":        "the fixed capability vocabulary the code enforces",
	"schema_version":     "migration bookkeeping",
	"platform_audit_log": "cross-tenant reads are not attributable to a tenant",
}

// The two predicate families every policy must match. The second exists for
// exactly one table — tenants keys on its own id — and is written down here so
// the exception is reviewed rather than silently widening the check.
const (
	predTenantColumn = "(tenant_id = current_tenant_id())"
	predSelfID       = "(id = current_tenant_id())"
)

func TestEveryTableIsTenantScopedOrAllowlisted(t *testing.T) {
	pool := appPool(t, 0)

	rows, err := pool.Query(t.Context(), `
		select c.relname,
		       exists (
		           select 1 from pg_attribute a
		            where a.attrelid = c.oid and a.attname = 'tenant_id'
		              and a.attnum > 0 and not a.attisdropped
		       ) as has_tenant_id
		  from pg_class c
		  join pg_namespace n on n.oid = c.relnamespace
		 where n.nspname = 'public' and c.relkind = 'r'
		 order by c.relname`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var hasTenantID bool
		if err := rows.Scan(&name, &hasTenantID); err != nil {
			t.Fatalf("scan: %v", err)
		}

		_, allowed := globalTables[name]
		switch {
		case name == "tenants":
			// keyed on its own id, checked below
		case hasTenantID && allowed:
			t.Errorf(
				"%s has tenant_id but is on the global allowlist: "+
					"remove it from one or the other", name)
		case !hasTenantID && !allowed:
			t.Errorf(
				"%s has no tenant_id and is not allowlisted: every table is "+
					"either tenant-scoped or explicitly global", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestTenantTablesForceRLSWithExactlyFourPolicies(t *testing.T) {
	pool := appPool(t, 0)

	rows, err := pool.Query(t.Context(), `
		select c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       count(p.polname)
		  from pg_class c
		  join pg_namespace n on n.oid = c.relnamespace
		  left join pg_policy p on p.polrelid = c.oid
		 where n.nspname = 'public' and c.relkind = 'r'
		 group by 1, 2, 3
		 order by 1`)
	if err != nil {
		t.Fatalf("query relations: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var enabled, forced bool
		var policies int
		if err := rows.Scan(&name, &enabled, &forced, &policies); err != nil {
			t.Fatalf("scan: %v", err)
		}

		if _, global := globalTables[name]; global {
			if enabled || policies > 0 {
				t.Errorf(
					"%s is allowlisted as global but carries RLS (%d policies)",
					name, policies)
			}
			continue
		}

		if !enabled {
			t.Errorf("%s does not have row-level security enabled", name)
		}
		// Without FORCE the table owner is exempt, and the owner is the role
		// migrations run as.
		if !forced {
			t.Errorf("%s has RLS enabled but not forced", name)
		}
		// Not >= 4: an *added* permissive policy is the subtle way isolation
		// gets widened, and it would pass a lower-bound check.
		if policies != 4 {
			t.Errorf(
				"%s has %d policies, want exactly 4 (select/insert/update/delete)",
				name,
				policies,
			)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestEveryPolicyPredicateIsAKnownFamily(t *testing.T) {
	pool := appPool(t, 0)

	rows, err := pool.Query(t.Context(), `
		select c.relname, p.polname, p.polcmd,
		       coalesce(pg_get_expr(p.polqual, p.polrelid), ''),
		       coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '')
		  from pg_policy p
		  join pg_class c on c.oid = p.polrelid
		 order by 1, 2`)
	if err != nil {
		t.Fatalf("query policies: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var table, policy, cmd, using, check string
		if err := rows.Scan(&table, &policy, &cmd, &using, &check); err != nil {
			t.Fatalf("scan: %v", err)
		}

		want := predTenantColumn
		if table == "tenants" {
			want = predSelfID
		}

		for _, expr := range []struct {
			kind string
			got  string
		}{{"using", using}, {"with check", check}} {
			if expr.got != "" && expr.got != want {
				t.Errorf(
					"%s.%s %s = %s, want %s",
					table, policy, expr.kind, expr.got, want)
			}
		}

		// Each command must be covered by its own policy, never one FOR ALL:
		// FOR ALL silently defaults WITH CHECK to USING, hiding the read/write
		// asymmetry from anyone reading the catalog later.
		if cmd == "*" {
			t.Errorf("%s.%s is a FOR ALL policy", table, policy)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationRoleOwnsNothing(t *testing.T) {
	pool := appPool(t, 0)

	var owned int
	err := pool.QueryRow(t.Context(), `
		select count(*)
		  from pg_class c
		  join pg_namespace n on n.oid = c.relnamespace
		 where n.nspname = 'public' and c.relkind = 'r'
		   and pg_get_userbyid(c.relowner) = current_user`).Scan(&owned)
	if err != nil {
		t.Fatalf("query ownership: %v", err)
	}
	// A table owner is exempt from its own policies unless FORCE is set, so the
	// application role owning anything is one missing FORCE away from a leak.
	if owned != 0 {
		t.Errorf("application role owns %d tables, want 0", owned)
	}
}
