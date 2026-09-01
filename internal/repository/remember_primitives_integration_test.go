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
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestRememberPrimitivesPersistRetryabilityAndLegacyProjection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-primitives-retryability")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-primitives-retryability-owner")
	repo := NewLedgerRepository(appDB, rls)

	explicitFalseID := uuid.NewString()
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt: RememberAttemptRecordInput{
			TeamID: teamID, OwnerProfileID: ownerID, AttemptID: explicitFalseID,
			IdempotencyKey: "retryability-explicit-false", RequestHash: "retryability-explicit-false-hash",
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "assessment", ErrorCode: "provider_response_invalid", Retryable: false, RetryabilitySet: true,
			PublicResult: map[string]any{"processing_state": "failed"},
		},
	}))
	loaded, err := repo.LoadRememberAttempt(ctx, RememberAttemptLookupInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "retryability-explicit-false"})
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.False(t, loaded.Retryable)

	explicitTrueID := uuid.NewString()
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt: RememberAttemptRecordInput{
			TeamID: teamID, OwnerProfileID: ownerID, AttemptID: explicitTrueID,
			IdempotencyKey: "retryability-explicit-true", RequestHash: "retryability-explicit-true-hash",
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "embedding", ErrorCode: "embedding_unavailable", Retryable: true, RetryabilitySet: true,
			PublicResult: map[string]any{"processing_state": "failed"},
		},
	}))
	detail, err := repo.GetRememberAttemptDiagnostic(ctx, teamID, explicitFalseID)
	require.NoError(t, err)
	require.False(t, detail.Retryable)
	require.Len(t, detail.Events, 1)
	require.Equal(t, false, detail.Events[0].Metadata["retryable"])

	legacyFailedID, legacyCompletedID := uuid.NewString(), uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, row := range []struct {
			attemptID, key, outcome string
		}{
			{legacyFailedID, "retryability-legacy-failed", "failed"},
			{legacyCompletedID, "retryability-legacy-completed", "completed"},
		} {
			if err := tx.Exec(`
				INSERT INTO remember_attempts (
				    team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
				    contract_version, submission_kind, outcome, public_result, completed_at
				) VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, 'legacy-test', 'remember', ?, '{}'::jsonb, now())
			`, teamID, row.attemptID, ownerID, row.key, row.key+"-hash", row.outcome).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	legacyFailed, err := repo.LoadRememberAttempt(ctx, RememberAttemptLookupInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "retryability-legacy-failed"})
	require.NoError(t, err)
	require.NotNil(t, legacyFailed)
	require.True(t, legacyFailed.Retryable)
	legacyCompleted, err := repo.LoadRememberAttempt(ctx, RememberAttemptLookupInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "retryability-legacy-completed"})
	require.NoError(t, err)
	require.NotNil(t, legacyCompleted)
	require.False(t, legacyCompleted.Retryable)
}

func TestRememberEvidenceOnlyCommitIsAtomicAndSearchable(t *testing.T) {
	runRememberEvidenceOnlyAtomicScenario(t)
}

