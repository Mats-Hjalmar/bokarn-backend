package otel

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// panics counts recovered HTTP handler panics. Domain counters are declared by
// the domain that owns them, not here.
var panics metric.Int64Counter

// Init builds the reliability counters. It returns an error rather than
// degrading to no-op recording, because a counter that silently never fires
// looks exactly like a system with no panics.
func Init() error {
	meter := otel.Meter("github.com/Mats-Hjalmar/bokarn-backend/internal/otel")

	var err error
	panics, err = meter.Int64Counter(
		"bokarn.panics",
		metric.WithDescription("Recovered HTTP handler panics."),
	)
	if err != nil {
		return err
	}
	return nil
}

// RecordPanic counts a recovered handler panic.
func RecordPanic(ctx context.Context) {
	panics.Add(ctx, 1)
}
