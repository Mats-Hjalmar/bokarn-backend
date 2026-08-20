// Command job runs one background job to completion and exits. Every job the
// API runs on a ticker is reachable here by name, so an operator (or a smoke
// test) can trigger it without waiting for a cadence.
//
//	job list
//	job run <name>
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/config"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/jobs"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/platform"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/pricing"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"

	_ "github.com/Mats-Hjalmar/bokarn-backend/internal/otel"
)

var logger = slog.With("subsystem", "job")

func main() {
	if err := run(os.Args[1:]); err != nil {
		logger.Error("job failed", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: job list | job run <name>")
	}

	ctx := context.Background()

	deps, closeDeps, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeDeps()

	// Registration happens here rather than in package init functions so that
	// both subcommands see the same registry, and so a job's dependencies are
	// visible at the point it is registered.
	register(deps)

	switch args[0] {
	case "list":
		for _, j := range jobs.All() {
			fmt.Printf("%-20s %s (every %s)\n",
				j.Name, j.Description, j.Interval)
		}
		return nil
	case "run":
		if len(args) != 2 {
			return fmt.Errorf("usage: job run <name>%s", available())
		}
		return runOne(ctx, deps, args[1])
	default:
		return fmt.Errorf("unknown command %q: expected list or run", args[0])
	}
}

// register wires every job. Adding one here is the only step: cmd/api reads the
// same registry for its tickers, so a job cannot exist on a schedule without
// also being runnable by hand.
func register(deps jobs.Deps) {
	store := pricing.NewStore(deps.DB)
	jobs.Register(pricing.CompileRates(store))
	jobs.Register(pricing.PruneQuotes(store))
}

func open(ctx context.Context) (jobs.Deps, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return jobs.Deps{}, nil, fmt.Errorf("load config: %w", err)
	}

	pool, err := db.New(ctx, cfg.Database.AppDSN())
	if err != nil {
		return jobs.Deps{}, nil, fmt.Errorf("connect db: %w", err)
	}

	tenantDB, err := tenant.New(ctx, pool)
	if err != nil {
		pool.Close()
		return jobs.Deps{}, nil, err
	}

	platformStore, err := platform.New(ctx, cfg.Database.PlatformDSN())
	if err != nil {
		pool.Close()
		return jobs.Deps{}, nil, fmt.Errorf("connect platform db: %w", err)
	}

	deps := jobs.Deps{
		Pool:      pool,
		DB:        tenantDB,
		TenantIDs: tenantLister(platformStore),
	}
	return deps, func() {
		platformStore.Close()
		pool.Close()
	}, nil
}

func tenantLister(
	p *platform.Store,
) func(context.Context) ([]tenant.ID, error) {
	return func(ctx context.Context) ([]tenant.ID, error) {
		raw, err := p.TenantIDs(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]tenant.ID, 0, len(raw))
		for _, id := range raw {
			ids = append(ids, tenant.ID(id))
		}
		return ids, nil
	}
}

func runOne(ctx context.Context, deps jobs.Deps, name string) error {
	j, ok := jobs.Lookup(name)
	if !ok {
		return fmt.Errorf("unknown job %q%s", name, available())
	}

	ran, err := jobs.RunOnce(ctx, deps, j)
	if err != nil {
		return err
	}
	if !ran {
		return errors.New(
			"job " + name + " is already running elsewhere: lock not acquired",
		)
	}

	logger.InfoContext(ctx, "job completed", "job", name)
	return nil
}

func available() string {
	all := jobs.All()
	if len(all) == 0 {
		return "\nno jobs are registered"
	}
	names := make([]string, 0, len(all))
	for _, j := range all {
		names = append(names, j.Name)
	}
	return "\navailable: " + strings.Join(names, ", ")
}
