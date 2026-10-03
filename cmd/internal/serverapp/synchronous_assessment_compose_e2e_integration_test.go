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

type composeSubmissionAssessmentCatalogStub struct{}

func (composeSubmissionAssessmentCatalogStub) ListSubmissionAssessmentEntityCatalog(
	_ context.Context,
	input knowledgecontract.SubmissionAssessmentEntityCatalogInput,
) (knowledgecontract.SubmissionAssessmentEntityCatalogResult, error) {
	result := knowledgecontract.SubmissionAssessmentEntityCatalogResult{Complete: true}
	for _, entity := range input.Entities {
		result.Groups = append(result.Groups, knowledgecontract.SubmissionAssessmentEntityCatalogGroup{Ref: entity.Ref, Complete: true})
	}
	return result, nil
}

func (composeSubmissionAssessmentCatalogStub) ResolveSemanticReviewPredicateCandidates(
	context.Context,
	knowledgecontract.SemanticReviewPredicateResolutionInput,
) ([]knowledgecontract.SemanticReviewPredicateResolution, error) {
	return []knowledgecontract.SemanticReviewPredicateResolution{}, nil
}

func (composeSubmissionAssessmentCatalogStub) ListSemanticAssessmentPredicateOptions(
	context.Context,
	knowledgecontract.SemanticAssessmentPredicateOptionsInput,
) ([]knowledgecontract.SemanticReviewPredicateCandidate, error) {
	return []knowledgecontract.SemanticReviewPredicateCandidate{}, nil
}

func (composeSubmissionAssessmentCatalogStub) ValidateSubmissionPredicateRegistrations(
	context.Context,
	knowledgecontract.SubmissionPredicateRegistrationValidationInput,
) ([]knowledgecontract.SubmissionPredicateRegistrationIssue, error) {
	return nil, nil
}

func TestComposeSynchronousUnsupportedProposalPreservesSafeEvidence(t *testing.T) {
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
		{FragmentID: uuid.NewString(), EvidenceIndex: 0, Content: "Dense-Mem uses PostgreSQL. [fixture-fault:no-supported]", Authority: "primary"},
		{FragmentID: uuid.NewString(), EvidenceIndex: 1, Content: "Dense-Mem stores memory in PostgreSQL. [fixture-fault:no-supported]", Authority: "primary"},
	}
	snapshot := rememberapp.RememberAssessmentSnapshot{
		Scope: rememberapp.RememberAssessmentScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID},
		Proposal: map[string]any{"relationship_hints": []map[string]any{{
			"ref": "unsupported", "subject": map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"entity": map[string]any{"name": "PostgreSQL", "entity_kind": "product"}},
			"polarity":  "+", "evidence_indices": []any{0, 1},
		}}}, Evidence: evidence,
		Items: []rememberapp.RememberAssessmentItem{{ItemID: uuid.NewString(), Fragment: evidence[0]}, {ItemID: uuid.NewString(), Fragment: evidence[1]}},
	}
	prepared, err := rememberapp.AssessSynchronousRemember(context.Background(), rememberapp.SynchronousAssessmentDependencies{
		Catalog:  composeSubmissionAssessmentCatalogStub{},
		Provider: assessorProvider, Limits: limits,
	}, rememberapp.SynchronousAssessmentInput{Scope: snapshot.Scope, Snapshot: snapshot})
	require.NoError(t, err)
	require.Equal(t, 1, prepared.Assessment.ProviderTurns)
	require.Len(t, prepared.Request.SubmittedEntities, 2)
	require.Len(t, prepared.Request.SubmittedRelationships, 1)
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
		IdempotencyKey: "compose-unsupported-proposal", RequestHash: "sha256:compose-unsupported-proposal",
		Evidence: commitEvidence, Assessment: prepared,
	})
	require.NoError(t, err)
	require.Len(t, commit.Commit.Items, len(evidence))
	require.Len(t, commit.Commit.EntityResolutions, 2)
	require.Empty(t, commit.Commit.RelationshipObservations)
	require.Len(t, commit.Commit.RelationshipResults, 1)
	require.Equal(t, "not_stored", commit.Commit.RelationshipResults[0].Disposition)
	require.Equal(t, "not_supported_by_evidence", commit.Commit.RelationshipResults[0].Reason)
	require.Len(t, commit.EvidenceSecurityResults, len(evidence))
	for _, result := range commit.EvidenceSecurityResults {
		require.Equal(t, "pass", result.Decision)
		require.True(t, result.Safe)
	}
}
