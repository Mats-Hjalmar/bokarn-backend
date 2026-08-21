package booking

import (
	"context"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/jobs"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/logging"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/outbox"
)

var logger = logging.New("booking")

// RegisterJobs wires the two recurring pieces of work this domain owns.
//
// Both are registered from cmd/api rather than from an init function, because a
// job that registers itself on import is a job that runs in whatever binary
// happens to link the package — including the migrator.
func RegisterJobs(
	sweepInterval, drainInterval time.Duration,
	store *Store,
	dispatcher *outbox.Dispatcher,
) {
	jobs.Register(jobs.Job{
		Name: "sweep-holds",
		Description: "Expire holds whose deadline has passed, " +
			"returning their pitches to inventory.",
		Interval: sweepInterval,
		Run: func(ctx context.Context, d jobs.Deps) error {
			return jobs.ForEachTenant(ctx, d, func(ctx context.Context) error {
				released, err := store.SweepHolds(ctx)
				if err != nil {
					return err
				}
				if released > 0 {
					logger.InfoContext(ctx, "holds expired",
						"released", released)
				}
				return nil
			})
		},
	})

	jobs.Register(jobs.Job{
		Name: "drain-outbox",
		Description: "Deliver owed side effects: confirmation emails and " +
			"anything else written to the outbox.",
		Interval: drainInterval,
		Run: func(ctx context.Context, d jobs.Deps) error {
			return jobs.ForEachTenant(ctx, d, func(ctx context.Context) error {
				delivered, err := dispatcher.Drain(ctx)
				if err != nil {
					return err
				}
				if delivered > 0 {
					logger.InfoContext(ctx, "outbox drained",
						"delivered", delivered)
				}
				return nil
			})
		},
	})
}
