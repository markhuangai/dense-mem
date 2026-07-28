package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelationshipProjectionGenerationRefreshUsesTaggedMembershipAndClearsFailedState(t *testing.T) {
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

	generationID := uuid.NewString()
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO search_projection_generations (
			    team_id, projection_generation_id, source_kind, generation,
			    projection_format_version, state, eligible_count, projected_count,
			    current_vector_count, failed_job_count, last_error
			) VALUES (
			    ?::uuid, ?::uuid, 'relationship', 1, 2,
			    'failed', 1, 0, 0, 1, 'stale failed state'
			)
		`, teamID, generationID).Error
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
	foregroundDoc, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       second.Relationship.RelationshipID,
		SourceVersion:  int64(second.Relationship.Version),
		DocumentText:   "relationship\nsubject: Bailey\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	assert.Empty(t, foregroundDoc.ProjectionGenerationID)

	const workerID = "relationship-generation-refresh-worker"
	jobs, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: workerID,
		Limit:    2,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	for _, job := range jobs {
		if job.SearchDocumentID != doc.SearchDocumentID {
			continue
		}
		require.NoError(t, repo.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
			TeamID:           teamID,
			EmbeddingJobID:   job.EmbeddingJobID,
			WorkerID:         workerID,
			ExpectedAttempts: job.Attempts,
			Embedding:        []float32{1, 0, 0},
		}))
	}

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

func TestPlacementRelationshipSearchUpdateOnlyTouchesActiveContractDocument(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-active-contract-document-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "placement-active-contract-document-owner")
	oldContractID := insertSearchTestContract(t, adminDB, rls, "placement-old-contract-document", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Omar")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement active contract document", "Omar works on Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "placement:active-contract-document",
			SpanStart:      0,
			SpanEnd:        len("Omar works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	_, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Omar\npredicate: old contract marker\nobject: Dense Mem",
	})
	require.NoError(t, err)

	activeContractID := insertSearchTestContract(t, adminDB, rls, "placement-active-contract-document", 3, "exact", "")
	_, err = searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Omar\npredicate: active contract marker\nobject: Dense Mem",
	})
	require.NoError(t, err)

	inactive := *decision.Relationship
	inactive.Status = "pending_evidence"
	inactive.SupportCount = 0
	inactive.Version++
	var updated *SearchDocumentResult
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		var err error
		updated, err = upsertPlacementRelationshipSearchDocument(ctx, tx, CommitPlacementSemanticInput{
			TeamID:         teamID,
			OwnerProfileID: ownerID,
		}, &inactive, 20)
		return err
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, activeContractID, updated.EmbeddingContractID)

	states := map[string]string{}
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT embedding_contract_id::text, search_state
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND source_id = ?::uuid
		`, teamID, decision.Relationship.RelationshipID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var contractID, state string
			if err := rows.Scan(&contractID, &state); err != nil {
				return err
			}
			states[contractID] = state
		}
		return rows.Err()
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", states[oldContractID])
	assert.Equal(t, "not_required", states[activeContractID])
}
