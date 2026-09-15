package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type recallConflictStore = conflictpostgres.Store

type recallKnowledgeConflictFixtureStore struct {
	*knowledgepostgres.Store
	*recallConflictStore
}

func newRecallKnowledgeConflictFixtureStore(db *gorm.DB, rls storagepostgres.RLSHelper) *recallKnowledgeConflictFixtureStore {
	knowledge := knowledgepostgres.NewStore(db, rls, knowledgepostgres.ConflictRuntimeConfig{})
	return &recallKnowledgeConflictFixtureStore{
		Store:               knowledge,
		recallConflictStore: conflictpostgres.NewStore(db, rls, knowledge),
	}
}

func (s *recallKnowledgeConflictFixtureStore) ResolveEvidenceConflict(ctx context.Context, input EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error) {
	return s.recallConflictStore.ResolveEvidenceConflict(ctx, input)
}

func (s *recallKnowledgeConflictFixtureStore) GetEvidenceConflict(ctx context.Context, input conflictpostgres.EvidenceConflictGetInput) (*conflictpostgres.EvidenceConflictGetResult, error) {
	return s.recallConflictStore.GetEvidenceConflict(ctx, input)
}

func loadRecallEvidenceConflictRecords(ctx context.Context, tx *gorm.DB, input RecallEvidenceInput, results []RecallEvidenceHit) ([]EvidenceConflictCaseRecord, error) {
	return LoadRecallEvidenceConflictRecords(ctx, tx, input, results)
}

func citedEvidenceRememberInput(teamID, ownerID, label, firstContent, secondContent, spaceID string, generation int64) knowledgepostgres.SynchronousRememberCommitInput {
	firstID, secondID := uuid.NewString(), uuid.NewString()
	assessmentID := uuid.NewString()
	input := knowledgepostgres.SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString(),
		SpaceID: spaceID, SpaceGeneration: generation, IdempotencyKey: label, RequestHash: sha256Hex(label), SourceSummary: label,
		Evidence: []knowledgepostgres.EvidenceInput{
			{FragmentID: firstID, Content: firstContent, ContentHash: sha256Hex(firstContent), SourceType: "manual", Authority: "primary"},
			{FragmentID: secondID, Content: secondContent, ContentHash: sha256Hex(secondContent), SourceType: "manual", Authority: "secondary"},
		},
		AssessmentID: assessmentID, AssessmentJSON: json.RawMessage(`{"request_id":"` + label + `"}`), ProviderTurns: 1,
		EvidenceSecurityResults: []knowledgepostgres.EvidenceSecurityResult{
			{FragmentID: firstID, EvidenceID: "evidence:0", EvidenceIndex: 0, Decision: "pass", Safe: true},
			{FragmentID: secondID, EvidenceID: "evidence:1", EvidenceIndex: 1, Decision: "pass", Safe: true},
		},
	}
	input.Commit = knowledgepostgres.CommitSubmissionAssessmentInput{
		AssessmentID: assessmentID,
		Items:        []knowledgepostgres.SubmissionAssessmentItemInput{{FragmentID: firstID, EvidenceID: "evidence:0"}, {FragmentID: secondID, EvidenceID: "evidence:1"}},
		EvidenceConflictResults: []knowledgepostgres.EvidenceConflictResultInput{{Positions: []knowledgepostgres.EvidenceConflictPositionInput{
			{EvidenceID: "evidence:0", Start: 0, End: 5},
			{EvidenceID: "evidence:1", Start: 0, End: 5},
		}}},
		Payload: map[string]any{"response_hash": sha256Hex(label), "model": "test-model", "tokenizer": "o200k_base", "candidate_context_tokens": 0, "candidate_context_truncated": false},
	}
	return input
}

type rememberEmbeddingCommitter interface {
	PlanRememberEmbeddings(context.Context, knowledgepostgres.SynchronousRememberCommitInput) (*knowledgepostgres.InlineEmbeddingPlan, error)
	CommitRememberWithEmbeddings(context.Context, knowledgepostgres.SynchronousRememberCommitInput, []knowledgepostgres.InlineEmbeddingResult) (*knowledgepostgres.SynchronousRememberCommitResult, error)
}

func commitCitedEvidenceFixture(t *testing.T, ctx context.Context, repo rememberEmbeddingCommitter, input knowledgepostgres.SynchronousRememberCommitInput) *knowledgepostgres.SynchronousRememberCommitResult {
	t.Helper()
	plan, err := repo.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	result, err := repo.CommitRememberWithEmbeddings(ctx, input, rememberTestEmbeddings(plan, false))
	require.NoError(t, err)
	return result
}

func conflictIDForTest(t *testing.T, db *gorm.DB, rls storagepostgres.RLSHelper, teamID string) string {
	t.Helper()
	var conflictID string
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT conflict_id::text FROM evidence_conflict_cases WHERE team_id = ?::uuid ORDER BY created_at LIMIT 1`, teamID).Row().Scan(&conflictID)
	}))
	return conflictID
}

func rememberTestEmbeddings(plan *knowledgepostgres.InlineEmbeddingPlan, invalid bool) []knowledgepostgres.InlineEmbeddingResult {
	results := make([]knowledgepostgres.InlineEmbeddingResult, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		dimensions := plan.EmbeddingDimensions
		if invalid {
			dimensions++
		}
		results = append(results, knowledgepostgres.InlineEmbeddingResult{DocumentHash: document.DocumentHash, Embedding: make([]float32, dimensions), EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: dimensions, EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration})
	}
	return results
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
