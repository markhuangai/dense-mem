package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// Conflict resolution supersedes losing Relationships; it must retire their
// search projections without asking the embedding provider to render them.
func TestConflictReviewRetiresLosingSearchProjectionWithoutEmbedding(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-review-synchronous", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-review-synchronous-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-synchronous-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-synchronous-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-synchronous-owner-c")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepositoryWithRuntimeConfig(appDB, rls, ConflictRuntimeConfig{ReviewTTLDays: 2, Timezone: "UTC"})

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Dense-Mem")
	preferredObject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "PostgreSQL")
	losingObject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "GraphDB")
	preferred := commitConflictRememberFixture(t, ctx, ledger, teamID, ownerA, subject.EntityID, preferredObject.EntityID, "Dense-Mem uses PostgreSQL.", "conflict-preferred")
	loser := commitConflictRememberFixture(t, ctx, ledger, teamID, ownerB, subject.EntityID, losingObject.EntityID, "Dense-Mem uses GraphDB.", "conflict-loser")
	_ = commitConflictRememberFixture(t, ctx, ledger, teamID, ownerC, subject.EntityID, preferredObject.EntityID, "Dense-Mem uses PostgreSQL for the team.", "conflict-preferred-second")
	require.NotEmpty(t, preferred.RelationshipResults)
	require.NotEmpty(t, loser.RelationshipResults)

	var conflictID string
	var dueAt time.Time
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT conflict_id::text, review_due_at
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			ORDER BY created_at
			LIMIT 1
		`, teamID).Row().Scan(&conflictID, &dueAt)
	}))
	require.NotEmpty(t, conflictID)
	reviewNow := dueAt.Add(time.Minute)
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID: teamID, WorkerID: "conflict-reviewer", LocalRunDate: reviewNow, Timezone: "UTC", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	cases, err := ledger.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
		TeamID: teamID, WorkerID: "conflict-reviewer", ReviewRunID: run.ReviewRunID,
		Limit: 10, Lease: time.Minute, MaxAttempts: 5, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)

	result, err := ledger.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID: teamID, WorkerID: "conflict-reviewer", ReviewRunID: run.ReviewRunID,
		ConflictID: conflictID, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Equal(t, ConflictReviewOutcomeResolve, result.Outcome)
	require.ElementsMatch(t, []string{loser.RelationshipResults[0].Relationship.RelationshipID}, result.UpdatedRelationships)

	var loserStatus, searchState string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Scan(&loserStatus).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT search_state FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Scan(&searchState).Error
	}))
	require.Equal(t, string(domain.RelationshipStatusSuperseded), loserStatus)
	require.Equal(t, string(domain.SearchProjectionNotRequired), searchState)
}
func commitConflictRememberFixture(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID, ownerID, subjectID, objectID, content, key string,
) *SynchronousRememberCommitResult {
	t.Helper()
	fragmentID, ingestID, assessmentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	relationshipRef := key + ":relationship"
	input := SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
		IdempotencyKey: key, RequestHash: sha256Hex(content), SourceSummary: key,
		Evidence: []EvidenceInput{{
			FragmentID: fragmentID, Content: content, ContentHash: sha256Hex(content),
			SourceType: "conversation", Authority: "primary",
		}},
		AssessmentID: assessmentID, AssessmentJSON: json.RawMessage(`{"request_id":"` + key + `"}`), ProviderTurns: 1,
		Commit: CommitSubmissionAssessmentInput{
			AssessmentID: assessmentID,
			Items:        []SubmissionAssessmentItemInput{{FragmentID: fragmentID}},
			EntityResolutions: []SubmissionAssessmentEntityResolutionInput{
				{Resolution: SemanticEntityResolutionInput{MentionRef: "subject", Action: string(domain.EntityResolutionReuse), EntityID: subjectID, ExactEntityID: subjectID, FragmentID: fragmentID, AssessmentID: assessmentID}},
				{Resolution: SemanticEntityResolutionInput{MentionRef: "object", Action: string(domain.EntityResolutionReuse), EntityID: objectID, ExactEntityID: objectID, FragmentID: fragmentID, AssessmentID: assessmentID}},
			},
			RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{{
				RelationshipRef: relationshipRef,
				Observation: SemanticRelationshipDecisionInput{
					Ref: relationshipRef, SubjectRef: "subject", OriginalPredicate: "primary_database", PredicateKey: "primary_database", PredicateVersion: 1,
					ObjectRef: "object", Polarity: "+", AssessorAccepted: true, AssessmentID: assessmentID,
					Support: &EvidenceSupportInput{FragmentID: fragmentID, SourceGroupKey: key, SpanStart: 0, SpanEnd: len(content), Quote: content, Authority: "primary"},
				},
			}},
			RelationshipResults: []SubmissionRelationshipResultInput{{RelationshipRef: relationshipRef, Disposition: "stored"}},
			Payload:             map[string]any{"response_hash": sha256Hex(key), "model": "test-model", "tokenizer": "o200k_base", "candidate_context_tokens": 0, "candidate_context_truncated": false},
		},
	}
	plan, err := repo.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	vectors := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for index, document := range plan.Documents {
		vector := []float32{1, 0, 0}
		if index%2 == 1 {
			vector = []float32{0, 1, 0}
		}
		vectors = append(vectors, InlineEmbeddingResult{
			DocumentHash: document.DocumentHash, Embedding: vector,
			EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions,
			EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration,
		})
	}
	result, err := repo.CommitRememberWithEmbeddings(ctx, input, vectors)
	require.NoError(t, err)
	return result
}
