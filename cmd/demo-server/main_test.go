package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
)

func TestValidateDemoStartupConfig_AllowsMissingControlPortalToken(t *testing.T) {
	cfg := validDemoStartupConfig()
	cfg.ControlPortalToken = ""

	require.NoError(t, validateDemoStartupConfig(&cfg))
}

func TestValidateDemoStartupConfig_RequiresReviewerAndVerifierModels(t *testing.T) {
	tests := []struct {
		name  string
		mut   func(*config.Config)
		field string
	}{
		{
			name: "reviewer model",
			mut: func(cfg *config.Config) {
				cfg.AIReviewerModel = ""
			},
			field: "AI_REVIEWER_MODEL",
		},
		{
			name: "verifier model",
			mut: func(cfg *config.Config) {
				cfg.AIVerifierModel = ""
			},
			field: "AI_VERIFIER_MODEL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validDemoStartupConfig()
			tt.mut(&cfg)

			err := validateDemoStartupConfig(&cfg)
			require.Error(t, err)
			var validationErr *config.ValidationError
			require.True(t, errors.As(err, &validationErr))
			require.Equal(t, tt.field, validationErr.Field)
		})
	}
}

func validDemoStartupConfig() config.Config {
	return config.Config{
		AIAPIURL:              "http://ai.example.test/v1",
		AIAPIKey:              "sk-test",
		AIEmbeddingModel:      "text-embedding-3-large",
		AIEmbeddingDimensions: 3072,
		AIReviewerModel:       "gpt-5.4-mini",
		AIVerifierModel:       "gpt-5.4-mini",
		RedisAddr:             "redis:6379",
	}
}
