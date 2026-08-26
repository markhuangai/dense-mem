package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRememberTerminalCommitHasNoRetiredWorkflowTables(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-direct-terminal-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-direct-terminal-owner")
	ledger := NewLedgerRepository(appDB, rls)
	input := SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString(),
		SpaceID: "", SpaceGeneration: 0, IdempotencyKey: "direct-terminal-key", RequestHash: "direct-terminal-hash",
		SourceSummary: "direct terminal test", Proposal: map[string]any{"relationship_hints": []any{}},
		Evidence: []EvidenceInput{{FragmentID: uuid.NewString(), Content: "The direct Remember request was rejected.", ContentHash: sha256Hex("The direct Remember request was rejected.")}},
		Commit:   CommitSubmissionAssessmentInput{RelationshipResults: []SubmissionRelationshipResultInput{{RelationshipRef: "ref-1", Disposition: "not_stored", Reason: "not_supported_by_evidence"}}},
	}
	result, err := ledger.CommitRememberTerminal(ctx, input, "rejected", "no_supported_memory", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "rejected", result.Outcome)

	var retiredTables int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT COUNT(*) FROM pg_class WHERE relname IN ('placement_runs', 'placement_items')`).Scan(&retiredTables).Error
	}))
	require.Zero(t, retiredTables)
	var outcome string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT outcome FROM remember_attempts WHERE team_id = ?::uuid AND attempt_id = ?::uuid`, teamID, input.IngestID).Scan(&outcome).Error
	}))
	require.Equal(t, "rejected", outcome)
}

func TestRememberPreflightQuarantineWritesOnlyTerminalAttempt(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-preflight-quarantine-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-preflight-quarantine-owner")
	ledger := NewLedgerRepository(appDB, rls)
	input := SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString(),
		IdempotencyKey: "preflight-quarantine-key", RequestHash: "preflight-quarantine-hash",
		Evidence: []EvidenceInput{{
			FragmentID: uuid.NewString(), Content: "rejected hostile input",
			ContentHash: sha256Hex("rejected hostile input"),
		}},
		Proposal: map[string]any{"relationship_hints": []any{
			map[string]any{"ref": "hostile-first"},
			map[string]any{"ref": "hostile-second"},
		}},
		Commit: CommitSubmissionAssessmentInput{RelationshipResults: []SubmissionRelationshipResultInput{}},
	}
	result, err := ledger.CommitRememberPreflightQuarantine(ctx, input, "submission_quarantined")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "quarantined", result.Outcome)
	require.Len(t, result.PublicResult["relationship_results"], 2)

	var canonicalRows int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT
				(SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM semantic_assessments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM search_documents WHERE team_id = ?::uuid)
		`, teamID, teamID, teamID, teamID).Scan(&canonicalRows).Error
	}))
	require.Zero(t, canonicalRows)

	var outcome string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT outcome FROM remember_attempts
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid
		`, teamID, input.IngestID).Scan(&outcome).Error
	}))
	require.Equal(t, "quarantined", outcome)
}