func runRememberEvidenceOnlyAtomicScenario(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "remember-primitives-evidence-only", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "remember-primitives-evidence-only")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-primitives-evidence-only-owner")
	repo := NewLedgerRepository(appDB, rls)

	input := evidenceOnlyRememberInput(teamID, ownerID, "remember-primitives-evidence-only-success")
	plan, err := repo.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	require.Len(t, plan.Documents, 1)
	result, err := repo.CommitRememberWithEmbeddings(ctx, input, rememberTestEmbeddings(plan, false))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "completed", result.Outcome)
	require.Empty(t, result.RelationshipResults)
	require.Len(t, result.SearchDocuments, 1)
	require.Equal(t, string(domain.SearchProjectionCurrent), result.SearchDocuments[0].SearchState)

	counts := rememberPrimitiveCounts(t, ctx, adminDB, rls, teamID, input.IngestID)
	require.EqualValues(t, 1, counts["knowledge_ingests"])
	require.EqualValues(t, 1, counts["evidence_fragments"])
	require.EqualValues(t, 1, counts["semantic_assessments"])
	require.EqualValues(t, 1, counts["search_documents"])
	require.EqualValues(t, 1, counts["remember_attempts"])
	require.Zero(t, counts["relationship_observations"])
	require.Zero(t, counts["relationship_records"])

	unsafe := evidenceOnlyRememberInput(teamID, ownerID, "remember-primitives-evidence-only-unsafe")
	unsafeContent := "Dense-Mem must reject this unsafe evidence item."
	unsafe.Evidence = append(unsafe.Evidence, EvidenceInput{
		FragmentID: uuid.NewString(), Content: unsafeContent, ContentHash: sha256Hex(unsafeContent), SourceType: "manual", Authority: "primary",
	})
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt: RememberAttemptRecordInput{
			TeamID: teamID, OwnerProfileID: ownerID, AttemptID: unsafe.IngestID,
			IdempotencyKey: unsafe.IdempotencyKey, RequestHash: unsafe.RequestHash,
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "assessment", ErrorCode: "submission_policy_rejected", Retryable: false, RetryabilitySet: true,
			PublicResult: map[string]any{"processing_state": "failed"}, EvidenceCount: len(unsafe.Evidence),
		},
	}))
	unsafeCounts := rememberPrimitiveCounts(t, ctx, adminDB, rls, teamID, unsafe.IngestID)
	for name, count := range unsafeCounts {
		if name == "remember_attempts" {
			require.EqualValues(t, 1, count)
			continue
		}
		require.Zero(t, count, "unsafe batch must not create %s", name)
	}

	rollback := evidenceOnlyRememberInput(teamID, ownerID, "remember-primitives-evidence-only-rollback")
	rollbackPlan, err := repo.PlanRememberEmbeddings(ctx, rollback)
	require.NoError(t, err)
	_, err = repo.CommitRememberWithEmbeddings(ctx, rollback, rememberTestEmbeddings(rollbackPlan, true))
	require.Error(t, err)
	rollbackCounts := rememberPrimitiveCounts(t, ctx, adminDB, rls, teamID, rollback.IngestID)
	for _, count := range rollbackCounts {
		require.Zero(t, count)
	}
}

