package config

import (
	"os"
	"testing"
)

func TestLoadV2MigrationControlConfig(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("V2_BOOT_MODE", V2BootModeDormant)
	os.Setenv("V2_LEGACY_MIGRATION_REQUIRED", "true")
	os.Setenv("V2_MIGRATION_CREDENTIAL_ID", "11111111-1111-4111-8111-111111111111")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.GetV2BootMode() != V2BootModeDormant {
		t.Fatalf("GetV2BootMode() = %q, want %q", cfg.GetV2BootMode(), V2BootModeDormant)
	}
	if !cfg.IsV2BootEnabled() {
		t.Fatal("IsV2BootEnabled() = false, want true")
	}
	if !cfg.GetV2LegacyMigrationRequired() {
		t.Fatal("GetV2LegacyMigrationRequired() = false, want true")
	}
	if got := cfg.GetV2MigrationCredentialID(); got != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("GetV2MigrationCredentialID() = %q", got)
	}
}

func TestLoadV2UATDoesNotRequireNeo4jConfig(t *testing.T) {
	clearEnv()
	os.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	os.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")
	os.Setenv("V2_BOOT_MODE", V2BootModeUAT)
	setRequiredEmbeddingEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.GetNeo4jURI() != "" || cfg.GetNeo4jUser() != "" || cfg.GetNeo4jPassword() != "" {
		t.Fatalf("Neo4j config should stay empty in UAT: %#v", cfg)
	}
	if err := cfg.ValidateServerStartup(); err != nil {
		t.Fatalf("ValidateServerStartup() returned unexpected error: %v", err)
	}
}

func TestLoadV2BootModeRejectsUnsupportedMode(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("V2_BOOT_MODE", "active")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "V2_BOOT_MODE" {
		t.Fatalf("field = %q, want V2_BOOT_MODE", validationErr.Field)
	}
}

func TestLoadV2MigrationRequiredNeedsEnabledBootMode(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("V2_LEGACY_MIGRATION_REQUIRED", "true")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "V2_LEGACY_MIGRATION_REQUIRED" {
		t.Fatalf("field = %q, want V2_LEGACY_MIGRATION_REQUIRED", validationErr.Field)
	}
}

func TestLoadV2MigrationRequiredDormantNeedsCredential(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("V2_BOOT_MODE", V2BootModeDormant)
	os.Setenv("V2_LEGACY_MIGRATION_REQUIRED", "true")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "V2_MIGRATION_CREDENTIAL_ID" {
		t.Fatalf("field = %q, want V2_MIGRATION_CREDENTIAL_ID", validationErr.Field)
	}
}

func TestLoadV2MigrationCredentialRejectsInvalidUUID(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("V2_BOOT_MODE", V2BootModeDormant)
	os.Setenv("V2_MIGRATION_CREDENTIAL_ID", "not-a-uuid")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "V2_MIGRATION_CREDENTIAL_ID" {
		t.Fatalf("field = %q, want V2_MIGRATION_CREDENTIAL_ID", validationErr.Field)
	}
}
