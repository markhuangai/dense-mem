package config

import (
	"os"
	"testing"
)

func TestLoadSessionModelOverrides(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()
	setRequiredModelEnv()
	for key, value := range map[string]string{
		"AI_REMEMBER_MODEL":          " remember-model ",
		"AI_CONFLICT_REVIEW_MODEL":   "conflict-model",
		"AI_DREAM_GRAPH_MODEL":       "dream-graph-model",
		"AI_DREAM_EVIDENCE_MODEL":    "dream-evidence-model",
		"AI_COMMUNITY_SUMMARY_MODEL": "community-model",
	} {
		os.Setenv(key, value)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	assertSessionModels(t, &cfg, map[string]string{
		"remember": "remember-model", "conflict review": "conflict-model",
		"dream graph": "dream-graph-model", "dream evidence": "dream-evidence-model",
		"community summary": "community-model",
	})
}

func TestSessionModelOverridesFallbackIndependently(t *testing.T) {
	for _, override := range []string{"", " \t "} {
		cfg := &Config{
			AIVerifierModel: " verifier-model ", AIRememberModel: override,
			AIConflictReviewModel: override, AIDreamGraphModel: override,
			AIDreamEvidenceModel: override, AICommunitySummaryModel: override,
		}
		assertSessionModels(t, cfg, map[string]string{
			"remember": "verifier-model", "conflict review": "verifier-model",
			"dream graph": "verifier-model", "dream evidence": "verifier-model",
			"community summary": "verifier-model",
		})
	}
}

func assertSessionModels(t *testing.T, cfg *Config, want map[string]string) {
	t.Helper()
	for name, got := range map[string]string{
		"remember": cfg.GetAIRememberModel(), "conflict review": cfg.GetAIConflictReviewModel(),
		"dream graph": cfg.GetAIDreamGraphModel(), "dream evidence": cfg.GetAIDreamEvidenceModel(),
		"community summary": cfg.GetAICommunitySummaryModel(),
	} {
		if got != want[name] {
			t.Errorf("%s model = %q, want %q", name, got, want[name])
		}
	}
}
