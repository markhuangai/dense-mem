//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRecallRelationshipsReportsFailedCurrentGenerationWithoutCandidateHit(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-failed-generation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-failed-generation-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-rel-failed-generation", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Mika")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"relationship recall failed generation", "Mika works on Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:relationship-failed-generation",
			SpanStart:      0,
			SpanEnd:        len("Mika works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	generationID := uuid.NewString()
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO search_projection_generations (
			    team_id, projection_generation_id, source_kind, generation,
			    projection_format_version, state, eligible_count, projected_count,
			    current_vector_count, failed_job_count, completed_at, activated_at
			) VALUES (
			    ?::uuid, ?::uuid, 'relationship', 1,
			    2, 'current', 1, 1,
			    1, 0, now(), now()
			)
		`, teamID, generationID).Error
	}))
	_, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerID,
		SourceKind:             "relationship",
		SourceID:               decision.Relationship.RelationshipID,
		SourceVersion:          int64(decision.Relationship.Version),
		ProjectionGenerationID: generationID,
		DocumentText:           "relationship\nsubject: Mika\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	claimed, err := searchRepo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "recall-rel-failed-worker", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, err = searchRepo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: claimed[0].EmbeddingJobID,
		WorkerID: "recall-rel-failed-worker", ExpectedAttempts: claimed[0].Attempts,
		FailureClass: string(domain.EmbeddingFailureTransient),
		FailureCode:  string(domain.EmbeddingFailureProviderTimeout), Terminal: true,
	})
	require.NoError(t, err)

	recall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:         teamID,
		Query:          "unmatchedtoken",
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          5,
	})
	require.NoError(t, err)
	require.True(t, recall.VectorOmitted)
	require.Equal(t, string(domain.SearchProjectionFailed), recall.SearchState)
	require.Empty(t, recall.Results)
}
