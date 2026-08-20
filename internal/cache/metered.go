package cache

import (
	"context"
	"fmt"

	"github.com/joakimcarlsson/minmux/outputcache"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "github.com/Mats-Hjalmar/bokarn-backend/internal/cache"

// Metered wraps an outputcache.Storage and records a cache.requests counter
// tagged result=hit|miss on every Get, so the output-cache hit ratio is
// visible in Grafana. All other operations pass straight through.
//
// A counter that cannot be created is an error, not a reason to return the
// storage unwrapped: a hit ratio that is absent looks identical to one that is
// zero, and the cache is the layer most likely to be serving stale or
// cross-tenant data when something is wrong.
func Metered(inner outputcache.Storage) (outputcache.Storage, error) {
	counter, err := otel.Meter(meterName).Int64Counter(
		"cache.requests",
		metric.WithDescription(
			"Output-cache lookups, tagged result=hit|miss.",
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create cache requests counter: %w", err)
	}
	return &metered{Storage: inner, counter: counter}, nil
}

type metered struct {
	outputcache.Storage
	counter metric.Int64Counter
}

func (m *metered) Get(key string) (*outputcache.CachedResponse, bool) {
	resp, ok := m.Storage.Get(key)
	result := "miss"
	if ok {
		result = "hit"
	}
	m.counter.Add(
		context.Background(),
		1,
		metric.WithAttributes(attribute.String("result", result)),
	)
	return resp, ok
}
