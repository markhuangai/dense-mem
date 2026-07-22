package repository

import (
	"context"
	"database/sql"
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

func TestV2PlacementSemanticCommitRequeuesOpenMultiItemRun(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-multi-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-multi-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)
	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Alex")
	ingest, err := ledgerRepo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []V2EvidenceInput{
			{Content: "Alex works on Dense-Mem."},
			{Content: "Alex uses PostgreSQL."},
		},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Items, 2)
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-first", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	_, err = ledgerRepo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-first",
		ExpectedAttempts: claimed.Attempts,
		EntityResolutions: []V2PlacementEntityResolutionInput{
			{MentionRef: "subject", Action: "reuse", EntityID: subject.EntityID},
			{MentionRef: "object", Action: "create", EntityKind: "project", CanonicalName: "Dense-Mem"},
		},
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{{
			Ref:          "rel-first",
			SubjectRef:   "subject",
			PredicateKey: "works_on",
			ObjectRef:    "object",
			Support: &V2EvidenceSupportInput{
				FragmentID:     ingest.Evidence[0].FragmentID,
				SourceGroupKey: "conversation:multi-item",
				SpanStart:      0,
				SpanEnd:        len("Alex works on Dense-Mem."),
				Quote:          "Alex works on Dense-Mem.",
				Authority:      "primary",
			},
		}},
	})
	require.NoError(t, err)

	var runStatus, workerID, firstStatus, secondStatus string
	var runAttempts int
	var leaseCleared bool
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status, COALESCE(worker_id, ''), lease_until IS NULL, attempts
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&runStatus, &workerID, &leaseCleared, &runAttempts))
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Row().Scan(&firstStatus))
		return tx.Raw(`
			SELECT status
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[1].PlacementItemID).Row().Scan(&secondStatus)
	})
	require.NoError(t, err)
	assert.Equal(t, "queued", runStatus)
	assert.Empty(t, workerID)
	assert.True(t, leaseCleared)
	assert.Equal(t, 0, runAttempts)
	assert.Equal(t, "completed", firstStatus)
	assert.Equal(t, "queued", secondStatus)

	reclaimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-second", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	assert.Equal(t, ingest.PlacementRunID, reclaimed.PlacementRunID)
	assert.Equal(t, 1, reclaimed.Attempts)
}

func TestV2PlacementSemanticCommitAppendsCorrectionTargetCrossReference(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-correction-target-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "correction-target-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "correction-target-owner-b")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	neo4j := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Neo4j")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")

	targetIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerA,
		"correction target neo4j", "Dense-Mem uses Neo4j.")
	targetClaim, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-target", time.Minute)
	require.NoError(t, err)
	require.Equal(t, targetIngest.PlacementRunID, targetClaim.PlacementRunID)
	targetCommitted, err := ledgerRepo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerA,
		IngestID:         targetIngest.IngestID,
		PlacementRunID:   targetIngest.PlacementRunID,
		PlacementItemID:  targetIngest.Items[0].PlacementItemID,
		WorkerID:         "worker-target",
		ExpectedAttempts: targetClaim.Attempts,
		EntityResolutions: []V2PlacementEntityResolutionInput{
			{
				MentionRef: "subject",
				Action:     "reuse",
				EntityID:   denseMem.EntityID,
			},
			{
				MentionRef: "object",
				Action:     "reuse",
				EntityID:   neo4j.EntityID,
			},
		},
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{{
			Ref:          "target-uses",
			SubjectRef:   "subject",
			PredicateKey: "uses",
			ObjectRef:    "object",
			Support: &V2EvidenceSupportInput{
				FragmentID:     targetIngest.Evidence[0].FragmentID,
				SourceGroupKey: "conversation:correction-target",
				SpanStart:      0,
				SpanEnd:        len("Dense-Mem uses Neo4j."),
				Quote:          "Dense-Mem uses Neo4j.",
				Authority:      "primary",
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, targetCommitted.RelationshipResults, 1)
	target := targetCommitted.RelationshipResults[0].Relationship
	require.NotNil(t, target)

	sourceIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerB,
		"correction target postgres", "Dense-Mem uses PostgreSQL.")
	sourceClaim, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-source", time.Minute)
	require.NoError(t, err)
	require.Equal(t, sourceIngest.PlacementRunID, sourceClaim.PlacementRunID)
	sourceCommitted, err := ledgerRepo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerB,
		IngestID:         sourceIngest.IngestID,
		PlacementRunID:   sourceIngest.PlacementRunID,
		PlacementItemID:  sourceIngest.Items[0].PlacementItemID,
		WorkerID:         "worker-source",
		ExpectedAttempts: sourceClaim.Attempts,
		EntityResolutions: []V2PlacementEntityResolutionInput{
			{
				MentionRef: "subject",
				Action:     "reuse",
				EntityID:   denseMem.EntityID,
			},
			{
				MentionRef: "object",
				Action:     "reuse",
				EntityID:   postgres.EntityID,
			},
		},
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{{
			Ref:          "source-uses",
			SubjectRef:   "subject",
			PredicateKey: "uses",
			ObjectRef:    "object",
			CorrectionTarget: &V2PlacementCorrectionTargetInput{
				RelationshipID:  target.RelationshipID,
				ExpectedVersion: target.Version,
			},
			Support: &V2EvidenceSupportInput{
				FragmentID:     sourceIngest.Evidence[0].FragmentID,
				SourceGroupKey: "conversation:correction-source",
				SpanStart:      0,
				SpanEnd:        len("Dense-Mem uses PostgreSQL."),
				Quote:          "Dense-Mem uses PostgreSQL.",
				Authority:      "primary",
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, sourceCommitted.RelationshipResults, 1)
	source := sourceCommitted.RelationshipResults[0].Relationship
	require.NotNil(t, source)

	var kind, sourceRelationshipID, targetRelationshipID, targetStatus string
	var sourceVersion, targetVersion int
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerB, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT kind, source_relationship_id::text, source_relationship_version,
			       target_relationship_id::text, target_relationship_version
			FROM relationship_cross_references
			WHERE team_id = ?::uuid
			  AND author_profile_id = ?::uuid
		`, teamID, ownerB).Row().Scan(&kind, &sourceRelationshipID, &sourceVersion, &targetRelationshipID, &targetVersion))
		return tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, target.RelationshipID).Scan(&targetStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "corrects", kind)
	assert.Equal(t, source.RelationshipID, sourceRelationshipID)
	assert.Equal(t, source.Version, sourceVersion)
	assert.Equal(t, target.RelationshipID, targetRelationshipID)
	assert.Equal(t, target.Version, targetVersion)
	assert.Equal(t, "active", targetStatus)
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
	require.NoError(t, adminDB.Exec(`UPDATE search_index_generations SET activation_state = 'retired'`).Error)

	_, err = ledgerRepo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-rollback",
		ExpectedAttempts: claimed.Attempts,
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
	assert.True(t, errors.Is(err, ErrV2SearchContractMismatch), err)

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
	var evidenceSearchDocumentCount int64
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
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'evidence'
		`, teamID).Scan(&evidenceSearchDocumentCount).Error)
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
	assert.Equal(t, int64(1), evidenceSearchDocumentCount)

	searchRepo := NewV2SearchRepository(appDB, rls)
	recall, err := searchRepo.RecallEvidence(ctx, V2RecallEvidenceInput{
		TeamID: teamID,
		Query:  "Ambiguous evidence",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, recall.Results, 1)
	assert.Equal(t, ingest.Evidence[0].FragmentID, recall.Results[0].EvidenceID)
	assert.Empty(t, recall.Results[0].RelationshipIDs)

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

func TestV2PlacementReviewRetryableRequeuesRunWithoutResettingAttempts(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-review-retryable-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "retryable-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)

	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement retryable review", "Provider timeout should requeue this evidence.")
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-retryable", time.Minute)
	require.NoError(t, err)

	requeued, err := ledgerRepo.RequeuePlacementReviewResult(ctx, V2RequeuePlacementReviewInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-retryable",
		ExpectedAttempts: claimed.Attempts,
	})
	require.NoError(t, err)
	require.Equal(t, "retryable", requeued.Status)

	var runStatus, workerID, itemStatus string
	var attempts int
	var leaseUntil sql.NullTime
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status, attempts, worker_id, lease_until
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&runStatus, &attempts, &workerID, &leaseUntil))
		return tx.Raw(`
			SELECT status
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Row().Scan(&itemStatus)
	})
	require.NoError(t, err)
	assert.Equal(t, "queued", runStatus)
	assert.Equal(t, claimed.Attempts, attempts)
	assert.Empty(t, workerID)
	assert.False(t, leaseUntil.Valid)
	assert.Equal(t, "queued", itemStatus)

	reclaimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-retryable-2", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	assert.Equal(t, ingest.PlacementRunID, reclaimed.PlacementRunID)
	assert.Equal(t, claimed.Attempts+1, reclaimed.Attempts)
}

