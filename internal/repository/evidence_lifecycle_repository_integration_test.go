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

func TestLedgerEvidenceLifecyclePersistsTargetSpaceAndRejectsMixedSpaces(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-lifecycle-space")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-lifecycle-space-owner")
	ledger := NewLedgerRepository(appDB, rls)
	sharedTarget := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "lifecycle-shared", "Shared lifecycle evidence.")
	privateIngestID := uuid.New()
	privateFragmentID := uuid.New()
	var privateSpaceID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			INSERT INTO memory_spaces (team_id, kind, owner_profile_id)
			VALUES (?, 'profile_private', ?)
			RETURNING id::text
		`, teamID, ownerID).Row().Scan(&privateSpaceID); err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Exec(`
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, request_hash, source_summary,
				status, proposal, metadata, space_id, created_at, updated_at
			) VALUES (?, ?, ?, ?, 'Private lifecycle evidence.', 'queued', '{}'::jsonb, '{}'::jsonb, ?, ?, ?)
		`, teamID, privateIngestID, ownerID, "hash-"+privateIngestID.String(), privateSpaceID, now, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				content, content_hash, source_type, authority, labels, metadata, space_id
			) VALUES (?, ?, ?, ?, 0, 'Private lifecycle evidence.', ?, 'conversation', 'primary', ARRAY[]::text[], '{}'::jsonb, ?)
		`, teamID, privateFragmentID, privateIngestID, ownerID, "hash-"+privateFragmentID.String(), privateSpaceID).Error
	}))

	_, err := ledger.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID,
		EvidenceIDs: []string{privateFragmentID.String(), sharedTarget.Evidence[0].FragmentID},
		Reason:      "mixed-space lifecycle test", IdempotencyKey: "mixed-space-lifecycle", RequestHash: "mixed-space-lifecycle-hash",
	})
	require.ErrorIs(t, err, ErrEvidenceLifecycleConflict)

	result, err := ledger.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID,
		EvidenceIDs: []string{privateFragmentID.String()},
		Reason:      "private lifecycle test", IdempotencyKey: "private-space-lifecycle", RequestHash: "private-space-lifecycle-hash",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.DecisionID)

	var operationSpaceID, eventSpaceID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT space_id::text
			FROM evidence_lifecycle_operations
			WHERE team_id = ? AND lifecycle_operation_id = ?::uuid
		`, teamID, result.DecisionID).Row().Scan(&operationSpaceID); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT space_id::text
			FROM evidence_lifecycle_events
			WHERE team_id = ? AND lifecycle_operation_id = ?::uuid
		`, teamID, result.DecisionID).Row().Scan(&eventSpaceID)
	}))
	require.Equal(t, privateSpaceID, operationSpaceID)
	require.Equal(t, privateSpaceID, eventSpaceID)
}

