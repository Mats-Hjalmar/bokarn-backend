// Package config loads application configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all application configuration.
type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Redis    RedisConfig
	OTel     OTelConfig
	Log      LogConfig
	Kratos   KratosConfig
	Staff    KratosConfig
	Outbox   OutboxConfig
	Holds    HoldsConfig
	SMTP     SMTPConfig
	Guest    GuestConfig
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port int
}

// DatabaseConfig holds Postgres connection settings for all three roles.
//
// The roles are not interchangeable and the separation is the whole security
// posture: Migrator owns the schema and bypasses RLS, App may only run DML and
// must never bypass RLS, Platform bypasses RLS for audited cross-tenant reads.
type DatabaseConfig struct {
	Host     string
	Port     int
	Name     string
	SSLMode  string
	URL      string
	User     string
	Password string

	MigratorUser     string
	MigratorPassword string
	PlatformUser     string
	PlatformPassword string
}

// AppDSN is the connection string for cmd/api and cmd/job. URL, when set,
// wins so an operator can point the service at a managed database — and so the
// boot assertion can be exercised by starting the API with a privileged DSN.
func (c DatabaseConfig) AppDSN() string {
	if c.URL != "" {
		return c.URL
	}
	return c.dsn(c.User, c.Password)
}

// MigratorDSN is the connection string for cmd/migrate.
func (c DatabaseConfig) MigratorDSN() string {
	return c.dsn(c.MigratorUser, c.MigratorPassword)
}

// PlatformDSN is the connection string for internal/platform.
func (c DatabaseConfig) PlatformDSN() string {
	return c.dsn(c.PlatformUser, c.PlatformPassword)
}

func (c DatabaseConfig) dsn(user, password string) string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, user, password, c.Name, c.SSLMode,
	)
}

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	Host     string
	Port     int
	Password string
	DB       int
	TLS      bool
}

// OTelConfig holds OpenTelemetry export settings. ServiceVersion doubles as the
// release tag and Environment as the deploy tag, so logs, metrics and traces
// can be filtered by deploy in Grafana.
type OTelConfig struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	OTLPEndpoint   string
	OTLPToken      string
	OTLPInsecure   bool
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string
	Format string
}

// KratosConfig holds the URLs and session-cache TTL for one Ory Kratos
// instance. Guests and staff are separate identity populations on separate
// instances, so a guest session can never satisfy a staff route.
type KratosConfig struct {
	PublicURL       string
	AdminURL        string
	SessionCacheTTL time.Duration
}

// OutboxConfig holds the cadence of the outbox dispatcher ticker.
type OutboxConfig struct {
	DrainInterval time.Duration
}

// HoldsConfig holds the cadence of the hold-expiry sweeper. Camping demand is
// bursty and a category can sit untouched for hours, so inventory recovery
// cannot rely on opportunistic expiry alone.
type HoldsConfig struct {
	SweepInterval time.Duration
}

// SMTPConfig is where guest mail goes.
//
// There is no username or password because local development sends to a
// catcher that wants neither, and production sends through a relay whose
// credentials are a secret-store reference rather than an environment
// variable. Adding empty auth fields here would make an unauthenticated
// production relay look configured.
type SMTPConfig struct {
	Host string
	Port int
	// From is the envelope sender. Per-operator sender addresses need domain
	// verification at the relay, which is an onboarding step rather than a
	// column, so one address sends for every operator and the operator's name
	// carries the identity in the message itself.
	From string
}

// GuestConfig is how the guest-facing site is addressed from the server side.
type GuestConfig struct {
	// SiteURLTemplate builds the public URL of one operator's site, with
	// {slug} standing for the operator. A template rather than a base URL
	// because operators live on their own hostnames, and a link in a
	// confirmation email has to land on the campsite the guest booked — not on
	// a shared host that would then have to guess.
	SiteURLTemplate string
}

// SiteURL is the public address of one operator's guest site.
func (c GuestConfig) SiteURL(slug string) string {
	return strings.ReplaceAll(c.SiteURLTemplate, "{slug}", slug)
}