func TestRememberPolicyArtifactLegalHoldReleaseAndErasure(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-primitives-artifacts")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-primitives-artifacts-owner")
	repo := NewLedgerRepository(appDB, rls)

	policyInput := evidenceOnlyRememberInput(teamID, ownerID, "remember-primitives-policy-artifact")
	policyArtifactContent := []byte(`{"submission_id":"policy","evidence":[]}`)
	err := repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt: RememberAttemptRecordInput{
			TeamID: teamID, OwnerProfileID: ownerID, AttemptID: policyInput.IngestID,
			IdempotencyKey: policyInput.IdempotencyKey, RequestHash: policyInput.RequestHash,
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "assessment", ErrorCode: "submission_policy_rejected", Retryable: false, RetryabilitySet: true,
			PublicResult: map[string]any{"processing_state": "failed"}, EvidenceCount: len(policyInput.Evidence),
		},
		Artifacts: []RememberFailureArtifactInput{{ArtifactKind: "policy_rejected_request", ContentType: "application/json", Content: policyArtifactContent}},
	})
	require.NoError(t, err)
	policyDetail, err := repo.GetRememberAttemptDiagnostic(ctx, teamID, policyInput.IngestID)
	require.NoError(t, err)
	require.Len(t, policyDetail.Artifacts, 1)
	require.Equal(t, "policy_rejected_request", policyDetail.Artifacts[0].ArtifactKind)
	policyArtifact, err := repo.GetRememberFailureArtifact(ctx, teamID, policyInput.IngestID, policyDetail.Artifacts[0].ArtifactID)
	require.NoError(t, err)
	require.NotContains(t, string(policyArtifact.Content), policyInput.Evidence[0].Content)
	require.Contains(t, string(policyArtifact.Content), "content_hash")

	teamUUID := uuid.MustParse(teamID)
	ownerIdentityID := createLedgerSSOIdentity(t, adminDB, rls, teamUUID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	credential := createOwnedCredential(t, credentialRepo, teamUUID, ownerIdentityID, "remember-primitives-held", domain.CredentialBindingCredentialPrivate)
	require.NotEqual(t, uuid.Nil, credential.MemorySpaceID)
	privateRepo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, privateRepo.Prepare(ctx))

	oldCaptured := time.Now().UTC().Add(-8 * 24 * time.Hour)
	heldAttemptID, heldArtifactID := uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt: RememberAttemptRecordInput{
			TeamID: teamID, OwnerProfileID: credential.ID.String(), AttemptID: heldAttemptID,
			SpaceID: credential.MemorySpaceID.String(), SpaceGeneration: credential.MemorySpaceGeneration,
			IdempotencyKey: "remember-primitives-held", RequestHash: "remember-primitives-held-hash",
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "preflight", ErrorCode: "submission_policy_rejected", PublicResult: map[string]any{},
		},
		Artifacts: []RememberFailureArtifactInput{{
			ArtifactID: heldArtifactID, ArtifactKind: "policy_rejected_request", ContentType: "application/json",
			Content: []byte(`{"submission_id":"held","evidence":[]}`), CapturedAt: oldCaptured, ExpiresAt: oldCaptured.Add(7 * 24 * time.Hour),
		}},
	}))
	_, err = repo.GetRememberFailureArtifact(ctx, teamID, heldAttemptID, heldArtifactID)
	require.ErrorIs(t, err, ErrRememberFailureArtifactNotFound)

	_, created, err := privateRepo.PlaceLegalHold(ctx, credential.MemorySpaceID, "remember-primitives-hold")
	require.NoError(t, err)
	require.True(t, created)
	var retained bool
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT retained_by_legal_hold FROM remember_failure_artifacts WHERE team_id = ?::uuid AND artifact_id = ?::uuid`, teamID, heldArtifactID).Row().Scan(&retained)
	}))
	require.True(t, retained)
	heldArtifact, err := repo.GetRememberFailureArtifact(ctx, teamID, heldAttemptID, heldArtifactID)
	require.NoError(t, err)
	require.True(t, heldArtifact.RetainedByLegalHold)
	deleted, err := repo.PurgeExpiredRememberFailureArtifacts(ctx, 100)
	require.NoError(t, err)
	require.Zero(t, deleted)
	var profileVisible int64
	require.NoError(t, rls.WithTeamProfileReadOnlyRepeatableTx(ctx, appDB, teamID, credential.ID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM remember_failure_artifacts WHERE team_id = ?::uuid`, teamID).Row().Scan(&profileVisible)
	}))
	require.Zero(t, profileVisible)

	_, released, err := privateRepo.ReleaseLegalHold(ctx, credential.MemorySpaceID)
	require.NoError(t, err)
	require.True(t, released)
	_, err = repo.GetRememberFailureArtifact(ctx, teamID, heldAttemptID, heldArtifactID)
	require.ErrorIs(t, err, ErrRememberFailureArtifactNotFound)
	deleted, err = repo.PurgeExpiredRememberFailureArtifacts(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)

	currentAttemptID, currentArtifactID := uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt: RememberAttemptRecordInput{
			TeamID: teamID, OwnerProfileID: credential.ID.String(), AttemptID: currentAttemptID,
			SpaceID: credential.MemorySpaceID.String(), SpaceGeneration: credential.MemorySpaceGeneration,
			IdempotencyKey: "remember-primitives-erasure", RequestHash: "remember-primitives-erasure-hash",
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "preflight", ErrorCode: "submission_policy_rejected", PublicResult: map[string]any{},
		},
		Artifacts: []RememberFailureArtifactInput{{
			ArtifactID: currentArtifactID, ArtifactKind: "failure", ContentType: "application/json",
			Content: []byte(`{"erased":true}`), CapturedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		}},
	}))
	operation, created, err := privateRepo.RequestControlErasure(ctx, credential.MemorySpaceID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "remember-primitives-erasure")
	require.NoError(t, err)
	require.True(t, created)
	claim, err := privateRepo.ClaimNext(ctx, "remember-primitives-erasure-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, operation.ID, claim.ID)
	_, err = privateRepo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	_, err = repo.GetRememberFailureArtifact(ctx, teamID, currentAttemptID, currentArtifactID)
	require.ErrorIs(t, err, ErrRememberFailureArtifactNotFound)
}

