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
					_, err := tx.Exec(ctx,
						`delete from quote where expires_at < now() - $1::interval`,
						quoteRetention.String())
					if err != nil {
						return fmt.Errorf("prune quotes: %w", err)
					}
					return nil
				})
			})
		},
	}
}
