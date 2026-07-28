package config

import (
	"os"
	"testing"
)

type configProviderWithoutEmbeddingConcurrency struct {
	ConfigProvider
}

type embeddingConcurrencyOverride struct {
	ConfigProvider
	concurrency int
}

func (c embeddingConcurrencyOverride) GetAIEmbeddingMaxConcurrency() int {
	return c.concurrency
}

func TestWorkerConcurrencyDefaults(t *testing.T) {
	cfg := &Config{}

	if got := cfg.GetAIEmbeddingMaxConcurrency(); got != DefaultAIEmbeddingMaxConcurrency {
		t.Fatalf("GetAIEmbeddingMaxConcurrency() = %d, want %d", got, DefaultAIEmbeddingMaxConcurrency)
	}
	if got := cfg.GetEmbeddingWorkerCount(); got != DefaultEmbeddingWorkerCount {
		t.Fatalf("GetEmbeddingWorkerCount() = %d, want %d", got, DefaultEmbeddingWorkerCount)
	}
	if got := cfg.GetEmbeddingBatchSize(); got != DefaultEmbeddingBatchSize {
		t.Fatalf("GetEmbeddingBatchSize() = %d, want %d", got, DefaultEmbeddingBatchSize)
	}
	if got := cfg.GetEmbeddingJobPollSeconds(); got != DefaultEmbeddingJobPollSeconds {
		t.Fatalf("GetEmbeddingJobPollSeconds() = %d, want %d", got, DefaultEmbeddingJobPollSeconds)
	}
	if got := cfg.GetMemoryPlacementWorkerCount(); got != DefaultMemoryPlacementWorkerCount {
		t.Fatalf("GetMemoryPlacementWorkerCount() = %d, want %d", got, DefaultMemoryPlacementWorkerCount)
	}
	if got := cfg.GetMemoryPlacementPollSeconds(); got != DefaultMemoryPlacementPollSeconds {
		t.Fatalf("GetMemoryPlacementPollSeconds() = %d, want %d", got, DefaultMemoryPlacementPollSeconds)
	}
}

func TestAIEmbeddingMaxConcurrency(t *testing.T) {
	if got := AIEmbeddingMaxConcurrency(nil); got != DefaultAIEmbeddingMaxConcurrency {
		t.Fatalf("AIEmbeddingMaxConcurrency(nil) = %d, want %d", got, DefaultAIEmbeddingMaxConcurrency)
	}

	legacy := configProviderWithoutEmbeddingConcurrency{ConfigProvider: &Config{}}
	if got := AIEmbeddingMaxConcurrency(legacy); got != DefaultAIEmbeddingMaxConcurrency {
		t.Fatalf("AIEmbeddingMaxConcurrency(legacy) = %d, want %d", got, DefaultAIEmbeddingMaxConcurrency)
	}

	if got := AIEmbeddingMaxConcurrency(&Config{}); got != DefaultAIEmbeddingMaxConcurrency {
		t.Fatalf("AIEmbeddingMaxConcurrency(empty config) = %d, want %d", got, DefaultAIEmbeddingMaxConcurrency)
	}

	invalid := embeddingConcurrencyOverride{ConfigProvider: &Config{}, concurrency: -1}
	if got := AIEmbeddingMaxConcurrency(invalid); got != DefaultAIEmbeddingMaxConcurrency {
		t.Fatalf("AIEmbeddingMaxConcurrency(invalid) = %d, want %d", got, DefaultAIEmbeddingMaxConcurrency)
	}

	cfg := &Config{AIEmbeddingMaxConcurrency: 12}
	if got := AIEmbeddingMaxConcurrency(cfg); got != 12 {
		t.Fatalf("AIEmbeddingMaxConcurrency(configured) = %d, want 12", got)
	}
}

func TestLoadWorkerConcurrencyOverrides(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("AI_VERIFIER_MAX_CONCURRENCY", "30")
	os.Setenv("MEMORY_PLACEMENT_WORKER_COUNT", "30")
	os.Setenv("MEMORY_PLACEMENT_POLL_SECONDS", "7")
	os.Setenv("AI_API_EMBEDDING_MAX_CONCURRENCY", "8")
	os.Setenv("EMBEDDING_WORKER_COUNT", "2")
	os.Setenv("EMBEDDING_BATCH_SIZE", "128")
	os.Setenv("EMBEDDING_JOB_POLL_SECONDS", "3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.GetAIVerifierMaxConcurrency() != 30 || cfg.GetMemoryPlacementWorkerCount() != 30 {
		t.Fatalf("placement/verifier concurrency = %d/%d, want 30/30", cfg.GetMemoryPlacementWorkerCount(), cfg.GetAIVerifierMaxConcurrency())
	}
	if cfg.GetMemoryPlacementPollSeconds() != 7 {
		t.Fatalf("placement poll = %d, want 7", cfg.GetMemoryPlacementPollSeconds())
	}
	if cfg.GetAIEmbeddingMaxConcurrency() != 8 || cfg.GetEmbeddingWorkerCount() != 2 {
		t.Fatalf("embedding worker/provider concurrency = %d/%d, want 2/8", cfg.GetEmbeddingWorkerCount(), cfg.GetAIEmbeddingMaxConcurrency())
	}
	if cfg.GetEmbeddingBatchSize() != 128 || cfg.GetEmbeddingJobPollSeconds() != 3 {
		t.Fatalf("embedding batch/poll = %d/%d, want 128/3", cfg.GetEmbeddingBatchSize(), cfg.GetEmbeddingJobPollSeconds())
	}
}