func TestRememberFailureCannotFollowCanonicalTerminalAttempt(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-canonical-winner-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-canonical-winner-owner")
	ledger := NewLedgerRepository(appDB, rls)
	attemptID := uuid.NewString()
	input := SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: attemptID,
		IdempotencyKey: "canonical-winner-key", RequestHash: "canonical-winner-hash",
		Proposal: map[string]any{"relationship_hints": []any{}},
		Evidence: []EvidenceInput{{
			FragmentID: uuid.NewString(), Content: "canonical terminal result wins",
			ContentHash: sha256Hex("canonical terminal result wins"),
		}},
		Commit: CommitSubmissionAssessmentInput{RelationshipResults: []SubmissionRelationshipResultInput{}},
	}
	_, err := ledger.CommitRememberTerminal(ctx, input, "rejected", "no_supported_memory", nil)
	require.NoError(t, err)

	err = ledger.RecordRememberFailure(ctx, RememberFailureRecordInput{
		TeamID: teamID, OwnerProfileID: ownerID, AttemptID: uuid.NewString(),
		IdempotencyKey: input.IdempotencyKey, RequestHash: input.RequestHash,
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		FailedPhase: "embedding", ErrorCode: "embedding_unavailable",
		PublicResult: map[string]any{"processing_state": "failed"},
	})
	require.ErrorIs(t, err, ErrRememberReplay)
	err = ledger.RecordRememberFailure(ctx, RememberFailureRecordInput{
		TeamID: teamID, OwnerProfileID: ownerID, AttemptID: uuid.NewString(),
		IdempotencyKey: input.IdempotencyKey, RequestHash: "different-request-hash",
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		FailedPhase: "assessment", ErrorCode: "provider_unavailable",
		PublicResult: map[string]any{"processing_state": "failed"},
	})
	require.ErrorIs(t, err, ErrIdempotencyConflict)

	var failedRows int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*) FROM remember_attempts
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
			  AND idempotency_key = ? AND outcome = 'failed'
		`, teamID, ownerID, input.IdempotencyKey).Scan(&failedRows).Error
	}))
	require.Zero(t, failedRows)

	firstFailureID := uuid.NewString()
	require.NoError(t, ledger.RecordRememberFailure(ctx, RememberFailureRecordInput{
		TeamID: teamID, OwnerProfileID: ownerID, AttemptID: firstFailureID,
		IdempotencyKey: "failed-before-canonical", RequestHash: "failed-before-canonical-hash",
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		FailedPhase: "embedding", ErrorCode: "embedding_unavailable",
		PublicResult: map[string]any{"processing_state": "failed"},
	}))
	err = ledger.RecordRememberFailure(ctx, RememberFailureRecordInput{
		TeamID: teamID, OwnerProfileID: ownerID, AttemptID: uuid.NewString(),
		IdempotencyKey: "failed-before-canonical", RequestHash: "failed-before-canonical-hash",
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		FailedPhase: "assessment", ErrorCode: "provider_unavailable",
		PublicResult: map[string]any{"processing_state": "failed"},
	})
	require.ErrorIs(t, err, ErrRememberReplay)
	err = ledger.RecordRememberFailure(ctx, RememberFailureRecordInput{
		TeamID: teamID, OwnerProfileID: ownerID, AttemptID: uuid.NewString(),
		IdempotencyKey: "failed-before-canonical", RequestHash: "different-failed-request-hash",
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		FailedPhase: "assessment", ErrorCode: "provider_unavailable",
		PublicResult: map[string]any{"processing_state": "failed"},
	})
	require.ErrorIs(t, err, ErrIdempotencyConflict)
	secondInput := input
	secondInput.IngestID = uuid.NewString()
	secondInput.IdempotencyKey = "failed-before-canonical"
	secondInput.RequestHash = "failed-before-canonical-hash"
	secondInput.Evidence = []EvidenceInput{{
		FragmentID: uuid.NewString(), Content: "canonical terminal result wins",
		ContentHash: sha256Hex("canonical terminal result wins"),
	}}
	differentTerminal := secondInput
	differentTerminal.IngestID = uuid.NewString()
	differentTerminal.RequestHash = "different-terminal-request-hash"
	_, err = ledger.CommitRememberTerminal(ctx, differentTerminal, "rejected", "no_supported_memory", nil)
	require.ErrorIs(t, err, ErrIdempotencyConflict)
	_, err = ledger.CommitRememberTerminal(ctx, secondInput, "rejected", "no_supported_memory", nil)
	require.NoError(t, err)
	winner, err := ledger.LoadRememberAttempt(ctx, RememberAttemptLookupInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: secondInput.IdempotencyKey,
	})
	require.NoError(t, err)
	require.Equal(t, "rejected", winner.Outcome)
	require.Equal(t, secondInput.IngestID, winner.AttemptID)
}

func TestRememberTerminalCommitPreservesAcceptedSourceAndSupersessionTargets(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-terminal-preserve-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-terminal-preserve-owner")
	insertSearchTestContract(t, adminDB, rls, "remember-terminal-preserve", 3, "exact", "")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)

	var sharedSpaceID string
	var sharedSpaceGeneration int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT id::text, generation
			FROM memory_spaces
			WHERE team_id = ?::uuid AND kind = 'team_shared'
		`, teamID).Row().Scan(&sharedSpaceID, &sharedSpaceGeneration)
	}))

	acceptedContent := "Jamie works on Dense-Mem."
	accepted, err := createTestIngest(ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: sharedSpaceID, SpaceGeneration: sharedSpaceGeneration,
		IdempotencyKey: "terminal-preserve-accepted", RequestHash: "terminal-preserve-accepted-hash",
		Evidence: []EvidenceInput{{
			Content: acceptedContent, ContentHash: sha256Hex(acceptedContent), SourceType: "document", Authority: "primary",
			SourceKey: "doc://terminal-preserve", SourceRevisionToken: "rev-1", SourceRevisionContentHash: sha256Hex(acceptedContent),
		}},
	})
	require.NoError(t, err)
	require.Len(t, accepted.Evidence, 1)
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, search, teamID, ownerID, accepted.Evidence[0])

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Jamie")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: accepted.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: accepted.Evidence[0].FragmentID, SourceGroupKey: "doc://terminal-preserve",
			SourceID: accepted.Evidence[0].SourceID, SourceRevisionID: accepted.Evidence[0].SourceRevisionID,
			SpanStart: 0, SpanEnd: len(acceptedContent), Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)

	type semanticState struct {
		SourceRevisionID   string
		SourceRevision     string
		RelationshipStatus string
		SupportCount       int
		SupportEvents      int64
		EvidenceSearch     string
		LifecycleEvents    int64
	}
	loadState := func() semanticState {
		state := semanticState{}
		require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			if err := tx.Raw(`
				SELECT COALESCE(current_revision_id::text, ''), current_revision_token
				FROM evidence_sources
				WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND source_key = ?
			`, teamID, ownerID, "doc://terminal-preserve").Row().Scan(&state.SourceRevisionID, &state.SourceRevision); err != nil {
				return err
			}
			if err := tx.Raw(`
				SELECT status, support_count
				FROM relationship_records
				WHERE team_id = ?::uuid AND relationship_id = ?::uuid
			`, teamID, decision.Relationship.RelationshipID).Row().Scan(&state.RelationshipStatus, &state.SupportCount); err != nil {
				return err
			}
			if err := tx.Raw(`
				SELECT count(*)
				FROM relationship_support_decision_events
				WHERE team_id = ?::uuid AND relationship_id = ?::uuid
			`, teamID, decision.Relationship.RelationshipID).Row().Scan(&state.SupportEvents); err != nil {
				return err
			}
			if err := tx.Raw(`
				SELECT search_state
				FROM search_documents
				WHERE team_id = ?::uuid AND source_kind = 'evidence' AND source_id = ?::uuid
				ORDER BY search_document_id ASC
				LIMIT 1
			`, teamID, accepted.Evidence[0].FragmentID).Row().Scan(&state.EvidenceSearch); err != nil {
				return err
			}
			return tx.Raw(`
				SELECT count(*)
				FROM evidence_lifecycle_events
				WHERE team_id = ?::uuid AND target_fragment_id = ?::uuid
			`, teamID, accepted.Evidence[0].FragmentID).Row().Scan(&state.LifecycleEvents)
		}))
		return state
	}
	before := loadState()
	require.Equal(t, "rev-1", before.SourceRevision)
	require.Equal(t, "active", before.RelationshipStatus)
	require.Equal(t, 1, before.SupportCount)
	require.Equal(t, "pending", before.EvidenceSearch)
	require.Zero(t, before.LifecycleEvents)

	newRevisionContent := "Jamie no longer works on Dense-Mem."
	terminalInput := SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString(),
		SpaceID: sharedSpaceID, SpaceGeneration: sharedSpaceGeneration,
		IdempotencyKey: "terminal-preserve-rejected", RequestHash: "terminal-preserve-rejected-hash",
		Proposal: map[string]any{"relationship_hints": []any{}},
		Evidence: []EvidenceInput{
			{
				FragmentID: uuid.NewString(), Content: newRevisionContent, ContentHash: sha256Hex(newRevisionContent),
				SourceType: "document", Authority: "primary", SourceKey: "doc://terminal-preserve",
				SourceRevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1", SourceRevisionContentHash: sha256Hex(newRevisionContent),
			},
			{
				FragmentID: uuid.NewString(), Content: "The rejected update must not supersede accepted evidence.",
				ContentHash:           sha256Hex("The rejected update must not supersede accepted evidence."),
				SupersedesEvidenceIDs: []string{accepted.Evidence[0].FragmentID}, IdempotencyKey: "terminal-preserve-supersession",
			},
		},
		Commit: CommitSubmissionAssessmentInput{RelationshipResults: []SubmissionRelationshipResultInput{}},
	}
	result, err := ledger.CommitRememberTerminal(ctx, terminalInput, "rejected", "no_supported_memory", nil)
	require.NoError(t, err)
	require.Equal(t, "rejected", result.Outcome)

	after := loadState()
	require.Equal(t, before, after)

	var ingestStatus string
	var terminalEvidenceSourceReferences int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status
			FROM knowledge_ingests
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, terminalInput.IngestID).Row().Scan(&ingestStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)
			FROM evidence_fragments
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
			  AND (source_id IS NOT NULL OR source_revision_id IS NOT NULL)
		`, teamID, terminalInput.IngestID).Row().Scan(&terminalEvidenceSourceReferences)
	}))
	require.Equal(t, "rejected", ingestStatus)
	require.Zero(t, terminalEvidenceSourceReferences)
}

func TestRememberTerminalCommitMapsStaleSourceRevisionWithoutCanonicalRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-stale-source-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-stale-source-owner")
	ledger := NewLedgerRepository(appDB, rls)

	_, err := ledger.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: "doc://remember-stale",
		SourceKind: "document", Authority: "primary", RevisionToken: "rev-2",
		ContentHash: "sha256:current",
	})
	require.NoError(t, err)
	var sharedSpaceID string
	var sharedGeneration int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT id::text, generation
			FROM memory_spaces
			WHERE team_id = ?::uuid AND kind = 'team_shared'
		`, teamID).Row().Scan(&sharedSpaceID, &sharedGeneration)
	}))

	input := SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString(),
		SpaceID: sharedSpaceID, SpaceGeneration: sharedGeneration,
		IdempotencyKey: "remember-stale-source", RequestHash: "remember-stale-source-hash",
		Proposal: map[string]any{"relationship_hints": []any{}},
		Evidence: []EvidenceInput{{
			FragmentID: uuid.NewString(), Content: "stale source evidence",
			ContentHash: sha256Hex("stale source evidence"), SourceType: "document", Authority: "primary",
			SourceKey: "doc://remember-stale", SourceRevisionToken: "rev-3",
			ExpectedPreviousRevisionToken: "rev-1", SourceRevisionContentHash: "sha256:stale",
		}},
		Commit: CommitSubmissionAssessmentInput{RelationshipResults: []SubmissionRelationshipResultInput{}},
	}
	_, err = ledger.CommitRememberTerminal(ctx, input, "rejected", "no_supported_memory", nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSourceRevisionConflict), "err=%v", err)

	var canonicalRows int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*) FROM knowledge_ingests
			WHERE team_id = ?::uuid AND idempotency_key = ?
		`, teamID, input.IdempotencyKey).Scan(&canonicalRows).Error
	}))
	require.Zero(t, canonicalRows)
}

func TestTestIngestRejectsMismatchedRequestHash(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-legacy-hash-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-legacy-hash-owner")
	ledger := NewLedgerRepository(appDB, rls)

	first, err := createTestIngest(ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "remember-legacy-hash",
		RequestHash: "dense-mem.v2.6-hash", Evidence: []EvidenceInput{{Content: "Legacy hash remains replayable."}},
	})
	require.NoError(t, err)

	_, err = createTestIngest(ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "remember-legacy-hash",
		RequestHash: "dense-mem.v2.6.1-hash",
		Evidence:    []EvidenceInput{{Content: "Legacy hash remains replayable."}},
	})
	require.ErrorIs(t, err, ErrIdempotencyConflict)
	require.NotNil(t, first)
}
