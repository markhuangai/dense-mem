package serverapp

import (
	"log/slog"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestApplicationBundleBuildsEveryCapabilityService(t *testing.T) {
	bundle := buildApplicationBundle(applicationCompositionDependencies{
		Metrics: observability.NoopDiscoverabilityMetrics(),
		Logger:  observability.New(slog.LevelInfo),
	})
	if bundle.Remember == nil || bundle.Recall == nil || bundle.Community == nil || bundle.Lifecycle == nil || bundle.Context == nil || bundle.Dream == nil || bundle.ControlDream == nil || bundle.Graph == nil || bundle.MemoryPack == nil || bundle.RecallFeedback == nil {
		t.Fatal("application composition did not build every capability service")
	}
}

func TestAccessCompositionReusesEarlyAuthenticationBindings(t *testing.T) {
	logger := observability.New(slog.LevelInfo)
	authentication := buildAccessAuthenticationApplication(nil, nil, 1, logger)
	application := buildAccessApplication(accessApplicationDependencies{
		CredentialVerifier: authentication.CredentialVerifier,
		ActivityWriter:     authentication.ActivityWriter,
		Logger:             logger,
	})
	if application.CredentialVerifier != authentication.CredentialVerifier {
		t.Fatal("access composition replaced the early credential verifier")
	}
	if application.ActivityWriter != authentication.ActivityWriter {
		t.Fatal("access composition replaced the early credential activity writer")
	}
}

func TestSearchProviderCompositionBuildsSharedProviderFacets(t *testing.T) {
	providers := buildSearchProviders(
		config.Config{},
		nil,
		nil,
		observability.NoopDiscoverabilityMetrics(),
		observability.New(slog.LevelInfo),
	)
	if providers == nil || providers.EmbeddingProvider == nil || providers.RetryEmbedding == nil || providers.Convergence == nil || providers.Reconciliation == nil {
		t.Fatal("search composition did not build all provider facets")
	}
}
