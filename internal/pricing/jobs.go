package pricing

import (
	"context"
	"fmt"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/jobs"
	"github.com/jackc/pgx/v5"
)

// quoteRetention is how long an expired quote is kept. Long enough to
// investigate a booking dispute from the same week, short enough that an
// unauthenticated endpoint cannot grow the table without bound.
const quoteRetention = 30 * 24 * time.Hour

// CompileRates rebuilds the rate calendar for every operator.
//
// Seasons are the authoring surface and rate_day is the evaluation surface;
// this turns one into the other. It exists as a job, not only as an admin
// endpoint, because a freshly migrated and seeded database has seasons but no
// compiled nights — and a category nobody can price looks exactly like a
// category with no availability.
func CompileRates(store *Store) jobs.Job {
	return jobs.Job{
		Name:        "compile-rates",
		Description: "Rebuild the rate calendar from the seasons, for every operator",
		Interval:    24 * time.Hour,
		Run: func(ctx context.Context, d jobs.Deps) error {
			return jobs.ForEachTenant(ctx, d, func(ctx context.Context) error {
				from, to := CompileWindow()
				_, err := store.CompileAll(ctx, from, to)
				return err
			})
		},
	}
}

// CompileWindow is the date range a recompile covers, whether triggered by the
// scheduled job or by a season edit through the admin API. Wide enough for
// every season an operator is likely to have published, bounded so the work
// stays predictable — and defined once, because two windows that drift apart
// mean an edit that recompiles a range the job does not.
func CompileWindow() (from, to string) {
	return "2026-01-01", "2029-12-31"
}

// PruneQuotes deletes quotes that expired long ago.
//
// The quote endpoint is public and unauthenticated by design — a guest has to
// be able to see a price before they have an account — so without this the
// table grows as fast as anyone cares to call it.
func PruneQuotes(store *Store) jobs.Job {
	return jobs.Job{
		Name:        "prune-quotes",
		Description: "Delete quotes that expired more than 30 days ago",
		Interval:    time.Hour,
		Run: func(ctx context.Context, d jobs.Deps) error {
			return jobs.ForEachTenant(ctx, d, func(ctx context.Context) error {
				return store.db.Tx(ctx, func(tx pgx.Tx) error {
					_, err := tx.Exec(
						ctx,
						`delete from quote where expires_at < now() - $1::interval`,
						quoteRetention.String(),
					)
					if err != nil {
						return fmt.Errorf("prune quotes: %w", err)
					}
					return nil
				})
			})
		},
	}
}
