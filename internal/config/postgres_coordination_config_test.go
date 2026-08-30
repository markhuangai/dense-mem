package config

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestLoadPostgresAndCoordinationDefaults(t *testing.T) {
	clearEnv()
	setRequiredEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.PostgresMaxOpenConns != 25 {
		t.Errorf("PostgresMaxOpenConns default = %d, want %d", cfg.PostgresMaxOpenConns, 25)
	}
	if cfg.PostgresMaxIdleConns != 10 {
		t.Errorf("PostgresMaxIdleConns default = %d, want %d", cfg.PostgresMaxIdleConns, 10)
	}
	if cfg.PostgresConnMaxLifetimeSeconds != 1800 {
		t.Errorf("PostgresConnMaxLifetimeSeconds default = %d, want %d", cfg.PostgresConnMaxLifetimeSeconds, 1800)
	}
	if cfg.PostgresMigrationTimeoutSeconds != DefaultPostgresMigrationTimeoutSeconds {
		t.Errorf("PostgresMigrationTimeoutSeconds default = %d, want %d", cfg.PostgresMigrationTimeoutSeconds, DefaultPostgresMigrationTimeoutSeconds)
	}
	if cfg.RedisTLSEnabled {
		t.Error("RedisTLSEnabled default = true, want false")
	}
	if cfg.DistributedCoordinationRequired {
		t.Error("DistributedCoordinationRequired default = true, want false")
	}
}

func TestLoadPostgresAndCoordinationOverrides(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("POSTGRES_MAX_OPEN_CONNS", "50")
	os.Setenv("POSTGRES_MAX_IDLE_CONNS", "25")
	os.Setenv("POSTGRES_CONN_MAX_LIFETIME_SECONDS", "900")
	os.Setenv("POSTGRES_MIGRATION_TIMEOUT_SECONDS", "900")
	os.Setenv("REDIS_ADDR", "localhost:6379")
	os.Setenv("REDIS_TLS_ENABLED", "true")
	os.Setenv("DISTRIBUTED_COORDINATION_REQUIRED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.PostgresMaxOpenConns != 50 {
		t.Errorf("PostgresMaxOpenConns = %d, want %d", cfg.PostgresMaxOpenConns, 50)
	}
	if cfg.PostgresMaxIdleConns != 25 {
		t.Errorf("PostgresMaxIdleConns = %d, want %d", cfg.PostgresMaxIdleConns, 25)
	}
	if cfg.PostgresConnMaxLifetimeSeconds != 900 {
		t.Errorf("PostgresConnMaxLifetimeSeconds = %d, want %d", cfg.PostgresConnMaxLifetimeSeconds, 900)
	}
	if cfg.PostgresMigrationTimeoutSeconds != 900 {
		t.Errorf("PostgresMigrationTimeoutSeconds = %d, want 900", cfg.PostgresMigrationTimeoutSeconds)
	}
	if !cfg.RedisTLSEnabled {
		t.Error("RedisTLSEnabled = false, want true")
	}
	if !cfg.DistributedCoordinationRequired {
		t.Error("DistributedCoordinationRequired = false, want true")
	}
}

func TestLoadValidation_PostgresReadDSNRejected(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("POSTGRES_READ_DSN", "postgres://readonly@example.com/db?sslmode=disable")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for POSTGRES_READ_DSN, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "POSTGRES_READ_DSN" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "POSTGRES_READ_DSN")
	}
}

func TestLoadValidation_PostgresIdleExceedsOpen(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("POSTGRES_MAX_OPEN_CONNS", "4")
	os.Setenv("POSTGRES_MAX_IDLE_CONNS", "5")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for postgres pool mismatch, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "POSTGRES_MAX_IDLE_CONNS" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "POSTGRES_MAX_IDLE_CONNS")
	}
}

func TestLoadValidation_PostgresOpenRequiresDreamConfirmationCapacity(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("POSTGRES_MAX_OPEN_CONNS", "1")
	os.Setenv("POSTGRES_MAX_IDLE_CONNS", "1")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for a one-connection postgres pool, got nil")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "POSTGRES_MAX_OPEN_CONNS" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "POSTGRES_MAX_OPEN_CONNS")
	}
}

func TestLoadValidation_PostgresMigrationTimeoutExceedsMaximum(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("POSTGRES_MIGRATION_TIMEOUT_SECONDS", "86401")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for excessive postgres migration timeout, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "POSTGRES_MIGRATION_TIMEOUT_SECONDS" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "POSTGRES_MIGRATION_TIMEOUT_SECONDS")
	}
}

func TestLoadValidation_DistributedCoordinationRequiresRedis(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("DISTRIBUTED_COORDINATION_REQUIRED", "true")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for missing REDIS_ADDR, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "DISTRIBUTED_COORDINATION_REQUIRED" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "DISTRIBUTED_COORDINATION_REQUIRED")
	}
}

func TestConfigJSONExcludesCredentials(t *testing.T) {
	configType := reflect.TypeOf(Config{})
	for _, fieldName := range []string{
		"PostgresDSN",
		"RedisPassword",
		"AIAPIKey",
		"AIVerifierAPIKey",
		"ControlPortalToken",
		"TelemetryScrapeToken",
	} {
		field, ok := configType.FieldByName(fieldName)
		if !ok {
			t.Fatalf("Config missing credential field %q", fieldName)
		}
		if got := field.Tag.Get("json"); got != "-" {
			t.Fatalf("Config.%s json tag = %q, want %q", fieldName, got, "-")
		}
	}

	cfg := Config{
		PostgresDSN:          "postgres://user:postgres-secret@example.com/db",
		RedisPassword:        "redis-secret",
		AIAPIKey:             "ai-api-secret",
		AIVerifierAPIKey:     "verifier-secret",
		ControlPortalToken:   "control-secret",
		TelemetryScrapeToken: "telemetry-secret",
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal(Config): %v", err)
	}
	encoded := string(data)
	for _, secret := range []string{
		"postgres-secret",
		"redis-secret",
		"ai-api-secret",
		"verifier-secret",
		"control-secret",
		"telemetry-secret",
	} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("serialized Config contains credential %q: %s", secret, encoded)
		}
	}
}
