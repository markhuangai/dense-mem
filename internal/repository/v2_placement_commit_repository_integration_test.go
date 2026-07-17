package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV2PlacementSemanticCommitWritesStateSearchAndOutcomeAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-commit-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Alex")
	evidenceText := "Alex works on Dense-Mem. Dense-Mem released on July 17, 2026."
	releaseQuote := "Dense-Mem released on July 17, 2026."
	releaseStart := strings.Index(evidenceText, releaseQuote)
	require.NotEqual(t, -1, releaseStart)
	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement semantic commit", evidenceText)
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-a", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	committed, err := ledgerRepo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-a",
		ExpectedAttempts: claimed.Attempts,
		EntityResolutions: []V2PlacementEntityResolutionInput{
			{
				MentionRef: "subject",
				Action:     "reuse",
				EntityID:   subject.EntityID,
			},
			{
				MentionRef:    "object",
				Action:        "create",
				EntityKind:    "project",
				CanonicalName: "Dense-Mem",
			},
		},
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{
			{
				Ref:          "rel-works-on",
				SubjectRef:   "subject",
				PredicateKey: "works_on",
				ObjectRef:    "object",
				Support: &V2EvidenceSupportInput{
					FragmentID:     ingest.Evidence[0].FragmentID,
					SourceGroupKey: "conversation:placement-commit",
					SpanStart:      0,
					SpanEnd:        len("Alex works on Dense-Mem."),
					Quote:          "Alex works on Dense-Mem.",
					Authority:      "primary",
				},
			},
			{
				Ref:          "rel-released",
				SubjectRef:   "object",
				PredicateKey: "released",
				ObjectValue: &V2PlacementValueInput{
					Ref:            "release-date",
					ValueType:      "date",
					CanonicalValue: "2026-07-17",
					Display:        "July 17, 2026",
				},
				Support: &V2EvidenceSupportInput{
					FragmentID:     ingest.Evidence[0].FragmentID,
					SourceGroupKey: "conversation:placement-commit",
					SpanStart:      releaseStart,
					SpanEnd:        releaseStart + len(releaseQuote),
					Quote:          releaseQuote,
					Authority:      "primary",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "accepted", committed.Status)
	require.NotEmpty(t, committed.OutcomeID)
	require.Len(t, committed.RelationshipResults, 2)
	require.Len(t, committed.SearchDocuments, 3)
	require.Len(t, committed.EntityResolutionIDs, 2)
	require.NotEmpty(t, committed.SearchDocuments[0].QueuedJobID)

	var runStatus, itemStatus, itemCategory string
	var relationshipCount, supportCount, relationshipSearchDocumentCount, evidenceSearchDocumentCount, embeddingJobCount, outcomeCount, valueCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Scan(&runStatus).Error)
		require.NoError(t, tx.Raw(`
			SELECT status, category
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Row().Scan(&itemStatus, &itemCategory))
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_records
			WHERE team_id = ?::uuid
		`, teamID).Scan(&relationshipCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid
		`, teamID).Scan(&supportCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
		`, teamID).Scan(&relationshipSearchDocumentCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'evidence'
		`, teamID).Scan(&evidenceSearchDocumentCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM embedding_jobs
			WHERE team_id = ?::uuid
		`, teamID).Scan(&embeddingJobCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM value_records
			WHERE team_id = ?::uuid
			  AND value_type = 'date'
		`, teamID).Scan(&valueCount).Error)
		return tx.Raw(`
			SELECT COUNT(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Scan(&outcomeCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "completed", runStatus)
	assert.Equal(t, "completed", itemStatus)
	assert.Equal(t, "validated_claim", itemCategory)
	assert.Equal(t, int64(2), relationshipCount)
	assert.Equal(t, int64(2), supportCount)
	assert.Equal(t, int64(2), relationshipSearchDocumentCount)
	assert.Equal(t, int64(1), evidenceSearchDocumentCount)
	assert.Equal(t, int64(3), embeddingJobCount)
	assert.Equal(t, int64(1), valueCount)
	assert.Equal(t, int64(1), outcomeCount)

	searchRepo := NewV2SearchRepository(appDB, rls)
	recall, err := searchRepo.RecallEvidence(ctx, V2RecallEvidenceInput{
		TeamID: teamID,
		Query:  "Dense-Mem",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, recall.Results, 1)
	assert.Equal(t, ingest.Evidence[0].FragmentID, recall.Results[0].EvidenceID)
	assert.ElementsMatch(t, []string{
		committed.RelationshipResults[0].Relationship.RelationshipID,
		committed.RelationshipResults[1].Relationship.RelationshipID,
	}, recall.Results[0].RelationshipIDs)
	assert.NotContains(t, recall.Results[0].Context, "candidate")

	known, err := searchRepo.RecallEvidence(ctx, V2RecallEvidenceInput{
		TeamID:               teamID,
		Query:                "Dense-Mem",
		Limit:                5,
		KnownRelationshipIDs: recall.Results[0].RelationshipIDs,
	})
	require.NoError(t, err)
	assert.Empty(t, known.Results)

	_, err = ledgerRepo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-a",
		ExpectedAttempts: claimed.Attempts,
		EntityResolutions: []V2PlacementEntityResolutionInput{
			{
				MentionRef: "subject",
				Action:     "reuse",
				EntityID:   subject.EntityID,
			},
			{
				MentionRef:    "object",
				Action:        "create",
				EntityKind:    "project",
				CanonicalName: "Dense-Mem",
			},
		},
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{{
			SubjectRef:   "subject",
			PredicateKey: "works_on",
			ObjectRef:    "object",
			Support: &V2EvidenceSupportInput{
				FragmentID:     ingest.Evidence[0].FragmentID,
				SourceGroupKey: "conversation:placement-commit",
				SpanStart:      0,
				SpanEnd:        len("Alex works on Dense-Mem."),
				Authority:      "primary",
			},
		}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2PlacementLeaseLost), err)
}

func TestV2PlacementSemanticCommitRollsBackSemanticWritesWhenSearchIntentFails(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-rollback-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "rollback-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Casey")
	object := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement semantic rollback", "Casey works on Dense-Mem.")
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-rollback", time.Minute)
	require.NoError(t, err)

	_, err = ledgerRepo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-rollback",
		ExpectedAttempts: claimed.Attempts,
		SearchProfileKey: "missing-search-profile",
		RelationshipDecisions: []V2ApplyRelationshipDecisionInput{{
			SubjectEntityID: subject.EntityID,
			PredicateKey:    "works_on",
			ObjectEntityID:  object.EntityID,
			Support: &V2EvidenceSupportInput{
				FragmentID:     ingest.Evidence[0].FragmentID,
				SourceGroupKey: "conversation:placement-rollback",
				SpanStart:      0,
				SpanEnd:        len("Casey works on Dense-Mem."),
				Quote:          "Casey works on Dense-Mem.",
				Authority:      "primary",
			},
		}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2SearchProfileMismatch), err)

	var relationshipCount, supportCount, searchDocumentCount, outcomeCount int64
	var runStatus, itemStatus string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Scan(&relationshipCount).Error)
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM relationship_evidence_supports WHERE team_id = ?::uuid`, teamID).Scan(&supportCount).Error)
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM search_documents WHERE team_id = ?::uuid`, teamID).Scan(&searchDocumentCount).Error)
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM placement_outcomes WHERE team_id = ?::uuid`, teamID).Scan(&outcomeCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Scan(&runStatus).Error)
		return tx.Raw(`
			SELECT status
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Scan(&itemStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), relationshipCount)
	assert.Equal(t, int64(0), supportCount)
	assert.Equal(t, int64(0), searchDocumentCount)
	assert.Equal(t, int64(0), outcomeCount)
	assert.Equal(t, "processing", runStatus)
	assert.Equal(t, "queued", itemStatus)
}