// Load reads configuration from the environment (and an optional .env file)
// and returns the populated Config. A variable that is set but cannot be parsed
// is an error, not a fall back to the default — a typo in BOKARN_DB_PORT must
// not quietly point the service at a different database. All such failures are
// reported together.
func Load() (Config, error) {
	_ = godotenv.Load()

	var e env

	cfg := Config{
		Server: ServerConfig{
			Port: e.int("BOKARN_PORT", 1437),
		},
		Database: DatabaseConfig{
			Host:    e.str("BOKARN_DB_HOST", "localhost"),
			Port:    e.int("BOKARN_DB_PORT", 1438),
			Name:    e.str("BOKARN_DB_NAME", "bokarn"),
			SSLMode: e.str("BOKARN_DB_SSLMODE", "disable"),
			URL:     os.Getenv("BOKARN_DATABASE_URL"),

			User:     e.str("BOKARN_DB_APP_USER", "bokarn_app"),
			Password: e.str("BOKARN_DB_APP_PASSWORD", "bokarn_app"),

			MigratorUser: e.str(
				"BOKARN_DB_MIGRATOR_USER",
				"bokarn_migrator",
			),
			MigratorPassword: e.str(
				"BOKARN_DB_MIGRATOR_PASSWORD",
				"bokarn_migrator",
			),
			PlatformUser: e.str(
				"BOKARN_DB_PLATFORM_USER",
				"bokarn_platform",
			),
			PlatformPassword: e.str(
				"BOKARN_DB_PLATFORM_PASSWORD",
				"bokarn_platform",
			),
		},
		Redis: RedisConfig{
			Host:     e.str("BOKARN_REDIS_HOST", "localhost"),
			Port:     e.int("BOKARN_REDIS_PORT", 1439),
			Password: os.Getenv("BOKARN_REDIS_PASSWORD"),
			DB:       e.int("BOKARN_REDIS_DB", 0),
			TLS:      e.bool("BOKARN_REDIS_TLS", false),
		},
		OTel: OTelConfig{
			ServiceName:    e.str("OTEL_SERVICE_NAME", "bokarn-backend"),
			ServiceVersion: e.str("OTEL_SERVICE_VERSION", "dev"),
			Environment:    e.str("DEPLOYMENT_ENVIRONMENT", "dev"),
			OTLPEndpoint:   os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			OTLPToken:      os.Getenv("OTEL_EXPORTER_OTLP_TOKEN"),
			OTLPInsecure:   e.bool("OTEL_EXPORTER_OTLP_INSECURE", false),
		},
		Log: LogConfig{
			Level:  e.str("BOKARN_LOG_LEVEL", "info"),
			Format: e.str("BOKARN_LOG_FORMAT", "json"),
		},
		Kratos: KratosConfig{
			PublicURL: e.str(
				"BOKARN_KRATOS_PUBLIC_URL",
				"http://auth.bokarn.localhost",
			),
			AdminURL: e.str(
				"BOKARN_KRATOS_ADMIN_URL",
				"http://auth-admin.bokarn.localhost",
			),
			SessionCacheTTL: e.duration(
				"BOKARN_KRATOS_SESSION_CACHE_TTL",
				time.Minute,
			),
		},
		Staff: KratosConfig{
			PublicURL: e.str(
				"BOKARN_KRATOS_STAFF_PUBLIC_URL",
				"http://auth-staff.bokarn.localhost",
			),
			AdminURL: e.str(
				"BOKARN_KRATOS_STAFF_ADMIN_URL",
				"http://auth-staff-admin.bokarn.localhost",
			),
			SessionCacheTTL: e.duration(
				"BOKARN_KRATOS_STAFF_SESSION_CACHE_TTL",
				time.Minute,
			),
		},
		Outbox: OutboxConfig{
			DrainInterval: e.duration(
				"BOKARN_OUTBOX_DRAIN_INTERVAL",
				5*time.Second,
			),
		},
		Holds: HoldsConfig{
			SweepInterval: e.duration(
				"BOKARN_HOLDS_SWEEP_INTERVAL",
				30*time.Second,
			),
		},
		Guest: GuestConfig{
			SiteURLTemplate: e.str(
				"BOKARN_GUEST_SITE_URL_TEMPLATE",
				"http://{slug}.bokarn.localhost",
			),
		},
		SMTP: SMTPConfig{
			Host: e.str("BOKARN_SMTP_HOST", "localhost"),
			Port: e.int("BOKARN_SMTP_PORT", 1425),
			From: e.str("BOKARN_SMTP_FROM", "bokningar@bokarn.local"),
		},
	}

	if err := e.err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// env reads typed values from the environment, collecting parse failures so
// Load reports every malformed variable at once rather than the first.
type env struct {
	errs []error
}

func (e *env) err() error { return errors.Join(e.errs...) }

func (e *env) fail(key, value string, err error) {
	e.errs = append(e.errs, fmt.Errorf("%s=%q: %w", key, value, err))
}

func (e *env) str(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (e *env) int(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		e.fail(key, v, err)
		return fallback
	}
	return i
}

func (e *env) bool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.fail(key, v, err)
		return fallback
	}
	return b
}

func (e *env) duration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.fail(key, v, err)
		return fallback
	}
	return d
}
