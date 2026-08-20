package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AssertRestrictedRole fails when the pool is connected as a role that can see
// past row-level security. FORCE ROW LEVEL SECURITY does not apply to
// superusers and BYPASSRLS roles, so a service running as one has every policy
// silently inert and no test will notice. Called at boot so the process refuses
// to start rather than serving one tenant's data to another.
func AssertRestrictedRole(ctx context.Context, pool *pgxpool.Pool) error {
	var role string
	var super, bypass bool
	err := pool.QueryRow(ctx,
		`select current_user, rolsuper, rolbypassrls
		   from pg_roles where rolname = current_user`,
	).Scan(&role, &super, &bypass)
	if err != nil {
		return fmt.Errorf("read current role: %w", err)
	}

	switch {
	case super:
		return fmt.Errorf(
			"app role %q is a superuser and ignores row-level security", role,
		)
	case bypass:
		return fmt.Errorf(
			"app role %q bypasses row-level security", role,
		)
	}
	return nil
}