func TestV2PlacementReviewRetrySupersedesStaleSource(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-review-stale-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "review-stale-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)

	ingest, err := ledgerRepo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "placement review stale source",
		Evidence: []V2EvidenceInput{{
			Content:                   "Retry should not revive stale evidence.",
			SourceType:                "document",
			SourceKey:                 "doc://placement-review-stale",
			SourceRevisionToken:       "rev-1",
			SourceRevisionContentHash: "sha256:rev1",
		}},
	})
	require.NoError(t, err)
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-review-stale", time.Minute)
	require.NoError(t, err)
	_, err = ledgerRepo.AdvanceSourceRevision(ctx, V2AdvanceSourceRevisionInput{
		TeamID:                        teamID,
		OwnerProfileID:                ownerID,
		SourceKey:                     "doc://placement-review-stale",
		SourceKind:                    "document",
		RevisionToken:                 "rev-2",
		ExpectedPreviousRevisionToken: "rev-1",
		ContentHash:                   "sha256:rev2",
	})
	require.NoError(t, err)

	requeued, err := ledgerRepo.RequeuePlacementReviewResult(ctx, V2RequeuePlacementReviewInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-review-stale",
		ExpectedAttempts: claimed.Attempts,
	})
	require.NoError(t, err)
	require.Equal(t, "superseded", requeued.Status)
	require.NotEmpty(t, requeued.OutcomeID)

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
		`, teamID, requeued.OutcomeID).Scan(&outcomeStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "failed", runStatus)
	assert.Equal(t, "failed", itemStatus)
	assert.Equal(t, "failed", itemCategory)
	assert.Equal(t, "superseded", outcomeStatus)
}