func TestRememberFailureArtifactHoldSkipsNoopUpdates(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamUUID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "remember-primitives-hold-noop"))
	ownerIdentityID := createLedgerSSOIdentity(t, adminDB, rls, teamUUID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	credential := createOwnedCredential(t, credentialRepo, teamUUID, ownerIdentityID, "remember-primitives-hold-noop", domain.CredentialBindingCredentialPrivate)
	repo := NewLedgerRepository(appDB, rls)
	privateRepo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, privateRepo.Prepare(ctx))

	attemptID, artifactID := uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt: RememberAttemptRecordInput{
			TeamID: teamUUID.String(), OwnerProfileID: credential.ID.String(), AttemptID: attemptID,
			SpaceID: credential.MemorySpaceID.String(), SpaceGeneration: credential.MemorySpaceGeneration,
			IdempotencyKey: "remember-primitives-hold-noop", RequestHash: "remember-primitives-hold-noop-hash",
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "preflight", ErrorCode: "provider_unavailable", PublicResult: map[string]any{},
		},
		Artifacts: []RememberFailureArtifactInput{{
			ArtifactID: artifactID, ArtifactKind: "failure", ContentType: "application/json",
			Content: []byte(`{"noop":true}`), CapturedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		}},
	}))

	tuple := func() [2]string {
		var value [2]string
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Raw(`
				SELECT xmin::text, ctid::text
				FROM remember_failure_artifacts
				WHERE team_id = ?::uuid AND artifact_id = ?::uuid
			`, teamUUID.String(), artifactID).Row().Scan(&value[0], &value[1])
		}))
		return value
	}

	freeBefore := tuple()
	require.NoError(t, repo.synchronizeRememberFailureArtifactHold(ctx, credential.MemorySpaceID.String()))
	require.Equal(t, freeBefore, tuple(), "repeating a released hold must not update artifacts")

	_, created, err := privateRepo.PlaceLegalHold(ctx, credential.MemorySpaceID, "remember-primitives-hold-noop")
	require.NoError(t, err)
	require.True(t, created)
	heldBefore := tuple()
	require.NoError(t, repo.synchronizeRememberFailureArtifactHold(ctx, credential.MemorySpaceID.String()))
	require.Equal(t, heldBefore, tuple(), "repeating an active hold must not update artifacts")

	_, released, err := privateRepo.ReleaseLegalHold(ctx, credential.MemorySpaceID)
	require.NoError(t, err)
	require.True(t, released)
	releasedBefore := tuple()
	require.NoError(t, repo.synchronizeRememberFailureArtifactHold(ctx, credential.MemorySpaceID.String()))
	require.Equal(t, releasedBefore, tuple(), "repeating a released hold must not update artifacts")
}

