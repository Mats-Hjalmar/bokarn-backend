// Command api is the bokarn backend HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/cache"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/config"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/httpx"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/otel"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/platform"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
)

var logger = slog.With("subsystem", "api")

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

	return nil
}
