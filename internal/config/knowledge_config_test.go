package config

import (
	"os"
	"testing"
)

// TestLoadKnowledgeConfigDefaults verifies that all knowledge-pipeline knobs
// have their expected default values when no environment variables are set (AC-X3).
func TestLoadKnowledgeConfigDefaults(t *testing.T) {
	clearEnv()
	setRequiredEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if got := cfg.GetAIVerifierModel(); got != "gpt-4o-mini" {
		t.Errorf("GetAIVerifierModel() = %q, want %q", got, "gpt-4o-mini")
	}
	if cfg.GetAIVerifierDisableTemperature() {
		t.Error("GetAIVerifierDisableTemperature() = true, want false")
	}
	if got := cfg.GetAIVerifierMaxConcurrency(); got != 5 {
		t.Errorf("GetAIVerifierMaxConcurrency() = %d, want %d", got, 5)
	}
	if got := cfg.GetClaimWriteRateLimit(); got != 60 {
		t.Errorf("GetClaimWriteRateLimit() = %d, want %d", got, 60)
	}
	if got := cfg.GetClaimReadRateLimit(); got != 300 {
		t.Errorf("GetClaimReadRateLimit() = %d, want %d", got, 300)
	}
	if got := cfg.GetRecallValidatedClaimWeight(); got != 0.5 {
		t.Errorf("GetRecallValidatedClaimWeight() = %f, want %f", got, 0.5)
	}
	if got := cfg.GetPromoteTxTimeoutSeconds(); got != 10 {
		t.Errorf("GetPromoteTxTimeoutSeconds() = %d, want %d", got, 10)
	}
	if got := cfg.GetSkillPackImportHistoryDays(); got != 30 {
		t.Errorf("GetSkillPackImportHistoryDays() = %d, want %d", got, 30)
	}
	if got := cfg.GetMemoryPackImportHistoryDays(); got != 30 {
		t.Errorf("GetMemoryPackImportHistoryDays() = %d, want %d", got, 30)
	}
	if got := cfg.GetAICommunityMaxNodes(); got != 500000 {
		t.Errorf("GetAICommunityMaxNodes() = %d, want %d", got, 500000)
	}
}

func TestLoadMemoryPackImportHistoryEnv(t *testing.T) {
	t.Run("new env wins", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()
		os.Setenv("MEMORY_PACK_IMPORT_HISTORY_DAYS", "14")
		os.Setenv("SKILL_PACK_IMPORT_HISTORY_DAYS", "60")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned unexpected error: %v", err)
		}
		if got := cfg.GetMemoryPackImportHistoryDays(); got != 14 {
			t.Fatalf("GetMemoryPackImportHistoryDays() = %d, want 14", got)
		}
	})

	t.Run("legacy env fallback", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()
		os.Setenv("SKILL_PACK_IMPORT_HISTORY_DAYS", "21")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned unexpected error: %v", err)
		}
		if got := cfg.GetMemoryPackImportHistoryDays(); got != 21 {
			t.Fatalf("GetMemoryPackImportHistoryDays() = %d, want 21", got)
		}
	})
}

func TestLoadControlPortalValidation(t *testing.T) {
	t.Run("server startup requires token", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()
		setRequiredEmbeddingEnv()
		os.Unsetenv("CONTROL_PORTAL_TOKEN")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned unexpected error: %v", err)
		}

		err = cfg.ValidateServerStartup()
		if err == nil {
			t.Fatal("ValidateServerStartup() expected error for missing control token, got nil")
		}
		validationErr, ok := err.(*ValidationError)
		if !ok {
			t.Fatalf("expected *ValidationError, got %T", err)
		}
		if validationErr.Field != "CONTROL_PORTAL_TOKEN" {
			t.Errorf("ValidationError.Field = %q, want CONTROL_PORTAL_TOKEN", validationErr.Field)
		}
	})

	t.Run("allows explicit network bind", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()
		os.Setenv("CONTROL_HTTP_ADDR", "0.0.0.0:8090")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned unexpected error: %v", err)
		}
		if cfg.ControlHTTPAddr != "0.0.0.0:8090" {
			t.Errorf("ControlHTTPAddr = %q, want 0.0.0.0:8090", cfg.ControlHTTPAddr)
		}
	})
}

