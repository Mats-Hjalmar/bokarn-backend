// Package worker is where background work is wired up.
//
// It exists so that cmd/api and cmd/job build the same registry from the same
// code. A job that a ticker runs but no operator can trigger by hand is a job
// nobody can test, and a job registered separately in two binaries drifts: the
// scheduled version gets a dependency the manual one does not, and the
// difference only shows up when somebody runs it to fix an incident.
//
// It lives under internal/ rather than in cmd/api because two commands cannot
// share a main package.
package worker

import (
	"context"
	"sync"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/assignment"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/booking"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/config"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/guest"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/jobs"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/logging"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/notify"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/outbox"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/pricing"
)

var logger = logging.New("worker")

// Register builds every job and puts it in the registry.
//
// The outbox handler map is the one place to look for what an owed side effect
// actually does. A kind with no handler here is not silently dropped: the
// dispatcher records ErrNoHandler against the message and retries, so a handler
// deleted while its messages were still queued is visible rather than lossy.
func Register(d jobs.Deps, cfg config.Config) {
	prices := pricing.NewStore(d.DB)
	bookings := booking.NewStore(
		d.DB,
		assignment.NewStore(d.DB),
		guest.NewStore(d.DB),
		prices,
		notify.NewStore(d.DB),
	)

	transport := notify.NewSMTP(cfg.SMTP.Host, cfg.SMTP.Port, cfg.SMTP.From)
	dispatcher := outbox.NewDispatcher(d.DB, map[string]outbox.Handler{
		booking.MessageBookingConfirmed: bookings.ConfirmationHandler(
			transport, cfg.Guest.SiteURL),
	})

	jobs.Register(pricing.CompileRates(prices))
	jobs.Register(pricing.PruneQuotes(prices))
	booking.RegisterJobs(
		cfg.Holds.SweepInterval, cfg.Outbox.DrainInterval,
		bookings, dispatcher)
}

// Run drives every registered job on its own ticker until the context is
// cancelled.
//
// Each job runs through jobs.RunOnce, which takes an advisory lock on the job's
// name, so several API replicas ticking at the same moment do not do the same
// work several times — and a manual `job run` while the API is up is skipped
// rather than duplicated.
//
// A failing job is logged and retried on its next tick. It does not stop the
// others and it does not stop the server: an SMTP server being down must not
// take the booking API with it.
func Run(ctx context.Context, d jobs.Deps) {
	var wg sync.WaitGroup
	for _, j := range jobs.All() {
		if j.Interval <= 0 {
			logger.WarnContext(ctx, "job has no interval and will not tick",
				"job", j.Name)
			continue
		}
		wg.Add(1)
		go func(j jobs.Job) {
			defer wg.Done()
			tick(ctx, d, j)
		}(j)
	}
	wg.Wait()
}

func tick(ctx context.Context, d jobs.Deps, j jobs.Job) {
	ticker := time.NewTicker(j.Interval)
	defer ticker.Stop()

	logger.InfoContext(ctx, "job scheduled",
		"job", j.Name, "interval", j.Interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := jobs.RunOnce(ctx, d, j); err != nil {
				logger.ErrorContext(ctx, "job failed",
					"job", j.Name, "err", err)
			}
		}
	}
}
