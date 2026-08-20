package otel

import (
	"context"
	"errors"
	"sync"
)

// Runtime owns the active OpenTelemetry providers and their shutdown hook,
// allowing the configuration to be swapped out at runtime.
type Runtime struct {
	mu       sync.Mutex
	shutdown func(context.Context) error
}

// NewRuntime sets up OpenTelemetry with the given config and returns a Runtime.
func NewRuntime(ctx context.Context, cfg Config) (*Runtime, error) {
	shutdown, err := Setup(ctx, cfg)
	if err != nil {
		return nil, err
	}

	if err := Init(); err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}

	return &Runtime{shutdown: shutdown}, nil
}

// Update re-initializes OpenTelemetry with the new config and shuts down the
// previous providers.
func (r *Runtime) Update(ctx context.Context, cfg Config) error {
	newShutdown, err := Setup(ctx, cfg)
	if err != nil {
		return err
	}

	r.mu.Lock()
	oldShutdown := r.shutdown
	r.shutdown = newShutdown
	r.mu.Unlock()

	if oldShutdown != nil {
		if err := oldShutdown(ctx); err != nil {
			return err
		}
	}

	return nil
}

// Shutdown flushes and stops the active OpenTelemetry providers.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	shutdown := r.shutdown
	r.shutdown = nil
	r.mu.Unlock()

	if shutdown == nil {
		return nil
	}

	return shutdown(ctx)
}