func TestLedgerRetractEvidenceRevokesOnlyItsSupportAndReplaysAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-retraction-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-retraction-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-retraction-other-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Jamie")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	firstIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "retract-first", "Jamie works on Dense-Mem.")
	first := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "source:first",
			SpanStart:      0,
			SpanEnd:        len("Jamie works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	secondIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "retract-second", "Another source confirms Jamie works on Dense-Mem.")
	second := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        secondIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     secondIngest.Evidence[0].FragmentID,
			SourceGroupKey: "source:second",
			SpanStart:      0,
			SpanEnd:        len("Another source confirms Jamie works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	require.Equal(t, first.Relationship.RelationshipID, second.Relationship.RelationshipID)
	require.Equal(t, 2, second.Relationship.SupportCount)
	firstRetraction, err := ledger.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EvidenceIDs:    []string{firstIngest.Evidence[0].FragmentID},
		Reason:         "source was entered in error",
		IdempotencyKey: "retract-first-evidence",
		RequestHash:    "sha256:retract-first",
	})
	require.NoError(t, err)
	assert.Equal(t, evidenceLifecycleCompleted, firstRetraction.ProcessingState)
	assert.Equal(t, []string{firstIngest.Evidence[0].FragmentID}, firstRetraction.RetractedEvidenceIDs)
	assert.Equal(t, 1, firstRetraction.AffectedRelationshipCount)
	assert.Equal(t, 0, firstRetraction.PendingRelationshipCount)
	assert.Equal(t, 1, firstRetraction.RetainedActiveRelationshipCount)

	trace, err := semantic.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID:         teamID,
		RelationshipID: first.Relationship.RelationshipID,
		MaxEvents:      20,
	})
	require.NoError(t, err)
	assert.Equal(t, "active", trace.Relationship.Status)
	assert.Equal(t, 1, trace.Relationship.SupportCount)

	replay, err := ledger.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EvidenceIDs:    []string{firstIngest.Evidence[0].FragmentID},
		Reason:         "source was entered in error",
		IdempotencyKey: "retract-first-evidence",
		RequestHash:    "sha256:retract-first",
	})
	require.NoError(t, err)
	assert.True(t, replay.Existing)
	wantReplay := *firstRetraction
	wantReplay.Existing = true
	assert.Equal(t, &wantReplay, replay)

	_, err = ledger.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID:         teamID,
		OwnerProfileID: otherOwnerID,
		EvidenceIDs:    []string{secondIngest.Evidence[0].FragmentID},
		Reason:         "other profile cannot retract",
		IdempotencyKey: "cross-owner-retract",
		RequestHash:    "sha256:cross-owner",
	})
	require.ErrorIs(t, err, ErrEvidenceLifecycleNotFound)

	secondRetraction, err := ledger.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EvidenceIDs:    []string{secondIngest.Evidence[0].FragmentID},
		Reason:         "remaining support was withdrawn",
		IdempotencyKey: "retract-second-evidence",
		RequestHash:    "sha256:retract-second",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, secondRetraction.PendingRelationshipCount)
	assert.Equal(t, 0, secondRetraction.RetainedActiveRelationshipCount)

	trace, err = semantic.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID:         teamID,
		RelationshipID: first.Relationship.RelationshipID,
		MaxEvents:      20,
	})
	require.NoError(t, err)
	assert.Equal(t, "pending_evidence", trace.Relationship.Status)
	assert.Equal(t, 0, trace.Relationship.SupportCount)

	var lifecycleEvents int
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM evidence_lifecycle_events
			WHERE team_id = ?::uuid
		`, teamID).Scan(&lifecycleEvents).Error
	})
	require.NoError(t, err)
	assert.Equal(t, 2, lifecycleEvents)
}

func TestLedgerDirectEvidenceSupersessionRetiresTargetWhenReplacementIsQuarantined(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-supersession-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-supersession-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Jamie")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	original := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "supersession-original", "Jamie works on Dense-Mem.")
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        original.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     original.Evidence[0].FragmentID,
			SourceGroupKey: "source:original",
			SpanStart:      0,
			SpanEnd:        len("Jamie works on Dense-Mem."),
			Authority:      "primary",
		},
	})

	replacementInput := CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "supersession-replacement-ingest",
		RequestHash:    "sha256:supersession-replacement",
		Evidence: []EvidenceInput{{
			Content:               "Jamie no longer works on Dense-Mem.",
			SupersedesEvidenceIDs: []string{original.Evidence[0].FragmentID},
			IdempotencyKey:        "supersession-replacement-evidence",
			InitialEvent: &SecurityEventDraft{
				EventKind: "deterministic_scan",
				Decision:  "quarantine",
				Reason:    "needs security review",
			},
		}},
	}
	replacement, err := ledger.CreateIngest(ctx, replacementInput)
	require.NoError(t, err)
	require.Len(t, replacement.Evidence, 1)
	loaded, err := ledger.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       replacement.IngestID,
	})
	require.NoError(t, err)
	require.Len(t, loaded.Evidence, 1)
	assert.Equal(t, []string{original.Evidence[0].FragmentID}, loaded.Evidence[0].SupersededEvidenceIDs)

	trace, err := semantic.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID:         teamID,
		RelationshipID: decision.Relationship.RelationshipID,
		MaxEvents:      20,
	})
	require.NoError(t, err)
	assert.Equal(t, "pending_evidence", trace.Relationship.Status)
	assert.Equal(t, 0, trace.Relationship.SupportCount)
	require.Len(t, trace.EvidenceLifecycleEvents, 1)
	assert.Equal(t, original.Evidence[0].FragmentID, trace.EvidenceLifecycleEvents[0].TargetFragmentID)
	assert.Equal(t, replacement.Evidence[0].FragmentID, trace.EvidenceLifecycleEvents[0].ReplacementFragmentID)
	assert.Equal(t, "supersede", trace.EvidenceLifecycleEvents[0].Action)

	var eventCount, quarantineCount int
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM evidence_lifecycle_events
			WHERE team_id = ?::uuid
			  AND target_fragment_id = ?::uuid
			  AND replacement_fragment_id = ?::uuid
		`, teamID, original.Evidence[0].FragmentID, replacement.Evidence[0].FragmentID).Scan(&eventCount).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*)
			FROM evidence_quarantines
			WHERE team_id = ?::uuid
			  AND fragment_id = ?::uuid
			  AND status = 'active'
		`, teamID, replacement.Evidence[0].FragmentID).Scan(&quarantineCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, 1, eventCount)
	assert.Equal(t, 1, quarantineCount)

	replay, err := ledger.CreateIngest(ctx, replacementInput)
	require.NoError(t, err)
	assert.True(t, replay.Existing)
	assert.Equal(t, replacement.IngestID, replay.IngestID)
}

func TestTraceRelationshipMarksLifecycleEventLimitAsTruncated(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-lifecycle-trace-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-lifecycle-trace-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	targetEvidenceIDs := make([]string, 0, 3)
	for index, content := range []string{
		"Prior evidence one.",
		"Prior evidence two.",
		"Prior evidence three.",
	} {
		target := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "trace-lifecycle-target-"+string(rune('a'+index)), content)
		targetEvidenceIDs = append(targetEvidenceIDs, target.Evidence[0].FragmentID)
	}
	replacement, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "trace-lifecycle-replacement-ingest",
		RequestHash:    "sha256:trace-lifecycle-replacement",
		Evidence: []EvidenceInput{{
			Content:               "The corrected evidence is current.",
			SupersedesEvidenceIDs: targetEvidenceIDs,
			IdempotencyKey:        "trace-lifecycle-replacement-evidence",
		}},
	})
	require.NoError(t, err)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Jamie")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        replacement.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     replacement.Evidence[0].FragmentID,
			SourceGroupKey: "source:replacement",
			SpanStart:      0,
			SpanEnd:        len("The corrected evidence is current."),
			Authority:      "primary",
		},
	})

	trace, err := semantic.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID:         teamID,
		RelationshipID: decision.Relationship.RelationshipID,
		MaxEvents:      2,
	})
	require.NoError(t, err)
	assert.Len(t, trace.Observations, 1)
	assert.Len(t, trace.EvidenceSupports, 1)
	assert.Len(t, trace.SupportDecisionEvents, 1)
	assert.Len(t, trace.EvidenceLifecycleEvents, 2)
	assert.True(t, trace.Truncated)
	assert.Equal(t, "max_events", trace.StoppedReason)
}
