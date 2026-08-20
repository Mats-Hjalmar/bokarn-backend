// Package cache constructs the shared Redis client.
package cache

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/config"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

// New opens a Redis client, instruments it with OpenTelemetry tracing and
// metrics, and verifies it with a ping.
func New(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	opts := &redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	if cfg.TLS {
		opts.TLSConfig = &tls.Config{}
	}
	client := redis.NewClient(opts)

	if err := redisotel.InstrumentTracing(client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("instrument tracing: %w", err)
	}
	if err := redisotel.InstrumentMetrics(client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("instrument metrics: %w", err)
	}

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return client, nil
}
