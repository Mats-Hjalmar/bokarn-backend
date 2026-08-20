package config

import (
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Port != 1437 {
		t.Errorf("Server.Port = %d, want 1437", cfg.Server.Port)
	}
	if cfg.Database.Port != 1438 {
		t.Errorf("Database.Port = %d, want 1438", cfg.Database.Port)
	}
	if cfg.Redis.Port != 1439 {
		t.Errorf("Redis.Port = %d, want 1439", cfg.Redis.Port)
	}
	if cfg.OTel.ServiceName != "bokarn-backend" {
		t.Errorf(
			"OTel.ServiceName = %q, want bokarn-backend",
			cfg.OTel.ServiceName,
		)
	}
}

// The three roles are the whole isolation model, so a DSN that quietly used
// the wrong one would disable row-level security without any other symptom.
func TestDSNsUseDistinctRoles(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		name string
		dsn  string
		user string
	}{
		{"app", cfg.Database.AppDSN(), "user=bokarn_app"},
		{"migrator", cfg.Database.MigratorDSN(), "user=bokarn_migrator"},
		{"platform", cfg.Database.PlatformDSN(), "user=bokarn_platform"},
	}

	for _, c := range cases {
		if !strings.Contains(c.dsn, c.user) {
			t.Errorf(
				"%s DSN = %q, want it to contain %q",
				c.name,
				c.dsn,
				c.user,
			)
		}
		if !strings.Contains(c.dsn, "dbname=bokarn") {
			t.Errorf("%s DSN = %q, want dbname=bokarn", c.name, c.dsn)
		}
	}
}

// BOKARN_DATABASE_URL overrides only the application DSN. The migrator must
// keep its own credentials, or pointing the API at a managed database would
// silently redirect migrations too.
func TestDatabaseURLOverridesOnlyAppDSN(t *testing.T) {
	clearEnv(t)
	t.Setenv("BOKARN_DATABASE_URL", "host=managed.example port=5432 user=x")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Database.AppDSN() != "host=managed.example port=5432 user=x" {
		t.Errorf("AppDSN = %q, want the URL verbatim", cfg.Database.AppDSN())
	}
	if !strings.Contains(cfg.Database.MigratorDSN(), "user=bokarn_migrator") {
		t.Errorf(
			"MigratorDSN = %q, want the migrator role",
			cfg.Database.MigratorDSN(),
		)
	}
}

func TestLoadReportsEveryMalformedVariable(t *testing.T) {
	clearEnv(t)
	t.Setenv("BOKARN_PORT", "not-a-port")
	t.Setenv("BOKARN_DB_PORT", "also-not")

	_, err := Load()
	if err == nil {
		t.Fatal("Load succeeded, want an error for both bad variables")
	}
	for _, want := range []string{"BOKARN_PORT", "BOKARN_DB_PORT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BOKARN_PORT", "BOKARN_DB_HOST", "BOKARN_DB_PORT", "BOKARN_DB_NAME",
		"BOKARN_DATABASE_URL", "BOKARN_DB_APP_USER", "BOKARN_DB_MIGRATOR_USER",
		"BOKARN_DB_PLATFORM_USER", "BOKARN_REDIS_PORT", "OTEL_SERVICE_NAME",
	} {
		t.Setenv(k, "")
	}
}
