// Command migrate applies outstanding database migrations and exits. It is the
// one-shot step run before the app rolls out (the compose migrate service, a
// deploy job) — the server in cmd/api never migrates on startup.
//
// It connects as the migrator role, which owns the schema and bypasses RLS.
// No other binary may use that DSN.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/config"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/migrations"

	_ "github.com/Mats-Hjalmar/bokarn-backend/internal/otel"
)

var logger = slog.With("subsystem", "migrate")

func main() {
	if err := run(); err != nil {
		logger.Error("migrations failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()

	if err := db.Migrate(
		ctx, cfg.Database.MigratorDSN(), migrations.FS,
	); err != nil {
		return err
	}

	logger.InfoContext(ctx, "migrations applied")
	return nil
}
