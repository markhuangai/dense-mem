//go:build integration

package serverapp

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestComposeEvidenceOnlyAssessmentPreservesExactEvidence(t *testing.T) {
	providerURL := strings.TrimSpace(os.Getenv("DENSE_MEM_E2E_PRIMITIVES_PROVIDER_URL"))
	if providerURL == "" {
		t.Fatal("DENSE_MEM_E2E_PRIMITIVES_PROVIDER_URL is required for the Compose assessor driver")
	}
	limits := assessor.DefaultSemanticAssessmentLimits()
	cfg := &config.Config{
		AIVerifierAPIURL: providerURL, AIVerifierAPIKey: "dense-mem-e2e-verifier-key",
		AIVerifierModel: "dense-mem-e2e-verifier", AIVerifierTimeoutSeconds: 10,
		AIVerifierMaxConcurrency: 1, AIVerifierDisableTemperature: true,
	}
	assessorProvider := assessorprovider.NewOpenAIAssessorWithAssessmentLimits(cfg, &http.Client{Timeout: 10 * time.Second}, limits)
	teamID, ownerID, ingestID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	evidence := []knowledgecontract.EvidenceFragment{
		{FragmentID: uuid.NewString(), EvidenceIndex: 0, Content: "Dense-Mem uses PostgreSQL.", Authority: "primary"},
		{FragmentID: uuid.NewString(), EvidenceIndex: 1, Content: "Dense-Mem stores memory in PostgreSQL.", Authority: "primary"},
	}
	snapshot := rememberapp.RememberAssessmentSnapshot{
		Scope:    rememberapp.RememberAssessmentScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID},
		Evidence: evidence,
		Items:    []rememberapp.RememberAssessmentItem{{ItemID: uuid.NewString(), Fragment: evidence[0]}, {ItemID: uuid.NewString(), Fragment: evidence[1]}},
	}
	prepared, err := rememberapp.AssessSynchronousRemember(context.Background(), rememberapp.SynchronousAssessmentDependencies{
		Provider: assessorProvider, Limits: limits,
	}, rememberapp.SynchronousAssessmentInput{Scope: snapshot.Scope, Snapshot: snapshot})
	require.NoError(t, err)
	require.Equal(t, 1, prepared.Assessment.ProviderTurns)
	require.Len(t, prepared.Response.EvidenceSecurityResults, len(evidence))
	require.Empty(t, prepared.Response.SecuritySignals)

	commitEvidence := make([]knowledgecontract.EvidenceInput, 0, len(evidence))
	for _, fragment := range evidence {
		commitEvidence = append(commitEvidence, knowledgecontract.EvidenceInput{
			FragmentID: fragment.FragmentID, Content: fragment.Content, ContentHash: fragment.ContentHash,
			SourceType: "manual", Authority: fragment.Authority,
		})
	}
	commit, err := rememberapp.BuildSynchronousRememberCommitInput(rememberapp.SynchronousRememberCommitRequest{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
		IdempotencyKey: "compose-evidence-only", RequestHash: "sha256:compose-evidence-only",
		Evidence: commitEvidence, Assessment: prepared,
	})
	require.NoError(t, err)
	require.Len(t, commit.Commit.Items, len(evidence))
	for index, item := range commit.Evidence {
		require.Equal(t, evidence[index].Content, item.Content)
	}
	require.Len(t, commit.EvidenceSecurityResults, len(evidence))
	for _, result := range commit.EvidenceSecurityResults {
		require.Equal(t, "pass", result.Decision)
		require.True(t, result.Safe)
	}
}
