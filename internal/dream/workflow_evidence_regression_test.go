package dream

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func TestEvidenceCycleLeaseCoversRegenerationBudget(t *testing.T) {
	providerLease := 16 * time.Minute
	service := New(Dependencies{ProviderCycleLease: providerLease}).(*service)

	require.Equal(t,
		providerLease*evidenceDiscoveryTargetLimit*evidenceDiscoveryPassLimit*evidenceDiscoveryRegenerationLimit,
		service.evidenceCycleLease(),
	)
}

func TestScheduledEvidenceCyclePersistsProviderFailureDiagnostics(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	store := &dreamRepositoryStub{}
	evidenceStore := &evidenceRepositoryStub{targets: []dreamcontract.EvidenceDiscoveryTargetInput{{
		Target: dreamcontract.EvidenceTarget{EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary"},
	}}}
	providerErr := &modelprovider.MalformedResponseError{Provider: "fixture", Message: "invalid", FailureClass: "malformed_exhausted", Attempts: 5}
	diagnostics := GenerationDiagnostics{ProviderTurns: 5, ProviderInputTokens: 55, ProviderOutputTokens: 35}
	service := New(Dependencies{
		Store: store, ScheduledStore: store, EvidenceStore: evidenceStore,
		EvidenceGenerator: &evidenceGeneratorStub{model: "evidence-model", err: providerErr, errorDiagnostics: diagnostics},
		AppConfig:         cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC"}},
	}).(*service)

	result, err := service.RunScheduledEvidenceCycle(context.Background(), teamID, time.Date(2026, 9, 4, 4, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, providerErr)
	require.Equal(t, diagnostics.ProviderTurns, result.ProviderTurns)
	require.Equal(t, diagnostics.ProviderInputTokens, result.ProviderInputTokens)
	require.Equal(t, diagnostics.ProviderOutputTokens, result.ProviderOutputTokens)
	require.Equal(t, diagnostics.ProviderTurns, store.completeInput.ProviderTurns)
	require.Equal(t, diagnostics.ProviderInputTokens, store.completeInput.ProviderInputTokens)
	require.Equal(t, diagnostics.ProviderOutputTokens, store.completeInput.ProviderOutputTokens)
}