func TestLoadValidation_RemainingInvalidEnvironmentBranches(t *testing.T) {
	cases := []struct {
		name  string
		set   func()
		field string
	}{
		{"invalid redis db", func() { os.Setenv("REDIS_DB", "bad") }, "REDIS_DB"},
		{"invalid http max body bytes", func() { os.Setenv("HTTP_MAX_BODY_BYTES", "bad") }, "HTTP_MAX_BODY_BYTES"},
		{"invalid auth verify concurrency", func() { os.Setenv("AUTH_VERIFY_MAX_CONCURRENCY", "bad") }, "AUTH_VERIFY_MAX_CONCURRENCY"},
		{"invalid graph default timeout", func() { os.Setenv("GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS", "bad") }, "GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS"},
		{"invalid graph max timeout", func() { os.Setenv("GRAPH_QUERY_MAX_TIMEOUT_SECONDS", "bad") }, "GRAPH_QUERY_MAX_TIMEOUT_SECONDS"},
		{"invalid fragment create rate", func() { os.Setenv("FRAGMENT_CREATE_RATE_LIMIT", "bad") }, "FRAGMENT_CREATE_RATE_LIMIT"},
		{"invalid fragment read rate", func() { os.Setenv("FRAGMENT_READ_RATE_LIMIT", "bad") }, "FRAGMENT_READ_RATE_LIMIT"},
		{"invalid sse heartbeat", func() { os.Setenv("SSE_HEARTBEAT_SECONDS", "bad") }, "SSE_HEARTBEAT_SECONDS"},
		{"invalid sse max duration", func() { os.Setenv("SSE_MAX_DURATION_SECONDS", "bad") }, "SSE_MAX_DURATION_SECONDS"},
		{"invalid sse streams", func() { os.Setenv("SSE_MAX_CONCURRENT_STREAMS", "bad") }, "SSE_MAX_CONCURRENT_STREAMS"},
		{"invalid embedding dimensions", func() { os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "bad") }, "AI_API_EMBEDDING_DIMENSIONS"},
		{"invalid embedding timeout", func() { os.Setenv("AI_API_EMBEDDING_TIMEOUT_SECONDS", "bad") }, "AI_API_EMBEDDING_TIMEOUT_SECONDS"},
		{"invalid verifier disable temperature", func() { os.Setenv("AI_VERIFIER_DISABLE_TEMPERATURE", "bad") }, "AI_VERIFIER_DISABLE_TEMPERATURE"},
		{"invalid verifier timeout", func() { os.Setenv("AI_VERIFIER_TIMEOUT_SECONDS", "bad") }, "AI_VERIFIER_TIMEOUT_SECONDS"},
		{"invalid verifier concurrency", func() { os.Setenv("AI_VERIFIER_MAX_CONCURRENCY", "bad") }, "AI_VERIFIER_MAX_CONCURRENCY"},
		{"invalid claim write rate", func() { os.Setenv("CLAIM_WRITE_RATE_LIMIT", "bad") }, "CLAIM_WRITE_RATE_LIMIT"},
		{"invalid claim read rate", func() { os.Setenv("CLAIM_READ_RATE_LIMIT", "bad") }, "CLAIM_READ_RATE_LIMIT"},
		{"invalid recall weight", func() { os.Setenv("RECALL_VALIDATED_CLAIM_WEIGHT", "bad") }, "RECALL_VALIDATED_CLAIM_WEIGHT"},
		{"invalid promote timeout", func() { os.Setenv("PROMOTE_TX_TIMEOUT_SECONDS", "bad") }, "PROMOTE_TX_TIMEOUT_SECONDS"},
		{"invalid community max nodes", func() { os.Setenv("AI_COMMUNITY_MAX_NODES", "bad") }, "AI_COMMUNITY_MAX_NODES"},
		{"zero http max body bytes", func() { os.Setenv("HTTP_MAX_BODY_BYTES", "0") }, "HTTP_MAX_BODY_BYTES"},
		{"recall weight below range", func() { os.Setenv("RECALL_VALIDATED_CLAIM_WEIGHT", "-0.1") }, "RECALL_VALIDATED_CLAIM_WEIGHT"},
		{"recall weight above range", func() { os.Setenv("RECALL_VALIDATED_CLAIM_WEIGHT", "1.1") }, "RECALL_VALIDATED_CLAIM_WEIGHT"},
		{"verifier key without url or shared api url", func() { os.Setenv("AI_VERIFIER_API_KEY", "verifier-key") }, "AI_VERIFIER_API_URL"},
		{"embedding missing url", func() {
			os.Setenv("AI_API_KEY", "sk-test")
			os.Setenv("AI_API_EMBEDDING_MODEL", "text-embedding-3-small")
			os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "1536")
		}, "AI_API_URL"},
		{"embedding missing model", func() {
			os.Setenv("AI_API_URL", "https://example.com/v1")
			os.Setenv("AI_API_KEY", "sk-test")
			os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "1536")
		}, "AI_API_EMBEDDING_MODEL"},
		{"embedding missing dimensions", func() {
			os.Setenv("AI_API_URL", "https://example.com/v1")
			os.Setenv("AI_API_KEY", "sk-test")
			os.Setenv("AI_API_EMBEDDING_MODEL", "text-embedding-3-small")
		}, "AI_API_EMBEDDING_DIMENSIONS"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv()
			setRequiredEnv()
			tc.set()

			_, err := Load()

			if err == nil {
				t.Fatalf("Load() expected error for %s, got nil", tc.field)
			}
			validationErr, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if validationErr.Field != tc.field {
				t.Fatalf("ValidationError.Field = %q, want %q; err=%v", validationErr.Field, tc.field, err)
			}
		})
	}
}

func TestValidateServerStartupRemainingRequiredFields(t *testing.T) {
	cfg := Config{
		AIAPIURL:              "https://example.com/v1",
		AIAPIKey:              "sk-test",
		AIEmbeddingModel:      "text-embedding-3-small",
		AIEmbeddingDimensions: 1536,
		ControlPortalToken:    "control-secret",
	}
	cases := []struct {
		name  string
		edit  func(*Config)
		field string
	}{
		{"missing api key", func(c *Config) { c.AIAPIKey = "" }, "AI_API_KEY"},
		{"missing embedding model", func(c *Config) { c.AIEmbeddingModel = "" }, "AI_API_EMBEDDING_MODEL"},
		{"missing embedding dimensions", func(c *Config) { c.AIEmbeddingDimensions = 0 }, "AI_API_EMBEDDING_DIMENSIONS"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testCfg := cfg
			tc.edit(&testCfg)

			err := testCfg.ValidateServerStartup()

			if err == nil {
				t.Fatal("ValidateServerStartup() expected error, got nil")
			}
			validationErr, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if validationErr.Field != tc.field {
				t.Fatalf("field = %q, want %q", validationErr.Field, tc.field)
			}
		})
	}
}
