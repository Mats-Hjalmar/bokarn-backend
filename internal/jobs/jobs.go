// Package jobs is the registry of background work. Every job has exactly one
// implementation, run on a ticker inside cmd/api and on demand by cmd/job, so
// a scheduled run and a manually triggered run can never drift apart — which
// is also what makes each job testable from a Makefile target.
package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps is what a job is allowed to reach. It grows as domains land; a job that
// needs something not listed here is a signal to widen this struct
// deliberately rather than to reach for a package-level singleton.
type Deps struct {
	// Pool is the application pool, used for the advisory lock that serialises
	// a job across replicas. Row-level security applies to it, so it cannot be
	// used to reach tenant data without a tenant pinned.
	Pool *pgxpool.Pool

	// DB is the tenant-pinned handle a job uses once it knows which operator it
	// is working for.
	DB *tenant.DB

	// TenantIDs lists the operators to iterate. Background work is per tenant:
	// there is no cross-tenant query the application role could run, and adding
	// one would mean a policy that lets any request read everything.
	TenantIDs func(context.Context) ([]tenant.ID, error)
}

// ForEachTenant runs fn once per operator with that operator pinned, which is
// the only shape background work takes here.
func ForEachTenant(
	ctx context.Context,
	d Deps,
	fn func(context.Context) error,
) error {
	ids, err := d.TenantIDs(ctx)
	if err != nil {
		return fmt.Errorf("list tenants: %w", err)
	}
	for _, id := range ids {
		if err := fn(tenant.With(ctx, id)); err != nil {
			return fmt.Errorf("tenant %s: %w", id, err)
		}
	}
	return nil
}

// Job is one unit of recurring work.
type Job struct {
	// Name is the identifier used by `job run <name>` and by the Makefile.
	Name string
	// Description is shown by `job list`.
	Description string
	// Interval is the ticker cadence inside cmd/api.
	Interval time.Duration
	// Run does the work. It must be idempotent: the ticker and an operator can
	// invoke it concurrently, and RunOnce only serialises across processes.
	Run func(ctx context.Context, d Deps) error
}

var registry []Job

// Register adds a job to the registry. Called from package init functions in
// the domain packages that own the work.
func Register(j Job) { registry = append(registry, j) }

// All returns every registered job.
func All() []Job { return registry }

// Lookup finds a job by name.
func Lookup(name string) (Job, bool) {
	for _, j := range registry {
		if j.Name == name {
			return j, true
		}
	}
	return Job{}, false
}

// RunOnce executes a job while holding a transaction-scoped advisory lock on
// its name, so several API replicas plus a manual invocation cannot run the
// same job concurrently. A lock that is already held is not an error: the work
// is being done elsewhere, and RunOnce reports that it skipped.
func RunOnce(ctx context.Context, d Deps, j Job) (ran bool, err error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked bool
	err = tx.QueryRow(ctx,
		`select pg_try_advisory_xact_lock(hashtext($1))`, j.Name,
	).Scan(&locked)
	if err != nil {
		return false, fmt.Errorf("acquire job lock: %w", err)
	}
	if !locked {
		return false, nil
	}

	if err := j.Run(ctx, d); err != nil {
		return false, fmt.Errorf("run %s: %w", j.Name, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit job lock: %w", err)
	}
	return true, nil
}