func TestV2PlacementSemanticCommitSupersedesStaleSourceWithoutSemanticRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-stale-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "stale-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Riley")
	object := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	ingest, err := ledgerRepo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "placement stale source",
		Evidence: []V2EvidenceInput{{
			Content:                   "Riley works on Dense-Mem.",
			SourceType:                "document",
			SourceKey:                 "doc://placement-stale",
			SourceRevisionToken:       "rev-1",
			SourceRevisionContentHash: "sha256:rev1",
		}},
	})
	require.NoError(t, err)
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-stale", time.Minute)
	require.NoError(t, err)
	_, err = ledgerRepo.AdvanceSourceRevision(ctx, V2AdvanceSourceRevisionInput{
		TeamID:                        teamID,
		OwnerProfileID:                ownerID,
		SourceKey:                     "doc://placement-stale",
		SourceKind:                    "document",
		RevisionToken:                 "rev-2",
		ExpectedPreviousRevisionToken: "rev-1",
		ContentHash:                   "sha256:rev2",
	})
	require.NoError(t, err)

	committed, err := ledgerRepo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-stale",
		ExpectedAttempts: claimed.Attempts,
		RelationshipDecisions: []V2ApplyRelationshipDecisionInput{{
			SubjectEntityID: subject.EntityID,
			PredicateKey:    "works_on",
			ObjectEntityID:  object.EntityID,
			Support: &V2EvidenceSupportInput{
				FragmentID:       ingest.Evidence[0].FragmentID,
				SourceGroupKey:   "document:placement-stale",
				SourceID:         ingest.Evidence[0].SourceID,
				SourceRevisionID: ingest.Evidence[0].SourceRevisionID,
				SpanStart:        0,
				SpanEnd:          len("Riley works on Dense-Mem."),
				Quote:            "Riley works on Dense-Mem.",
				Authority:        "primary",
			},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "superseded", committed.Status)
	assert.NotEmpty(t, committed.OutcomeID)

	var relationshipCount, outcomeCount int64
	var runStatus, itemStatus, itemCategory, outcomeStatus string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Scan(&relationshipCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Scan(&runStatus).Error)
		require.NoError(t, tx.Raw(`
			SELECT status, category
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Row().Scan(&itemStatus, &itemCategory))
		return tx.Raw(`
			SELECT COUNT(*), MAX(status)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&outcomeCount, &outcomeStatus)
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), relationshipCount)
	assert.Equal(t, "failed", runStatus)
	assert.Equal(t, "failed", itemStatus)
	assert.Equal(t, "failed", itemCategory)
	assert.Equal(t, int64(1), outcomeCount)
	assert.Equal(t, "superseded", outcomeStatus)
}

