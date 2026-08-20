// Package e2e proves the multi-tenant isolation model against a real Postgres.
//
// Every connection here is made as the application role, never as a superuser
// and never as the migrator. That is not a detail: FORCE ROW LEVEL SECURITY
// does not constrain a superuser or a BYPASSRLS role, so a suite that connected
// as one would pass every assertion below while proving nothing at all.
// scripts/rls-mutation.sh is the standing check that this stayed true.
package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	storsand  = "11111111-1111-1111-1111-111111111111"
	hamnviken = "22222222-2222-2222-2222-222222222222"
)

var appDSN string

func TestMain(m *testing.M) {
	appDSN = os.Getenv("BOKARN_TEST_APP_DSN")
	if appDSN == "" {
		appDSN = "host=localhost port=1438 user=bokarn_app " +
			"password=bokarn_app dbname=bokarn sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect as application role: %v\n", err)
		os.Exit(1)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(
			os.Stderr,
			"the suite needs a running stack: make -C backend up migrate seed\n%v\n",
			err,
		)
		os.Exit(1)
	}
	pool.Close()

	os.Exit(m.Run())
}

// appPool opens a pool as the application role and fails the test if that role
// can see past row-level security, so no assertion can pass vacuously.
func appPool(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(appDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}

	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	var role string
	var super, bypass bool
	err = pool.QueryRow(t.Context(),
		`select current_user, rolsuper, rolbypassrls
		   from pg_roles where rolname = current_user`,
	).Scan(&role, &super, &bypass)
	if err != nil {
		t.Fatalf("read current role: %v", err)
	}
	if super || bypass {
		t.Fatalf(
			"suite is connected as %q (superuser=%v bypassrls=%v): "+
				"every policy is inert and these tests prove nothing",
			role, super, bypass)
	}

	return pool
}
