// Package db constructs the shared pgx connection pool.
package db

import (
	"context"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// New opens a pgx connection pool, verifies it with a ping, and registers
// pool metrics on the global OpenTelemetry meter. Every query is traced via
// otelpgx, producing client spans for per-query latency.
func New(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	poolCfg.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	if err := registerMetrics(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("register metrics: %w", err)
	}

	return pool, nil
}
