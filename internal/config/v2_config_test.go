package config

import (
	"os"
	"testing"
)

func setRequiredV2Env() {
	setRequiredEmbeddingEnv()
	os.Setenv("AI_REVIEWER_MODEL", "reviewer-model")
	os.Setenv("AI_VERIFIER_MODEL", "verifier-model")
	os.Setenv("SEARCH_DOCUMENT_FORMAT_VERSION", "search-doc-v1")
	os.Setenv("EMBEDDING_NORMALIZATION_VERSION", "embedding-norm-v1")
	os.Setenv("PREDICATE_REGISTRY_VERSION", "predicate-registry-v1")
}

func TestValidateV2DormantStartup(t *testing.T) {
	t.Run("off does not require v2 provider contract", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned unexpected error: %v", err)
		}

		if err := cfg.ValidateV2DormantStartup(); err != nil {
			t.Fatalf("ValidateV2DormantStartup() returned unexpected error: %v", err)
		}
	})

	t.Run("dormant requires reviewer and versioned profiles", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()
		setRequiredEmbeddingEnv()
		os.Setenv("V2_BOOT_MODE", V2BootModeDormant)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned unexpected error: %v", err)
		}

		err = cfg.ValidateV2DormantStartup()
		if err == nil {
			t.Fatal("ValidateV2DormantStartup() expected error, got nil")
		}
		validationErr, ok := err.(*ValidationError)
		if !ok {
			t.Fatalf("expected *ValidationError, got %T", err)
		}
		if validationErr.Field != "AI_REVIEWER_MODEL" {
			t.Fatalf("field = %q, want AI_REVIEWER_MODEL", validationErr.Field)
		}
	})

	t.Run("dormant succeeds with v2 contract settings", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()
		setRequiredV2Env()
		os.Setenv("V2_BOOT_MODE", V2BootModeDormant)
		os.Setenv("V2_LEGACY_MIGRATION_REQUIRED", "true")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned unexpected error: %v", err)
		}

		if !cfg.IsV2BootEnabled() {
			t.Fatal("IsV2BootEnabled() = false, want true")
		}
		if !cfg.GetV2LegacyMigrationRequired() {
			t.Fatal("GetV2LegacyMigrationRequired() = false, want true")
		}
		if err := cfg.ValidateV2DormantStartup(); err != nil {
			t.Fatalf("ValidateV2DormantStartup() returned unexpected error: %v", err)
		}
	})
}

func TestLoadV2ConfigRejectsUnsafeModesAndTopologyHints(t *testing.T) {
	cases := []struct {
		name  string
		set   func()
		field string
	}{
		{"active mode unsupported before cutover issue", func() { os.Setenv("V2_BOOT_MODE", "active") }, "V2_BOOT_MODE"},
		{"postgres read dsn rejected", func() { os.Setenv("POSTGRES_READ_DSN", "postgres://replica/db") }, "POSTGRES_READ_DSN"},
		{"distributed coordination requires redis", func() { os.Setenv("DISTRIBUTED_COORDINATION_REQUIRED", "true") }, "DISTRIBUTED_COORDINATION_REQUIRED"},
		{"unsupported ann strategy", func() { os.Setenv("PGVECTOR_ANN_STRATEGY", "ivfflat") }, "PGVECTOR_ANN_STRATEGY"},
		{"embedding batch too large", func() { os.Setenv("EMBEDDING_BATCH_SIZE", "257") }, "EMBEDDING_BATCH_SIZE"},
		{"placement heartbeat must be lower than lease", func() {
			os.Setenv("MEMORY_PLACEMENT_LEASE_SECONDS", "30")
			os.Setenv("MEMORY_PLACEMENT_HEARTBEAT_SECONDS", "30")
		}, "MEMORY_PLACEMENT_HEARTBEAT_SECONDS"},
		{"recall branch floor must not exceed max", func() {
			os.Setenv("RECALL_BRANCH_LIMIT_FLOOR", "201")
			os.Setenv("RECALL_BRANCH_LIMIT_MAX", "200")
		}, "RECALL_BRANCH_LIMIT_FLOOR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv()
			setRequiredEnv()
			tc.set()

			_, err := Load()

			if err == nil {
				t.Fatal("Load() expected error, got nil")
			}
			validationErr, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if validationErr.Field != tc.field {
				t.Fatalf("field = %q, want %q; err=%v", validationErr.Field, tc.field, err)
			}
		})
	}
}

func TestLoadV2ConfigAcceptsRedisRequiredWhenConfigured(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("DISTRIBUTED_COORDINATION_REQUIRED", "true")
	os.Setenv("REDIS_ADDR", "redis:6379")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if !cfg.GetDistributedCoordinationRequired() {
		t.Fatal("GetDistributedCoordinationRequired() = false, want true")
	}
	if cfg.GetRedisAddr() != "redis:6379" {
		t.Fatalf("GetRedisAddr() = %q", cfg.GetRedisAddr())
	}
}
