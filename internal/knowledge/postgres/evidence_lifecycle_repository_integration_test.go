//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEvidenceOnlyPostgresRetractionReplaysAndKeepsAuthorizedProvenance(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := t.Context()
	team := createLedgerTeam(t, admin, rls, "evidence-only-lifecycle")
	owner := createLedgerProfile(t, admin, rls, team, "owner A")
	peer := createLedgerProfile(t, admin, rls, team, "peer B")
	store := NewStore(app, rls)
	original := createSemanticIngest(t, ctx, store, team, owner, "original", "Exact original Evidence. Café 🧭")
	id := original.Evidence[0].FragmentID
	input := RetractEvidenceInput{TeamID: team, OwnerProfileID: owner, EvidenceIDs: []string{id}, Reason: "Withdraw original", IdempotencyKey: "withdraw", RequestHash: sha256Hex("withdraw")}
	foreign := input
	foreign.OwnerProfileID, foreign.IdempotencyKey = peer, "peer-withdraw"
	_, err := store.RetractEvidence(ctx, foreign)
	require.ErrorIs(t, err, ErrEvidenceLifecycleNotFound)
	type outcome struct {
		result *EvidenceLifecycleResult
		err    error
	}
	results, start := make(chan outcome, 2), make(chan struct{})
	for range 2 {
		go func() { <-start; result, err := store.RetractEvidence(ctx, input); results <- outcome{result, err} }()
	}
	close(start)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.Equal(t, first.result.DecisionID, second.result.DecisionID)
	require.NotEqual(t, first.result.Existing, second.result.Existing)
	first.result.Existing, second.result.Existing = false, false
	require.Equal(t, first.result, second.result)
	trace, err := tracepostgres.New(app, rls).TraceEvidence(ctx, tracecontract.Input{TeamID: team, EvidenceID: id})
	require.NoError(t, err)
	require.Equal(t, "Exact original Evidence. Café 🧭", trace.Evidence.Content)
	require.Len(t, trace.Lifecycle, 1)
	require.Equal(t, "retract", trace.Lifecycle[0].Action)
	require.Equal(t, id, trace.Lifecycle[0].TargetFragmentID)
	otherTeam := createLedgerTeam(t, admin, rls, "foreign lifecycle reader")
	_, err = tracepostgres.New(app, rls).TraceEvidence(ctx, tracecontract.Input{TeamID: otherTeam, EvidenceID: id})
	require.ErrorIs(t, err, tracecontract.ErrEvidenceNotFound)
	changed := input
	changed.Reason = "Changed request"
	changed.RequestHash = sha256Hex("changed")
	_, err = store.RetractEvidence(ctx, changed)
	require.ErrorIs(t, err, ErrIdempotencyConflict)
}