func TestV2PlacementReviewCompletionClosesNonAcceptedResultWithLeaseFence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-review-required-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "review-required-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)

	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement review required", "Ambiguous evidence needs review.")
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-review-required", time.Minute)
	require.NoError(t, err)

	completed, err := ledgerRepo.CompletePlacementReviewResult(ctx, V2CompletePlacementReviewInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-review-required",
		ExpectedAttempts: claimed.Attempts,
		Status:           "review_required",
		Payload: map[string]any{
			"reason": "ambiguous entity resolution",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "review_required", completed.Status)
	require.NotEmpty(t, completed.OutcomeID)

	var runStatus, itemStatus, itemCategory, outcomeStatus string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Scan(&runStatus).Error)
		require.NoError(t, tx.Raw(`
			SELECT status, category
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Row().Scan(&itemStatus, &itemCategory))
		return tx.Raw(`
			SELECT status
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND outcome_id = ?::uuid
		`, teamID, completed.OutcomeID).Scan(&outcomeStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "completed", runStatus)
	assert.Equal(t, "completed", itemStatus)
	assert.Equal(t, "candidate", itemCategory)
	assert.Equal(t, "review_required", outcomeStatus)

	_, err = ledgerRepo.CompletePlacementReviewResult(ctx, V2CompletePlacementReviewInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-review-required",
		ExpectedAttempts: claimed.Attempts,
		Status:           "review_required",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2PlacementLeaseLost), err)
}
