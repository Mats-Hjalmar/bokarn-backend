// Command api is the bokarn backend HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/cache"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/config"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/httpx"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/jobs"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/logging"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/otel"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/platform"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/worker"
)

var logger = logging.New("api")

func main() {
	if err := run(); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx := context.Background()

	otelRuntime, err := otel.NewRuntime(ctx, otel.Config{
		ServiceName:    cfg.OTel.ServiceName,
		ServiceVersion: cfg.OTel.ServiceVersion,
		Environment:    cfg.OTel.Environment,
		OTLPEndpoint:   cfg.OTel.OTLPEndpoint,
		OTLPToken:      cfg.OTel.OTLPToken,
		OTLPInsecure:   cfg.OTel.OTLPInsecure,
	})
	if err != nil {
		return fmt.Errorf("setup otel: %w", err)
	}
	defer func() {
		if err := otelRuntime.Shutdown(context.Background()); err != nil {
			logger.Error("otel shutdown failed", "err", err)
		}
	}()

	pool, err := db.New(ctx, cfg.Database.AppDSN())
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()

	// tenant.New asserts the role cannot bypass row-level security, so the
	// process refuses to start rather than running with every policy inert.
	tenantDB, err := tenant.New(ctx, pool)
	if err != nil {
		return err
	}

	platformStore, err := platform.New(ctx, cfg.Database.PlatformDSN())
	if err != nil {
		return fmt.Errorf("connect platform db: %w", err)
	}
	defer platformStore.Close()

	redisClient, err := cache.New(ctx, cfg.Redis)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			logger.Error("redis close failed", "err", err)
		}
	}()

	srv, err := httpx.NewServer(cfg, tenantDB, redisClient, platformStore)
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}

	// Background work runs in this process, on tickers, alongside the server.
	// The registry is built by internal/worker so cmd/job builds the same one:
	// every job the API ticks is reachable by name from the command line, which
	// is what makes each one testable from a Makefile target.
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()

	deps := jobs.Deps{
		Pool:      pool,
		DB:        tenantDB,
		TenantIDs: tenantLister(platformStore),
	}
	worker.Register(deps, cfg)

	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		worker.Run(workerCtx, deps)
	}()

	serverErr := make(chan error, 1)
	go func() {
		logger.InfoContext(ctx, "server listening", "addr", srv.Addr())
		if err := srv.Start(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	case <-stop:
		logger.InfoContext(ctx, "shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	// Jobs are stopped after the server, so a request that enqueued an outbox
	// message during shutdown still has a dispatcher to deliver it.
	stopWorkers()
	select {
	case <-workersDone:
	case <-shutdownCtx.Done():
		logger.Warn("background jobs did not stop in time")
	}

	return nil
}

// tenantLister is how background work learns which operators exist. It reads
// through the platform role, which is the only role that can see across
// operators, and every such read is audited by that store.
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