func TestRememberFailureArtifactHoldAndPurgeSerializeOnPrivateSpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	appSQL, err := appDB.DB()
	require.NoError(t, err)
	appSQL.SetMaxOpenConns(8)
	appSQL.SetMaxIdleConns(8)

	teamUUID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "remember-primitives-hold-purge-race"))
	ownerIdentityID := createLedgerSSOIdentity(t, adminDB, rls, teamUUID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	credential := createOwnedCredential(t, credentialRepo, teamUUID, ownerIdentityID, "remember-primitives-hold-purge-race", domain.CredentialBindingCredentialPrivate)
	privateRepo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, privateRepo.Prepare(ctx))

	input := evidenceOnlyRememberInput(teamUUID.String(), credential.ID.String(), "remember-primitives-hold-purge-race")
	input.SpaceID = credential.MemorySpaceID.String()
	input.SpaceGeneration = credential.MemorySpaceGeneration
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	artifactID := uuid.NewString()
	require.NoError(t, NewLedgerRepository(appDB, rls).RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt: RememberAttemptRecordInput{
			TeamID: teamUUID.String(), OwnerProfileID: credential.ID.String(), AttemptID: input.IngestID,
			SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
			RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "preflight", ErrorCode: "provider_unavailable", PublicResult: map[string]any{},
		},
		Artifacts: []RememberFailureArtifactInput{{
			ArtifactID: artifactID, ArtifactKind: "failure", ContentType: "application/json",
			Content: []byte(`{"expired":true}`), CapturedAt: old, ExpiresAt: old.Add(7 * 24 * time.Hour),
		}},
	}))

	const barrierKey int64 = 8070317317001
	const barrierFunction = "dense_mem_test_remember_hold_purge_barrier"
	const barrierTrigger = "dense_mem_test_remember_hold_purge_barrier_trigger"
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			CREATE OR REPLACE FUNCTION ` + barrierFunction + `()
			RETURNS trigger
			LANGUAGE plpgsql
			AS $function$
			BEGIN
				PERFORM pg_advisory_xact_lock(8070317317001::bigint);
				RETURN NEW;
			END
			$function$;
			DROP TRIGGER IF EXISTS ` + barrierTrigger + ` ON private_memory_legal_holds;
			CREATE TRIGGER ` + barrierTrigger + `
				BEFORE INSERT ON private_memory_legal_holds
				FOR EACH ROW EXECUTE FUNCTION ` + barrierFunction + `();
		`).Error
	}))
	defer func() {
		_ = rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
			return tx.Exec(`
				DROP TRIGGER IF EXISTS ` + barrierTrigger + ` ON private_memory_legal_holds;
				DROP FUNCTION IF EXISTS ` + barrierFunction + `();
			`).Error
		})
	}()

	barrierConn, err := adminSQL.Conn(ctx)
	require.NoError(t, err)
	defer barrierConn.Close()
	_, err = barrierConn.ExecContext(ctx, "SELECT pg_advisory_lock($1::bigint)", barrierKey)
	require.NoError(t, err)

	type holdOutcome struct {
		hold    *domain.PrivateMemoryLegalHold
		created bool
		err     error
	}
	holdDone := make(chan holdOutcome, 1)
	go func() {
		hold, created, err := privateRepo.PlaceLegalHold(ctx, credential.MemorySpaceID, "race_hold")
		holdDone <- holdOutcome{hold: hold, created: created, err: err}
	}()

	waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer waitCancel()
	for {
		var waiting bool
		err := adminSQL.QueryRowContext(waitCtx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks
				WHERE locktype = 'advisory'
				  AND NOT granted
				  AND objsubid = 1
				  AND classid::bigint = (($1::bigint >> 32) & 4294967295)
				  AND objid::bigint = ($1::bigint & 4294967295)
			)
		`, barrierKey).Scan(&waiting)
		require.NoError(t, err)
		if waiting {
			break
		}
		select {
		case outcome := <-holdDone:
			require.NoError(t, outcome.err, "hold returned before reaching the insert barrier")
			t.Fatalf("hold returned before reaching the insert barrier: created=%v hold=%v", outcome.created, outcome.hold)
		case <-waitCtx.Done():
			t.Fatal("legal-hold insert did not reach the PostgreSQL advisory barrier")
		case <-time.After(10 * time.Millisecond):
		}
	}

	purgeDone := make(chan struct {
		deleted int
		err     error
	}, 1)
	go func() {
		deleted, err := NewLedgerRepository(appDB, rls).PurgeExpiredRememberFailureArtifacts(ctx, 100)
		purgeDone <- struct {
			deleted int
			err     error
		}{deleted: deleted, err: err}
	}()
	select {
	case purge := <-purgeDone:
		require.NoError(t, purge.err)
		require.Zero(t, purge.deleted, "purge must skip an expired artifact while its private space is locked by hold placement")
	case <-time.After(2 * time.Second):
		t.Fatal("purge remained blocked behind the legal-hold space lock")
	}

	_, err = barrierConn.ExecContext(ctx, "SELECT pg_advisory_unlock($1::bigint)", barrierKey)
	require.NoError(t, err)
	outcome := <-holdDone
	require.NoError(t, outcome.err)
	require.True(t, outcome.created)
	require.NotNil(t, outcome.hold)

	repo := NewLedgerRepository(appDB, rls)
	artifact, err := repo.GetRememberFailureArtifact(ctx, teamUUID.String(), input.IngestID, artifactID)
	require.NoError(t, err)
	require.True(t, artifact.RetainedByLegalHold)
	var profileVisible int64
	require.NoError(t, rls.WithTeamProfileReadOnlyRepeatableTx(ctx, appDB, teamUUID.String(), credential.ID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM remember_failure_artifacts WHERE team_id = ?::uuid`, teamUUID.String()).Row().Scan(&profileVisible)
	}))
	require.Zero(t, profileVisible, "held failure artifacts must remain system/control-only")

	_, released, err := privateRepo.ReleaseLegalHold(ctx, credential.MemorySpaceID)
	require.NoError(t, err)
	require.True(t, released)
	deleted, err := repo.PurgeExpiredRememberFailureArtifacts(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	_, err = repo.GetRememberFailureArtifact(ctx, teamUUID.String(), input.IngestID, artifactID)
	require.ErrorIs(t, err, ErrRememberFailureArtifactNotFound)
}

func evidenceOnlyRememberInput(teamID, ownerID, label string) SynchronousRememberCommitInput {
	fragmentID, ingestID, assessmentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	content := "Dense-Mem stores this evidence without a semantic Relationship. [" + label + "]"
	return SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
		IdempotencyKey: label, RequestHash: sha256Hex(label), SourceSummary: label,
		Evidence:     []EvidenceInput{{FragmentID: fragmentID, Content: content, ContentHash: sha256Hex(content), SourceType: "manual", Authority: "primary"}},
		AssessmentID: assessmentID, AssessmentJSON: json.RawMessage(`{"request_id":"` + label + `"}`), ProviderTurns: 1,
		EvidenceSecurityResults: []EvidenceSecurityResult{{FragmentID: fragmentID, EvidenceID: "evidence:0", EvidenceIndex: 0, Decision: "pass", Safe: true}},
		Commit: CommitSubmissionAssessmentInput{
			AssessmentID: assessmentID, Items: []SubmissionAssessmentItemInput{{FragmentID: fragmentID}},
			Payload: map[string]any{"response_hash": sha256Hex(label), "model": "test-model", "tokenizer": "o200k_base", "candidate_context_tokens": 0, "candidate_context_truncated": false},
		},
	}
}

func rememberTestEmbeddings(plan *InlineEmbeddingPlan, invalid bool) []InlineEmbeddingResult {
	results := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		vector := make([]float32, plan.EmbeddingDimensions)
		vector[0] = 1
		if invalid {
			vector = vector[:1]
		}
		results = append(results, InlineEmbeddingResult{
			DocumentHash: document.DocumentHash, Embedding: vector,
			EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions,
			EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration,
		})
	}
	return results
}

func rememberPrimitiveCounts(t *testing.T, ctx context.Context, db *gorm.DB, rls storagepostgres.RLSHelper, teamID, ingestID string) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64)
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		queries := map[string]string{
			"knowledge_ingests":         `SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND ingest_id = ?::uuid`,
			"evidence_fragments":        `SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid AND ingest_id = ?::uuid`,
			"semantic_assessments":      `SELECT count(*) FROM semantic_assessments WHERE team_id = ?::uuid AND attempt_id = ?::uuid`,
			"search_documents":          `SELECT count(*) FROM search_documents WHERE team_id = ?::uuid AND source_kind = 'evidence' AND source_id IN (SELECT fragment_id FROM evidence_fragments WHERE team_id = ?::uuid AND ingest_id = ?::uuid)`,
			"remember_attempts":         `SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND attempt_id = ?::uuid`,
			"relationship_observations": `SELECT count(*) FROM relationship_observations WHERE team_id = ?::uuid AND ingest_id = ?::uuid`,
			"relationship_records": `SELECT count(*)
				FROM relationship_records AS record
				JOIN relationship_observations AS observation
				  ON observation.team_id = record.team_id
				 AND observation.relationship_id = record.relationship_id
				WHERE record.team_id = ?::uuid
				  AND observation.team_id = ?::uuid
				  AND observation.ingest_id = ?::uuid`,
		}
		for name, query := range queries {
			var count int64
			var err error
			if name == "search_documents" || name == "relationship_records" {
				err = tx.Raw(query, teamID, teamID, ingestID).Row().Scan(&count)
			} else {
				err = tx.Raw(query, teamID, ingestID).Row().Scan(&count)
			}
			if err != nil {
				return err
			}
			counts[name] = count
		}
		return nil
	}))
	return counts
}
