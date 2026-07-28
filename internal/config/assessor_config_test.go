package config

import (
	"math"
	"os"
	"testing"
)

type assessorConfigProviderStub struct {
	ConfigProvider
	inputTokens            int
	outputTokens           int
	candidateContextTokens int
	tokenizer              string
	confidenceThreshold    float64
}

func (s assessorConfigProviderStub) GetAIVerifierMaxInputTokens() int {
	return s.inputTokens
}

func (s assessorConfigProviderStub) GetAIVerifierMaxOutputTokens() int {
	return s.outputTokens
}

func (s assessorConfigProviderStub) GetAIVerifierMaxCandidateContextTokens() int {
	return s.candidateContextTokens
}

func (s assessorConfigProviderStub) GetAIVerifierTokenizer() string {
	return s.tokenizer
}

func (s assessorConfigProviderStub) GetMemoryAutoWriteConfidenceThreshold() float64 {
	return s.confidenceThreshold
}

func TestMemoryAutoWriteConfidenceThresholdDefaultsOnlyWhenUnset(t *testing.T) {
	if got := (&Config{}).GetMemoryAutoWriteConfidenceThreshold(); got != DefaultMemoryAutoWriteConfidenceThreshold {
		t.Fatalf("zero-value threshold = %v, want default %v", got, DefaultMemoryAutoWriteConfidenceThreshold)
	}
	if got := (&Config{MemoryAutoWriteConfidenceThreshold: 0.5}).GetMemoryAutoWriteConfidenceThreshold(); got != 0.5 {
		t.Fatalf("configured threshold = %v, want 0.5", got)
	}

	clearEnv()
	setRequiredEnv()
	t.Setenv("MEMORY_AUTO_WRITE_CONFIDENCE_THRESHOLD", "0")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.GetMemoryAutoWriteConfidenceThreshold(); got != 0 {
		t.Fatalf("explicit zero threshold = %v, want 0", got)
	}
}

func TestLoadAssessorTokenBudgetAndRejectsObsoleteVariables(t *testing.T) {
	t.Run("overrides", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()
		os.Setenv("AI_VERIFIER_MAX_INPUT_TOKENS", "1234")
		os.Setenv("AI_VERIFIER_MAX_OUTPUT_TOKENS", "567")
		os.Setenv("AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS", "789")
		os.Setenv("AI_VERIFIER_TOKENIZER", "cl100k_base")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned unexpected error: %v", err)
		}
		budget := AIVerifierAssessmentBudgetFor(&cfg)
		if budget.MaxInputTokens != 1234 || budget.MaxOutputTokens != 567 || budget.MaxCandidateContextTokens != 789 || budget.Tokenizer != "cl100k_base" {
			t.Fatalf("assessor budget = %#v", budget)
		}
	})

	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{"assessor prefix", "AI_ASSESSOR_MODEL", "old"},
		{"input bytes", "AI_VERIFIER_MAX_INPUT_BYTES", "131072"},
		{"output bytes", "AI_VERIFIER_MAX_OUTPUT_BYTES", "131072"},
		{"candidate bytes", "AI_VERIFIER_MAX_CANDIDATE_CONTEXT_BYTES", "32768"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv()
			setRequiredEnv()
			os.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() expected obsolete assessor configuration error, got nil")
			}
			validationErr, ok := err.(*ValidationError)
			if !ok || validationErr.Field != tc.key {
				t.Fatalf("Load() error = %v, want %s validation error", err, tc.key)
			}
		})
	}
}

func TestAssessorConfigFallbacksAndConfidenceValidation(t *testing.T) {
	budget := AIVerifierAssessmentBudgetFor(assessorConfigProviderStub{
		inputTokens:            -1,
		outputTokens:           0,
		candidateContextTokens: -2,
		tokenizer:              "   ",
	})
	if budget != (AIVerifierAssessmentBudget{
		MaxInputTokens:            DefaultAIVerifierMaxInputTokens,
		MaxOutputTokens:           DefaultAIVerifierMaxOutputTokens,
		MaxCandidateContextTokens: DefaultAIVerifierMaxCandidateContextTokens,
		Tokenizer:                 DefaultAIVerifierTokenizer,
	}) {
		t.Fatalf("fallback budget = %#v", budget)
	}

	budget = AIVerifierAssessmentBudgetFor(assessorConfigProviderStub{
		inputTokens:            10,
		outputTokens:           20,
		candidateContextTokens: 30,
		tokenizer:              " custom ",
	})
	if budget != (AIVerifierAssessmentBudget{MaxInputTokens: 10, MaxOutputTokens: 20, MaxCandidateContextTokens: 30, Tokenizer: "custom"}) {
		t.Fatalf("configured budget = %#v", budget)
	}

	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01, 1.01} {
		if got := MemoryAutoWriteConfidenceThresholdFor(assessorConfigProviderStub{confidenceThreshold: value}); got != DefaultMemoryAutoWriteConfidenceThreshold {
			t.Fatalf("threshold %v = %v, want default %v", value, got, DefaultMemoryAutoWriteConfidenceThreshold)
		}
	}
	if got := MemoryAutoWriteConfidenceThresholdFor(assessorConfigProviderStub{confidenceThreshold: 0}); got != 0 {
		t.Fatalf("explicit zero threshold = %v, want 0", got)
	}

	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01, 1.01} {
		if got := (&Config{MemoryAutoWriteConfidenceThreshold: value}).GetMemoryAutoWriteConfidenceThreshold(); got != DefaultMemoryAutoWriteConfidenceThreshold {
			t.Fatalf("Config threshold %v = %v, want default %v", value, got, DefaultMemoryAutoWriteConfidenceThreshold)
		}
	}
	if got := (&Config{MemoryAutoWriteConfidenceThreshold: 0, memoryAutoWriteConfidenceThresholdSet: true}).GetMemoryAutoWriteConfidenceThreshold(); got != 0 {
		t.Fatalf("explicit Config zero threshold = %v, want 0", got)
	}
}
