package config

import (
	"strconv"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

func TestValidateServerStartupReportsCandidateContextBudgetWhenMinimumCannotFit(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()
	setRequiredModelEnv()
	t.Setenv("AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	err = cfg.ValidateServerStartup()
	validationErr, ok := err.(*ValidationError)
	if !ok || validationErr.Field != "AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS" {
		t.Fatalf("ValidateServerStartup() error = %v, want assessor candidate-context budget validation error", err)
	}
}

func TestValidateServerStartupUsesDisabledTemperatureFraming(t *testing.T) {
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.ProviderModel = "verifier-model"
	limits.ProviderSchemaName = assessor.SemanticAssessmentSchemaName
	limits.ProviderTemperatureDisabled = false
	withTemperature, err := assessor.CountSemanticAssessmentProviderFramingTokens(limits)
	if err != nil {
		t.Fatalf("CountSemanticAssessmentProviderFramingTokens() with temperature: %v", err)
	}
	limits.ProviderTemperatureDisabled = true
	withoutTemperature, err := assessor.CountSemanticAssessmentProviderFramingTokens(limits)
	if err != nil {
		t.Fatalf("CountSemanticAssessmentProviderFramingTokens() without temperature: %v", err)
	}
	if withTemperature <= withoutTemperature {
		t.Fatalf("framing tokens with temperature = %d, without = %d; want disabled envelope smaller", withTemperature, withoutTemperature)
	}
	repairHeadroom := (assessor.SemanticAssessmentMaxProviderTurns - 1) * (limits.MaxOutputTokens + 4096)
	inputLimit := withoutTemperature + repairHeadroom + 1

	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()
	setRequiredModelEnv()
	t.Setenv("AI_VERIFIER_DISABLE_TEMPERATURE", "true")
	t.Setenv("AI_VERIFIER_MAX_INPUT_TOKENS", strconv.Itoa(inputLimit))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with disabled temperature: %v", err)
	}
	if err := cfg.ValidateServerStartup(); err != nil {
		t.Fatalf("ValidateServerStartup() with disabled temperature: %v", err)
	}

	t.Setenv("AI_VERIFIER_DISABLE_TEMPERATURE", "false")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() with enabled temperature: %v", err)
	}
	err = cfg.ValidateServerStartup()
	validationErr, ok := err.(*ValidationError)
	if !ok || validationErr.Field != "AI_VERIFIER_MAX_OUTPUT_TOKENS" {
		t.Fatalf("ValidateServerStartup() with enabled temperature = %v, want output budget error", err)
	}
}
