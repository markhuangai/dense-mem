package config

import (
	"os"
	"testing"
)

func TestLoadValidation_RemainingInvalidEnvironmentBranches(t *testing.T) {
	cases := []struct {
		name  string
		set   func()
		field string
	}{
		{"invalid redis db", func() { os.Setenv("REDIS_DB", "bad") }, "REDIS_DB"},
		{"invalid postgres migration timeout", func() { os.Setenv("POSTGRES_MIGRATION_TIMEOUT_SECONDS", "bad") }, "POSTGRES_MIGRATION_TIMEOUT_SECONDS"},
		{"zero postgres migration timeout", func() { os.Setenv("POSTGRES_MIGRATION_TIMEOUT_SECONDS", "0") }, "POSTGRES_MIGRATION_TIMEOUT_SECONDS"},
		{"invalid http max body bytes", func() { os.Setenv("HTTP_MAX_BODY_BYTES", "bad") }, "HTTP_MAX_BODY_BYTES"},
		{"invalid auth verify concurrency", func() { os.Setenv("AUTH_VERIFY_MAX_CONCURRENCY", "bad") }, "AUTH_VERIFY_MAX_CONCURRENCY"},
		{"invalid sse heartbeat", func() { os.Setenv("SSE_HEARTBEAT_SECONDS", "bad") }, "SSE_HEARTBEAT_SECONDS"},
		{"invalid sse max duration", func() { os.Setenv("SSE_MAX_DURATION_SECONDS", "bad") }, "SSE_MAX_DURATION_SECONDS"},
		{"invalid sse streams", func() { os.Setenv("SSE_MAX_CONCURRENT_STREAMS", "bad") }, "SSE_MAX_CONCURRENT_STREAMS"},
		{"invalid embedding dimensions", func() { os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "bad") }, "AI_API_EMBEDDING_DIMENSIONS"},
		{"invalid embedding timeout", func() { os.Setenv("AI_API_EMBEDDING_TIMEOUT_SECONDS", "bad") }, "AI_API_EMBEDDING_TIMEOUT_SECONDS"},
		{"invalid embedding concurrency", func() { os.Setenv("AI_API_EMBEDDING_MAX_CONCURRENCY", "bad") }, "AI_API_EMBEDDING_MAX_CONCURRENCY"},
		{"zero embedding concurrency", func() { os.Setenv("AI_API_EMBEDDING_MAX_CONCURRENCY", "0") }, "AI_API_EMBEDDING_MAX_CONCURRENCY"},
		{"invalid embedding worker count", func() { os.Setenv("EMBEDDING_WORKER_COUNT", "bad") }, "EMBEDDING_WORKER_COUNT"},
		{"embedding workers exceed provider", func() {
			os.Setenv("AI_API_EMBEDDING_MAX_CONCURRENCY", "1")
			os.Setenv("EMBEDDING_WORKER_COUNT", "2")
		}, "EMBEDDING_WORKER_COUNT"},
		{"invalid embedding batch size", func() { os.Setenv("EMBEDDING_BATCH_SIZE", "bad") }, "EMBEDDING_BATCH_SIZE"},
		{"excessive embedding batch size", func() { os.Setenv("EMBEDDING_BATCH_SIZE", "257") }, "EMBEDDING_BATCH_SIZE"},
		{"invalid embedding poll", func() { os.Setenv("EMBEDDING_JOB_POLL_SECONDS", "bad") }, "EMBEDDING_JOB_POLL_SECONDS"},
		{"invalid embedding job attempts", func() { os.Setenv("EMBEDDING_JOB_MAX_ATTEMPTS", "bad") }, "EMBEDDING_JOB_MAX_ATTEMPTS"},
		{"zero embedding job attempts", func() { os.Setenv("EMBEDDING_JOB_MAX_ATTEMPTS", "0") }, "EMBEDDING_JOB_MAX_ATTEMPTS"},
		{"excessive embedding job attempts", func() { os.Setenv("EMBEDDING_JOB_MAX_ATTEMPTS", "101") }, "EMBEDDING_JOB_MAX_ATTEMPTS"},
		{"invalid verifier disable temperature", func() { os.Setenv("AI_VERIFIER_DISABLE_TEMPERATURE", "bad") }, "AI_VERIFIER_DISABLE_TEMPERATURE"},
		{"invalid verifier timeout", func() { os.Setenv("AI_VERIFIER_TIMEOUT_SECONDS", "bad") }, "AI_VERIFIER_TIMEOUT_SECONDS"},
		{"invalid verifier concurrency", func() { os.Setenv("AI_VERIFIER_MAX_CONCURRENCY", "bad") }, "AI_VERIFIER_MAX_CONCURRENCY"},
		{"invalid verifier input token budget", func() { os.Setenv("AI_VERIFIER_MAX_INPUT_TOKENS", "bad") }, "AI_VERIFIER_MAX_INPUT_TOKENS"},
		{"invalid verifier output token budget", func() { os.Setenv("AI_VERIFIER_MAX_OUTPUT_TOKENS", "bad") }, "AI_VERIFIER_MAX_OUTPUT_TOKENS"},
		{"invalid verifier candidate token budget", func() { os.Setenv("AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS", "bad") }, "AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS"},
		{"candidate token budget exceeds input", func() {
			os.Setenv("AI_VERIFIER_MAX_INPUT_TOKENS", "10")
			os.Setenv("AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS", "11")
		}, "AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS"},
		{"unsupported verifier tokenizer", func() { os.Setenv("AI_VERIFIER_TOKENIZER", "unknown") }, "AI_VERIFIER_TOKENIZER"},
		{"invalid placement worker count", func() { os.Setenv("MEMORY_PLACEMENT_WORKER_COUNT", "bad") }, "MEMORY_PLACEMENT_WORKER_COUNT"},
		{"placement workers exceed verifier", func() {
			os.Setenv("AI_VERIFIER_MAX_CONCURRENCY", "1")
			os.Setenv("MEMORY_PLACEMENT_WORKER_COUNT", "2")
		}, "MEMORY_PLACEMENT_WORKER_COUNT"},
		{"invalid placement poll", func() { os.Setenv("MEMORY_PLACEMENT_POLL_SECONDS", "bad") }, "MEMORY_PLACEMENT_POLL_SECONDS"},
		{"invalid promote timeout", func() { os.Setenv("PROMOTE_TX_TIMEOUT_SECONDS", "bad") }, "PROMOTE_TX_TIMEOUT_SECONDS"},
		{"invalid conflict ttl", func() { os.Setenv("CONFLICT_REVIEW_TTL_DAYS", "bad") }, "CONFLICT_REVIEW_TTL_DAYS"},
		{"zero conflict ttl", func() { os.Setenv("CONFLICT_REVIEW_TTL_DAYS", "0") }, "CONFLICT_REVIEW_TTL_DAYS"},
		{"excessive conflict ttl", func() { os.Setenv("CONFLICT_REVIEW_TTL_DAYS", "31") }, "CONFLICT_REVIEW_TTL_DAYS"},
		{"invalid conflict start time", func() { os.Setenv("CONFLICT_REVIEW_START_TIME_LOCAL", "4am") }, "CONFLICT_REVIEW_START_TIME_LOCAL"},
		{"invalid app timezone", func() { os.Setenv("APP_TIMEZONE", "Mars/Base") }, "APP_TIMEZONE"},
		{"excessive conflict concurrency", func() { os.Setenv("CONFLICT_REVIEW_MAX_CONCURRENCY", "17") }, "CONFLICT_REVIEW_MAX_CONCURRENCY"},
		{"excessive conflict batch size", func() { os.Setenv("CONFLICT_REVIEW_BATCH_SIZE", "501") }, "CONFLICT_REVIEW_BATCH_SIZE"},
		{"short conflict lease", func() { os.Setenv("CONFLICT_REVIEW_LEASE_SECONDS", "29") }, "CONFLICT_REVIEW_LEASE_SECONDS"},
		{"long conflict lease", func() { os.Setenv("CONFLICT_REVIEW_LEASE_SECONDS", "1801") }, "CONFLICT_REVIEW_LEASE_SECONDS"},
		{"excessive conflict attempts", func() { os.Setenv("CONFLICT_REVIEW_MAX_ATTEMPTS", "21") }, "CONFLICT_REVIEW_MAX_ATTEMPTS"},
		{"negative conflict jitter", func() { os.Setenv("CONFLICT_REVIEW_JITTER_SECONDS", "-1") }, "CONFLICT_REVIEW_JITTER_SECONDS"},
		{"excessive conflict jitter", func() { os.Setenv("CONFLICT_REVIEW_JITTER_SECONDS", "3601") }, "CONFLICT_REVIEW_JITTER_SECONDS"},
		{"zero http max body bytes", func() { os.Setenv("HTTP_MAX_BODY_BYTES", "0") }, "HTTP_MAX_BODY_BYTES"},
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
		AIVerifierModel:       "verifier-model",
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
		{"missing verifier model", func(c *Config) { c.AIVerifierModel = "" }, "AI_VERIFIER_MODEL"},
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

func TestConfigProviderDatabaseAndCoordinationGetters(t *testing.T) {
	cfg := Config{
		PostgresMaxOpenConns:            25,
		PostgresMaxIdleConns:            7,
		PostgresConnMaxLifetimeSeconds:  90,
		PostgresMigrationTimeoutSeconds: 1800,
		RedisTLSEnabled:                 true,
		DistributedCoordinationRequired: true,
	}

	if got := cfg.GetPostgresMaxOpenConns(); got != 25 {
		t.Fatalf("GetPostgresMaxOpenConns() = %d, want 25", got)
	}
	if got := cfg.GetPostgresMaxIdleConns(); got != 7 {
		t.Fatalf("GetPostgresMaxIdleConns() = %d, want 7", got)
	}
	if got := cfg.GetPostgresConnMaxLifetimeSeconds(); got != 90 {
		t.Fatalf("GetPostgresConnMaxLifetimeSeconds() = %d, want 90", got)
	}
	if got := cfg.GetPostgresMigrationTimeoutSeconds(); got != 1800 {
		t.Fatalf("GetPostgresMigrationTimeoutSeconds() = %d, want 1800", got)
	}
	if !cfg.GetRedisTLSEnabled() {
		t.Fatal("GetRedisTLSEnabled() = false, want true")
	}
	if !cfg.GetDistributedCoordinationRequired() {
		t.Fatal("GetDistributedCoordinationRequired() = false, want true")
	}
}
