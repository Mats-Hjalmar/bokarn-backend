package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

func registerMetrics(pool *pgxpool.Pool) error {
	meter := otel.Meter("github.com/Mats-Hjalmar/bokarn-backend/internal/db")

	total, err := meter.Int64ObservableGauge(
		"db.pool.total_conns",
		metric.WithDescription("Total number of connections in the pool."),
	)
	if err != nil {
		return err
	}

	idle, err := meter.Int64ObservableGauge(
		"db.pool.idle_conns",
		metric.WithDescription("Number of idle connections in the pool."),
	)
	if err != nil {
		return err
	}

	acquired, err := meter.Int64ObservableGauge(
		"db.pool.acquired_conns",
		metric.WithDescription("Number of currently acquired connections."),
	)
	if err != nil {
		return err
	}

	maxConns, err := meter.Int64ObservableGauge(
		"db.pool.max_conns",
		metric.WithDescription("Maximum number of connections allowed."),
	)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			stat := pool.Stat()
			o.ObserveInt64(total, int64(stat.TotalConns()))
			o.ObserveInt64(idle, int64(stat.IdleConns()))
			o.ObserveInt64(acquired, int64(stat.AcquiredConns()))
			o.ObserveInt64(maxConns, int64(stat.MaxConns()))
			return nil
		},
		total, idle, acquired, maxConns,
	)

	return err
}