func TestEvidenceOnlyPostgresSupersessionRetainsLineageForQuarantinedReplacement(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := t.Context()
	team := createLedgerTeam(t, admin, rls, "evidence-only-supersession")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	store := NewStore(app, rls)
	original := createSemanticIngest(t, ctx, store, team, owner, "before", "Original exact Evidence.")
	input := CreateIngestInput{TeamID: team, OwnerProfileID: owner, IdempotencyKey: "replacement", RequestHash: sha256Hex("replacement"), Evidence: []EvidenceInput{{Content: "Exact replacement Evidence.", SupersedesEvidenceIDs: []string{original.Evidence[0].FragmentID}, InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "quarantine", Reason: "Needs security review"}}}}
	replacement, err := store.CreateIngestForTest(ctx, input)
	require.NoError(t, err)
	trace, err := tracepostgres.New(app, rls).TraceEvidence(ctx, tracecontract.Input{TeamID: team, EvidenceID: original.Evidence[0].FragmentID})
	require.NoError(t, err)
	require.Equal(t, "Original exact Evidence.", trace.Evidence.Content)
	require.Len(t, trace.Lifecycle, 1)
	require.Equal(t, "supersede", trace.Lifecycle[0].Action)
	require.Equal(t, replacement.Evidence[0].FragmentID, trace.Lifecycle[0].ReplacementFragmentID)
	var quarantined int64
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM evidence_quarantines WHERE team_id=?::uuid AND fragment_id=?::uuid AND status='active'`, team, replacement.Evidence[0].FragmentID).Scan(&quarantined).Error
	}))
	require.EqualValues(t, 1, quarantined)
	replayed, err := store.CreateIngestForTest(ctx, input)
	require.NoError(t, err)
	require.True(t, replayed.Existing)
	require.Equal(t, replacement.IngestID, replayed.IngestID)
}

func TestLedgerEvidenceLifecyclePersistsTargetSpaceAndRejectsMixedSpaces(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-lifecycle-space")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-lifecycle-space-owner")
	ledger := NewStore(appDB, rls)
	sharedTarget := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "lifecycle-shared", "Shared lifecycle evidence.")
	privateIngestID := uuid.New()
	privateFragmentID := uuid.New()
	var privateSpaceID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT space.id::text FROM memory_spaces space JOIN team_memberships membership ON membership.id=space.owner_membership_id JOIN ownership_aliases alias ON alias.team_id=membership.team_id AND alias.canonical_identity_id=membership.actor_identity_id WHERE space.team_id=? AND alias.legacy_owner_id=? AND space.kind='private' AND space.lifecycle_state='active'
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

func TestLedgerDirectEvidenceSupersessionRejectsCrossSpaceReplacement(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-supersession-cross-space")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-supersession-cross-space-owner")
	ledger := NewStore(appDB, rls)
	sharedTarget := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "cross-space-target", "Shared target evidence.")
	privateIngestID := uuid.New()
	privateFragmentID := uuid.New()
	var privateSpaceID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT space.id::text FROM memory_spaces space JOIN team_memberships membership ON membership.id=space.owner_membership_id JOIN ownership_aliases alias ON alias.team_id=membership.team_id AND alias.canonical_identity_id=membership.actor_identity_id WHERE space.team_id=? AND alias.legacy_owner_id=? AND space.kind='private' AND space.lifecycle_state='active'
		`, teamID, ownerID).Row().Scan(&privateSpaceID); err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Exec(`
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, request_hash, source_summary,
				status, proposal, metadata, space_id, created_at, updated_at
			) VALUES (?, ?, ?, ?, 'Private replacement evidence.', 'queued', '{}'::jsonb, '{}'::jsonb, ?, ?, ?)
		`, teamID, privateIngestID, ownerID, "hash-"+privateIngestID.String(), privateSpaceID, now, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				content, content_hash, source_type, authority, labels, metadata, space_id
			) VALUES (?, ?, ?, ?, 0, 'Private replacement evidence.', ?, 'conversation', 'primary', ARRAY[]::text[], '{}'::jsonb, ?)
		`, teamID, privateFragmentID, privateIngestID, ownerID, "hash-"+privateFragmentID.String(), privateSpaceID).Error
	}))

	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return applyDirectEvidenceSupersessions(ctx, tx, CreateIngestInput{
			TeamID:         teamID,
			OwnerProfileID: ownerID,
			RequestHash:    "cross-space-request",
			Evidence: []EvidenceInput{{
				Content:               "Private replacement evidence.",
				ContentHash:           sha256Hex("Private replacement evidence."),
				SupersedesEvidenceIDs: []string{sharedTarget.Evidence[0].FragmentID},
				IdempotencyKey:        "cross-space-supersession",
			}},
		}, privateIngestID.String(), []EvidenceFragment{{FragmentID: privateFragmentID.String()}})
	})
	require.ErrorIs(t, err, ErrEvidenceLifecycleConflict)

	var operationCount, eventCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM evidence_lifecycle_operations
			WHERE team_id = ?::uuid AND idempotency_key = 'cross-space-supersession'
		`, teamID).Row().Scan(&operationCount); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*)
			FROM evidence_lifecycle_events
			WHERE team_id = ?::uuid AND target_fragment_id = ?::uuid
		`, teamID, sharedTarget.Evidence[0].FragmentID).Row().Scan(&eventCount)
	}))
	assert.Zero(t, operationCount)
	assert.Zero(t, eventCount)
}

func TestLedgerDirectEvidenceSupersessionRejectsSameTeamCrossOwnerReplacement(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-supersession-cross-owner")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "evidence-supersession-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "evidence-supersession-owner-b")
	ledger := NewStore(appDB, rls)
	target := createSemanticIngest(t, ctx, ledger, teamID, ownerA, "cross-owner-target", "Owner A target evidence.")
	replacement := createSemanticIngest(t, ctx, ledger, teamID, ownerB, "cross-owner-replacement", "Owner B replacement evidence.")

	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return applyDirectEvidenceSupersessions(ctx, tx, CreateIngestInput{
			TeamID:         teamID,
			OwnerProfileID: ownerA,
			RequestHash:    "cross-owner-request",
			Evidence: []EvidenceInput{{
				Content:               replacement.Evidence[0].Content,
				ContentHash:           replacement.Evidence[0].ContentHash,
				SupersedesEvidenceIDs: []string{target.Evidence[0].FragmentID},
				IdempotencyKey:        "cross-owner-supersession",
			}},
		}, uuid.NewString(), []EvidenceFragment{{FragmentID: replacement.Evidence[0].FragmentID}})
	})
	require.ErrorIs(t, err, ErrEvidenceLifecycleNotFound)
}
