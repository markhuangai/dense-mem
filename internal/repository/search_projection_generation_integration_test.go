package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelationshipProjectionGenerationRefreshRecomputesEligibleAndClearsFailedState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-rel-generation-refresh-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-rel-generation-refresh-owner")
	insertSearchTestContract(t, adminDB, rls, "search-rel-generation-refresh", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	repo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Avery")
	secondSubject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Bailey")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	firstIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"relationship projection refresh first", "Avery works on Dense Mem.")
	first := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "search:relationship-generation-refresh-first",
			SpanStart:      0,
			SpanEnd:        len("Avery works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, first.Relationship)
	secondIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"relationship projection refresh second", "Bailey works on Dense Mem.")
	second := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        secondIngest.IngestID,
		SubjectEntityID: secondSubject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     secondIngest.Evidence[0].FragmentID,
			SourceGroupKey: "search:relationship-generation-refresh-second",
			SpanStart:      0,
			SpanEnd:        len("Bailey works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, second.Relationship)

	generationID := uuid.NewString()
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO search_projection_generations (
			    team_id, projection_generation_id, source_kind, generation,
			    projection_format_version, state, eligible_count, projected_count,
			    current_vector_count, failed_job_count, last_error
			) VALUES (
			    ?::uuid, ?::uuid, 'relationship', 1, 2,
			    'failed', 2, 0, 0, 1, 'stale failed state'
			)
		`, teamID, generationID).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE relationship_records
			SET support_count = 0,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, second.Relationship.RelationshipID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("expected to detach one relationship from generation eligibility, got %d", res.RowsAffected)
		}
		return nil
	}))

	doc, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerID,
		SourceKind:             "relationship",
		SourceID:               first.Relationship.RelationshipID,
		SourceVersion:          int64(first.Relationship.Version),
		ProjectionGenerationID: generationID,
		DocumentText:           "relationship\nsubject: Avery\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	completeSearchJobsForTest(t, repo, teamID, map[string][]float32{
		doc.SearchDocumentID: {1, 0, 0},
	})

	var state string
	var eligibleCount, projectedCount, currentVectorCount, failedJobCount int64
	var lastError string
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT state, eligible_count, projected_count, current_vector_count,
			       failed_job_count, last_error
			FROM search_projection_generations
			WHERE team_id = ?::uuid
			  AND projection_generation_id = ?::uuid
		`, teamID, generationID).Row().Scan(
			&state,
			&eligibleCount,
			&projectedCount,
			&currentVectorCount,
			&failedJobCount,
			&lastError,
		)
	})
	require.NoError(t, err)
	assert.Equal(t, "current", state)
	assert.EqualValues(t, 1, eligibleCount)
	assert.EqualValues(t, 1, projectedCount)
	assert.EqualValues(t, 1, currentVectorCount)
	assert.EqualValues(t, 0, failedJobCount)
	assert.Empty(t, lastError)
}
