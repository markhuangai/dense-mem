package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func decisionEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"AI_DECISION_PROVIDER", "AI_DECISION_API_URL", "AI_DECISION_API_KEY", "AI_DECISION_MODEL", "AI_DECISION_MAX_INPUT_TOKENS", "AI_DECISION_MAX_QUESTION_INPUT_TOKENS", "AI_DECISION_TIMEOUT_SECONDS", "AI_DECISION_MAX_CONCURRENCY", "AI_DECISION_LONG_CONTEXT_ENABLED"} {
		t.Setenv(key, "")
	}
}

func TestDecisionConfigurationDefaultsAndExplicitCredentials(t *testing.T) {
	decisionEnvironment(t)
	cfg, err := loadDecisionProviderConfig()
	if err != nil || cfg.Configured() {
		t.Fatalf("unconfigured decision provider: %+v, %v", cfg, err)
	}
	t.Setenv("AI_API_KEY", "embedding-credential")
	t.Setenv("AI_VERIFIER_API_KEY", "generative-credential")
	t.Setenv("AI_DECISION_PROVIDER", "openai")
	t.Setenv("AI_DECISION_API_URL", "https://api.openai.com/v1")
	t.Setenv("AI_DECISION_MODEL", "gpt-6-luna")
	t.Setenv("AI_DECISION_MAX_INPUT_TOKENS", "32000")
	if _, err := loadDecisionProviderConfig(); err == nil || !strings.Contains(err.Error(), "AI_DECISION_API_KEY") {
		t.Fatalf("decision key must not inherit another credential: %v", err)
	}
	t.Setenv("AI_DECISION_API_KEY", "decision-test-credential")
	cfg, err = loadDecisionProviderConfig()
	if err != nil || !cfg.Configured() || cfg.TimeoutSeconds != 10 || cfg.MaxConcurrency != 4 || cfg.MaxQuestionInputTokens != 32000 {
		t.Fatalf("configured provider defaults: %+v, %v", cfg, err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(encoded), "decision-test-credential") {
		t.Fatal("decision credential entered serialized configuration")
	}
}

func TestDecisionConfigurationRejectsInvalidOrUnboundedRequests(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"AI_DECISION_PROVIDER", "unknown"},
		{"AI_DECISION_API_URL", "https://user:credential@api.openai.com/v1"},
		{"AI_DECISION_API_URL", "https://api.openai.com/v1?key=credential"},
		{"AI_DECISION_MODEL", ""},
		{"AI_DECISION_MAX_INPUT_TOKENS", ""},
		{"AI_DECISION_MAX_INPUT_TOKENS", "0"},
		{"AI_DECISION_MAX_QUESTION_INPUT_TOKENS", "32001"},
		{"AI_DECISION_TIMEOUT_SECONDS", "-1"},
		{"AI_DECISION_MAX_CONCURRENCY", "0"},
		{"AI_DECISION_LONG_CONTEXT_ENABLED", "true"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			decisionEnvironment(t)
			for key, value := range map[string]string{"AI_DECISION_PROVIDER": "openai", "AI_DECISION_API_URL": "https://api.openai.com/v1", "AI_DECISION_API_KEY": "fixture", "AI_DECISION_MODEL": "gpt-6-luna", "AI_DECISION_MAX_INPUT_TOKENS": "32000"} {
				t.Setenv(key, value)
			}
			t.Setenv(tc.key, tc.value)
			if _, err := loadDecisionProviderConfig(); err == nil {
				t.Fatal("invalid decision configuration was accepted")
			}
		})
	}
}

func TestTypeSafeDecisionConfigurationContextOptIn(t *testing.T) {
	decisionEnvironment(t)
	for key, value := range map[string]string{"AI_DECISION_PROVIDER": "typesafe", "AI_DECISION_API_URL": "https://api.typesafe.ai/v1", "AI_DECISION_API_KEY": "fixture", "AI_DECISION_MODEL": "jev-1.14.0", "AI_DECISION_MAX_INPUT_TOKENS": "64000", "AI_DECISION_MAX_QUESTION_INPUT_TOKENS": "32000"} {
		t.Setenv(key, value)
	}
	if _, err := loadDecisionProviderConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_DECISION_MAX_INPUT_TOKENS", "262000")
	t.Setenv("AI_DECISION_MAX_QUESTION_INPUT_TOKENS", "262000")
	if _, err := loadDecisionProviderConfig(); err == nil {
		t.Fatal("long context was enabled without explicit opt-in")
	}
	t.Setenv("AI_DECISION_LONG_CONTEXT_ENABLED", "true")
	if _, err := loadDecisionProviderConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_DECISION_MAX_INPUT_TOKENS", "262001")
	if _, err := loadDecisionProviderConfig(); err == nil {
		t.Fatal("request exceeded the long-context bound")
	}
}

func TestDecisionConfigurationLoadedAndProtected(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()
	setRequiredModelEnv()
	decisionEnvironment(t)
	for key, value := range map[string]string{"AI_DECISION_PROVIDER": "openai", "AI_DECISION_API_URL": "https://api.openai.com/v1", "AI_DECISION_API_KEY": "decision-protected-key", "AI_DECISION_MODEL": "gpt-6-luna", "AI_DECISION_MAX_INPUT_TOKENS": "32000"} {
		t.Setenv(key, value)
	}
	cfg, err := Load()
	if err != nil || !cfg.Decision.Configured() {
		t.Fatalf("Load decision configuration: %v", err)
	}
	secrets, err := cfg.OperationalSecrets()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, secret := range secrets {
		found = found || secret == "decision-protected-key"
	}
	if !found {
		t.Fatal("decision credential is absent from operational secret protection")
	}
}

func TestUnsetDecisionProviderIgnoresResidualConnectionSettings(t *testing.T) {
	decisionEnvironment(t)
	for key, value := range map[string]string{"AI_DECISION_API_URL": "https://api.openai.com/v1", "AI_DECISION_API_KEY": "residual-private-key", "AI_DECISION_MODEL": "gpt-6-luna", "AI_DECISION_MAX_INPUT_TOKENS": "invalid-disabled-value"} {
		t.Setenv(key, value)
	}
	for _, selection := range []string{"", "disabled"} {
		t.Setenv("AI_DECISION_PROVIDER", selection)
		cfg, err := loadDecisionProviderConfig()
		if err != nil || cfg.Configured() || cfg.Provider != "disabled" {
			t.Fatalf("disabled selection must retain existing behavior: %v", err)
		}
	}
	t.Setenv("AI_DECISION_PROVIDER", "openai")
	if _, err := loadDecisionProviderConfig(); err == nil {
		t.Fatal("enabled provider did not validate its limits")
	}
}
