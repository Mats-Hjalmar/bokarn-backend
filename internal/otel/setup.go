package otel

import (
	"context"
	"errors"
	"net/url"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config holds the settings needed to set up OpenTelemetry exporters.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	OTLPEndpoint   string
	OTLPToken      string
	OTLPInsecure   bool
}

// Setup configures the global tracer and meter providers and returns a function
// that shuts them down. When the OTLP endpoint or token is empty, providers are
// created without exporters (a no-op fallback).
func Setup(
	ctx context.Context,
	cfg Config,
) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	shutdown = func(ctx context.Context) error {
		var errs []error
		for _, fn := range shutdownFuncs {
			if err := fn(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.DeploymentEnvironment(cfg.Environment),
		),
	)
	if err != nil {
		handleErr(err)
		return
	}

	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	otel.SetTextMapPropagator(prop)

	useOTLP := cfg.OTLPEndpoint != ""

	tracerProvider, err := newTracerProvider(ctx, res, cfg, useOTLP)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	meterProvider, err := newMeterProvider(ctx, res, cfg, useOTLP)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	if err = runtime.Start(
		runtime.WithMeterProvider(meterProvider),
	); err != nil {
		handleErr(err)
		return
	}

	loggerProvider, err := newLoggerProvider(ctx, res, cfg, useOTLP)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, loggerProvider.Shutdown)
	global.SetLoggerProvider(loggerProvider)

	return shutdown, nil
}

func newTracerProvider(
	ctx context.Context,
	res *resource.Resource,
	cfg Config,
	useOTLP bool,
) (*trace.TracerProvider, error) {
	if useOTLP {
		opts := []otlptracehttp.Option{
			otlptracehttp.WithEndpointURL(
				signalEndpointURL(cfg.OTLPEndpoint, "/v1/traces"),
			),
		}
		if cfg.OTLPInsecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if cfg.OTLPToken != "" {
			opts = append(opts, otlptracehttp.WithHeaders(map[string]string{
				"Authorization": "Bearer " + cfg.OTLPToken,
			}))
		}
		exporter, err := otlptracehttp.New(ctx, opts...)
		if err != nil {
			return nil, err
		}
		return trace.NewTracerProvider(
			trace.WithBatcher(exporter),
			trace.WithResource(res),
		), nil
	}

	return trace.NewTracerProvider(
		trace.WithResource(res),
	), nil
}

func newMeterProvider(
	ctx context.Context,
	res *resource.Resource,
	cfg Config,
	useOTLP bool,
) (*metric.MeterProvider, error) {
	if useOTLP {
		opts := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpointURL(
				signalEndpointURL(cfg.OTLPEndpoint, "/v1/metrics"),
			),
		}
		if cfg.OTLPInsecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		if cfg.OTLPToken != "" {
			opts = append(opts, otlpmetrichttp.WithHeaders(map[string]string{
				"Authorization": "Bearer " + cfg.OTLPToken,
			}))
		}
		exporter, err := otlpmetrichttp.New(ctx, opts...)
		if err != nil {
			return nil, err
		}
		return metric.NewMeterProvider(
			metric.WithReader(metric.NewPeriodicReader(exporter)),
			metric.WithResource(res),
		), nil
	}

	return metric.NewMeterProvider(
		metric.WithResource(res),
	), nil
}

func newLoggerProvider(
	ctx context.Context,
	res *resource.Resource,
	cfg Config,
	useOTLP bool,
) (*log.LoggerProvider, error) {
	if useOTLP {
		opts := []otlploghttp.Option{
			otlploghttp.WithEndpointURL(
				signalEndpointURL(cfg.OTLPEndpoint, "/v1/logs"),
			),
		}
		if cfg.OTLPInsecure {
			opts = append(opts, otlploghttp.WithInsecure())
		}
		if cfg.OTLPToken != "" {
			opts = append(opts, otlploghttp.WithHeaders(map[string]string{
				"Authorization": "Bearer " + cfg.OTLPToken,
			}))
		}
		exporter, err := otlploghttp.New(ctx, opts...)
		if err != nil {
			return nil, err
		}
		return log.NewLoggerProvider(
			log.WithProcessor(log.NewBatchProcessor(exporter)),
			log.WithResource(res),
		), nil
	}

	return log.NewLoggerProvider(
		log.WithResource(res),
	), nil
}

// signalEndpointURL appends the standard OTLP signal path to a base endpoint.
//
// WithEndpointURL cannot be handed a bare base URL: when the URL carries no
// path it pins the target to "/" precisely so the default signal path is NOT
// appended, and every export then POSTs to the collector root and 404s. The
// failure is invisible in request logs because it happens on the export path,
// not the request path.
func signalEndpointURL(endpoint, signalPath string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = signalPath
	}
	return u.String()
}
