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

func TestClaimedPrivateConflictDerivedEvidenceCannotStageAfterErasureReopensSpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "conflict-derived-private-generation"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	credential := createOwnedCredential(t, credentialRepo, teamID, identityID, "conflict-derived-private-generation", domain.CredentialBindingCredentialPrivate)
	ledgerRepo := NewLedgerRepository(appDB, rls)
	privateRepo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, privateRepo.Prepare(ctx))

	ingestID := uuid.New()
	fragmentID := uuid.New()
	subjectID := uuid.New()
	objectID := uuid.New()
	conflictID := uuid.New()
	positionID := uuid.New()
	resolutionPlanID := uuid.New()
	var enqueued []ConflictDerivedEvidenceTarget
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id, space_generation)
			VALUES (?, ?, 'project', ?, ?), (?, ?, 'product', ?, ?)
		`, teamID, subjectID, credential.MemorySpaceID, credential.MemorySpaceGeneration,
			teamID, objectID, credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
				source_summary, status, proposal, metadata, space_id, space_generation
			) VALUES (?, ?, ?, ?, ?, ?, 'queued', '{}'::jsonb, '{}'::jsonb, ?, ?)
		`, teamID, ingestID, credential.ID, "private-conflict-source-"+ingestID.String(),
			"hash-"+ingestID.String(), "private conflict source", credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				content, content_hash, source_type, authority, labels, metadata,
				space_id, space_generation
			) VALUES (?, ?, ?, ?, 0, ?, ?, 'observation', 'primary', ARRAY[]::text[], '{}'::jsonb, ?, ?)
		`, teamID, fragmentID, ingestID, credential.ID, "private conflict evidence",
			"hash-"+fragmentID.String(), credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_conflict_cases (
				team_id, conflict_id, semantic_scope_key, status, subject_entity_id,
				predicate_key, predicate_version, relationship_kind, current_cardinality,
				polarity, question, policy_version, review_due_at, next_review_at,
				review_ttl_days, timezone, space_id, space_generation
			) VALUES (?, ?, ?, 'overdue', ?, 'uses', 1, 'state', 'one', '+', ?, ?, now(), now(), 2, 'UTC', ?, ?)
		`, teamID, conflictID, "private-generation:"+conflictID.String(), subjectID,
			"Which private value is current?", string(domain.ConflictPolicyVersion),
			credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_conflict_positions (
				team_id, conflict_id, position_id, position_key, object_entity_id,
				space_id, space_generation
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, teamID, conflictID, positionID, "entity:"+objectID.String(), objectID,
			credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		systemProfileID, err := ensureConflictSystemProfile(ctx, tx, teamID.String())
		if err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_conflict_resolution_plans (
				team_id, resolution_plan_id, conflict_id, expected_case_version,
				preferred_position_id, method, effective_at, effective_time_basis,
				status, applied_at, space_id, space_generation
			) VALUES (?, ?, ?, 1, ?, 'last_write_wins', now(), 'recorded_at', 'applied', now(), ?, ?)
		`, teamID, resolutionPlanID, conflictID, positionID,
			credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		enqueued, err = enqueueConflictDerivedEvidenceTasks(ctx, tx, resolutionPlanID.String(), []ConflictDerivedEvidenceTarget{{
			TeamID:               teamID.String(),
			ConflictID:           conflictID.String(),
			SystemProfileID:      systemProfileID,
			TargetFragmentID:     fragmentID.String(),
			TargetOwnerProfileID: credential.ID.String(),
			SelectedPositionID:   positionID.String(),
			SourceGroupKey:       "private-conflict-generation",
			EvidenceIndex:        0,
		}})
		return err
	}))
	require.Len(t, enqueued, 1)
	require.Equal(t, credential.MemorySpaceID.String(), enqueued[0].SpaceID)
	require.Equal(t, credential.MemorySpaceGeneration, enqueued[0].SpaceGeneration)

	claimed, err := ledgerRepo.ClaimConflictDerivedEvidenceTasks(ctx, ClaimConflictDerivedEvidenceTasksInput{
		TeamID:      teamID.String(),
		ReviewRunID: uuid.NewString(),
		WorkerID:    "private-conflict-generation-worker",
		Limit:       1,
		Lease:       time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, credential.MemorySpaceGeneration, claimed[0].SpaceGeneration)

	operation, created, err := privateRepo.RequestControlErasure(
		ctx,
		credential.MemorySpaceID,
		privateMemoryHash("conflict-derived-private-generation", credential.MemorySpaceID.String()),
		privateMemoryHash("erase-conflict-derived-private-generation", credential.MemorySpaceID.String()),
		"owner_request",
	)
	require.NoError(t, err)
	require.True(t, created)
	erasureClaim, err := privateRepo.ClaimNext(ctx, "private-conflict-erasure-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, erasureClaim)
	require.Equal(t, operation.ID, erasureClaim.ID)
	completed, err := privateRepo.ExecuteClaim(ctx, erasureClaim.ID, erasureClaim.WorkerID, erasureClaim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)

	_, err = ledgerRepo.StageConflictDerivedEvidence(ctx, claimed[0])
	require.ErrorContains(t, err, "memory space generation is stale")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var generation int64
		var lifecycle string
		if err := tx.Raw(`SELECT generation, lifecycle_state FROM memory_spaces WHERE id = ?`, credential.MemorySpaceID).Row().Scan(&generation, &lifecycle); err != nil {
			return err
		}
		require.Equal(t, credential.MemorySpaceGeneration+1, generation)
		require.Equal(t, string(domain.MemorySpaceActive), lifecycle)
		var resurrected int64
		if err := tx.Raw(`
			SELECT count(*) FROM knowledge_ingests
			WHERE team_id = ? AND idempotency_key = ?
		`, teamID, "conflict-derived:"+conflictID.String()+":"+fragmentID.String()).Scan(&resurrected).Error; err != nil {
			return err
		}
		require.Zero(t, resurrected)
		return nil
	}))
}

func TestConflictDerivedEvidenceUsesInferredAuthority(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-derived-inferred-authority")
	systemProfileID := createLedgerProfile(t, adminDB, rls, teamID, "conflict-derived-system-profile")
	ledger := NewLedgerRepository(appDB, rls)

	var spaceID string
	var spaceGeneration int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, systemProfileID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT id::text, generation
			FROM memory_spaces
			WHERE team_id = ?::uuid AND kind = 'team_shared'
		`, teamID).Row().Scan(&spaceID, &spaceGeneration)
	}))

	target := ConflictDerivedEvidenceTarget{
		TaskID: uuid.NewString(), TeamID: teamID, SpaceID: spaceID, SpaceGeneration: spaceGeneration,
		ConflictID: uuid.NewString(), SystemProfileID: systemProfileID, TargetFragmentID: uuid.NewString(),
		TargetOwnerProfileID: systemProfileID, SelectedPositionID: uuid.NewString(), SourceGroupKey: "derived-authority",
	}
	content := "Deletion-only conflict evidence remains non-semantic."
	result, err := ledger.commitDerivedEvidenceIngest(ctx, target, content, sha256Hex(content))
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
	require.Equal(t, string(domain.AuthorityInferred), result.Evidence[0].Authority)

	var authority string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT authority
			FROM evidence_fragments
			WHERE team_id = ?::uuid AND fragment_id = ?::uuid
		`, teamID, result.Evidence[0].FragmentID).Row().Scan(&authority)
	}))
	require.Equal(t, string(domain.AuthorityInferred), authority)
}
