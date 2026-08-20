package db

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/tern/v2/migrate"
)

const migrationsVersionTable = "schema_version"

// migrationLockTimeout caps how long a migration statement waits to acquire a
// lock before aborting, so a schema change cannot sit in Postgres' lock queue
// and stall live reads/writes behind it. Migrations should be re-run rather
// than left blocking traffic.
const migrationLockTimeout = "3s"

// Migrate applies all outstanding migrations from fsys against the database
// at dsn. It opens a dedicated short-lived connection so it can run as a
// standalone one-shot step (the `migrate` subcommand, the compose migrate
// service, the e2e suite, a future deploy job) without touching the
// application pool. tern wraps each migration in a transaction and serializes
// concurrent runners with a Postgres advisory lock, so this is safe to invoke
// from multiple processes.
func Migrate(ctx context.Context, dsn string, fsys fs.FS) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(
		ctx,
		"SET lock_timeout = '"+migrationLockTimeout+"'",
	); err != nil {
		return fmt.Errorf("set lock_timeout: %w", err)
	}

	migrator, err := migrate.NewMigrator(ctx, conn, migrationsVersionTable)
	if err != nil {
		return fmt.Errorf("new migrator: %w", err)
	}

	if err := migrator.LoadMigrations(fsys); err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	if err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	return nil
}
