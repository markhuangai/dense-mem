//go:build compose_e2e

package memoryservice

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
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestComposeSynchronousEvidenceOnlyAssessorBatch(t *testing.T) {
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
	evidence := []repository.EvidenceFragment{
		{FragmentID: uuid.NewString(), EvidenceIndex: 0, Content: "The first evidence item is safe.", Authority: "primary"},
		{FragmentID: uuid.NewString(), EvidenceIndex: 1, Content: "The second evidence item is also safe.", Authority: "primary"},
	}
	snapshot := RememberAssessmentSnapshot{
		Scope:    RememberAssessmentScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID},
		Proposal: map[string]any{}, Evidence: evidence,
		Items: []RememberAssessmentItem{{ItemID: uuid.NewString(), Fragment: evidence[0]}, {ItemID: uuid.NewString(), Fragment: evidence[1]}},
	}
	prepared, err := AssessSynchronousRemember(context.Background(), SynchronousAssessmentDependencies{
		Catalog:  &submissionAssessmentWorkerCatalogStub{entityComplete: true, predicateComplete: true},
		Provider: assessorProvider, Limits: limits,
	}, SynchronousAssessmentInput{Scope: snapshot.Scope, Snapshot: snapshot})
	require.NoError(t, err)
	require.Equal(t, 1, prepared.Assessment.ProviderTurns)
	require.Empty(t, prepared.Request.SubmittedEntities)
	require.Empty(t, prepared.Request.SubmittedRelationships)
	require.Len(t, prepared.Response.EvidenceSecurityResults, len(evidence))
	require.Empty(t, prepared.Response.SecuritySignals)

	commitEvidence := make([]repository.EvidenceInput, 0, len(evidence))
	for _, fragment := range evidence {
		commitEvidence = append(commitEvidence, repository.EvidenceInput{
			FragmentID: fragment.FragmentID, Content: fragment.Content, ContentHash: fragment.ContentHash,
			SourceType: "manual", Authority: fragment.Authority,
		})
	}
	commit, err := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
		IdempotencyKey: "compose-evidence-only", RequestHash: "sha256:compose-evidence-only",
		Evidence: commitEvidence, Assessment: prepared,
	})
	require.NoError(t, err)
	require.Len(t, commit.Commit.Items, len(evidence))
	require.Empty(t, commit.Commit.EntityResolutions)
	require.Empty(t, commit.Commit.RelationshipObservations)
	require.Empty(t, commit.Commit.RelationshipResults)
	require.Len(t, commit.EvidenceSecurityResults, len(evidence))
	for _, result := range commit.EvidenceSecurityResults {
		require.Equal(t, "pass", result.Decision)
		require.True(t, result.Safe)
	}
}
