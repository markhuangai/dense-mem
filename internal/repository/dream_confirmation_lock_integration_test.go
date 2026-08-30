package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestHypothesisConfirmationLockAdmitsOneCallback(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	repo := NewSemanticRepository(appDB, rls)
	teamID := uuid.NewString()
	hypothesisID := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)

	go func() {
		firstErr <- repo.WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func(DreamRepository) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-firstEntered:
	case <-ctx.Done():
		t.Fatal("first confirmation lock callback did not start")
	}

	go func() {
		secondErr <- repo.WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func(DreamRepository) error {
			return nil
		})
	}()
	select {
	case err := <-secondErr:
		require.ErrorIs(t, err, ErrDreamConfirmationBusy)
	case <-time.After(5 * time.Second):
		t.Fatal("second confirmation lock admission did not return while the first held the lock")
	}
	close(releaseFirst)

	require.NoError(t, <-firstErr)
}

func TestHypothesisConfirmationLockReleasesAfterContextCancellation(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	repo := NewSemanticRepository(appDB, rls)
	teamID := uuid.NewString()
	hypothesisID := uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- repo.WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func(DreamRepository) error {
			close(entered)
			cancel()
			return context.Canceled
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation lock callback did not start")
	}
	require.ErrorIs(t, <-firstErr, context.Canceled)

	followupCtx, followupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer followupCancel()
	followupErr := make(chan error, 1)
	go func() {
		followupErr <- repo.WithHypothesisConfirmationLock(followupCtx, teamID, hypothesisID, func(DreamRepository) error {
			return nil
		})
	}()
	require.NoError(t, <-followupErr)
}

func TestHypothesisConfirmationLockAllowsNestedRepositoryUseAtMaxOpenOne(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	repo := NewSemanticRepository(appDB, rls)
	teamID := uuid.NewString()
	hypothesisID := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstReady := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- repo.WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func(store DreamRepository) error {
			if _, err := store.GetHypothesis(ctx, GetHypothesisInput{TeamID: teamID, HypothesisID: hypothesisID}); !errors.Is(err, ErrDreamHypothesisNotFound) {
				return err
			}
			close(firstReady)
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-firstReady:
	case <-ctx.Done():
		t.Fatal("first confirmation callback did not reach its database operation")
	}
	close(releaseFirst)
	require.NoError(t, <-firstErr)
	followupErr := repo.WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func(DreamRepository) error { return nil })
	require.NoError(t, followupErr)
}

func TestHypothesisConfirmationLockBoundsDifferentHypothesesAtMaxOpenOne(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	repo := NewSemanticRepository(appDB, rls)
	teamID := uuid.NewString()
	firstHypothesisID := uuid.NewString()
	secondHypothesisID := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- repo.WithHypothesisConfirmationLock(ctx, teamID, firstHypothesisID, func(DreamRepository) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-firstEntered:
	case <-ctx.Done():
		t.Fatal("first confirmation lock callback did not start")
	}

	secondEntered := make(chan struct{})
	secondErr := make(chan error, 1)
	go func() {
		secondErr <- repo.WithHypothesisConfirmationLock(ctx, teamID, secondHypothesisID, func(DreamRepository) error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second confirmation lock callback exceeded the configured admission bound")
	case err := <-secondErr:
		require.ErrorIs(t, err, ErrDreamConfirmationBusy)
	case <-time.After(5 * time.Second):
		t.Fatal("second confirmation lock admission did not return while the bound was full")
	}
	close(releaseFirst)
	require.NoError(t, <-firstErr)
}

func TestHypothesisConfirmationLockUsesCanonicalAlias(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-confirmation-canonical-lock")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-confirmation-owner")
	semanticRepo := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Canonical lock subject")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "Canonical lock object")
	run, err := semanticRepo.ClaimDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, InitiatedByProfileID: ownerID, RunDate: "2026-08-30",
		WindowKey: "manual:canonical-lock", LeaseToken: uuid.NewString(), LeaseUntil: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	canonical, inserted, err := semanticRepo.UpsertHypothesis(ctx, UpsertHypothesisInput{
		TeamID: teamID, CreatedByProfileID: ownerID, RunID: run.RunID,
		Statement: "Canonical lock aliases share one transition.", SubjectEntityID: subject.EntityID,
		PredicateKey: "uses", PredicateVersion: 1, ObjectEntityID: object.EntityID,
		SourceVersions: map[string]int{"seed": 1}, SourceRefs: []map[string]any{},
		SourceOwnerProfileIDs: []string{ownerID}, ContentHash: "sha256:canonical-lock",
		GeneratorKind: "evaluation_seed", GeneratorVersion: "test", Payload: map[string]any{},
	})
	require.NoError(t, err)
	require.True(t, inserted)
	aliasID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO hypotheses (
			    team_id, hypothesis_id, space_id, space_generation, created_by_profile_id,
			    status, statement, rationale, likelihood, confidence, subject_entity_id,
			    predicate_key, predicate_version, object_entity_id, object_value_id,
			    source_refs, source_versions, source_owner_profile_ids, content_hash,
			    target_identity, cycle_run_id, generator_kind, generator_version,
			    invalidated_reason, submitted_ingest_id, submitted_at, canonical_hypothesis_id, payload
			)
			SELECT team_id, ?::uuid, space_id, space_generation, created_by_profile_id,
			       status, statement, rationale, likelihood, confidence, subject_entity_id,
			       predicate_key, predicate_version, object_entity_id, object_value_id,
			       source_refs, source_versions, source_owner_profile_ids, content_hash,
			       target_identity, cycle_run_id, generator_kind, generator_version,
			       invalidated_reason, submitted_ingest_id, submitted_at, ?::uuid, payload
			FROM hypotheses
			WHERE team_id = ?::uuid AND hypothesis_id = ?::uuid
		`, aliasID, canonical.HypothesisID, teamID, canonical.HypothesisID).Error
	}))

	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)
	go func() {
		firstErr <- semanticRepo.WithHypothesisConfirmationLock(lockCtx, teamID, aliasID, func(DreamRepository) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-firstEntered:
	case <-lockCtx.Done():
		t.Fatal("alias confirmation lock callback did not start")
	}
	go func() {
		secondErr <- semanticRepo.WithHypothesisConfirmationLock(lockCtx, teamID, canonical.HypothesisID, func(DreamRepository) error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("canonical confirmation callback ran while alias callback held the lock")
	case err := <-secondErr:
		require.ErrorIs(t, err, ErrDreamConfirmationBusy)
	case <-time.After(5 * time.Second):
		t.Fatal("canonical confirmation lock admission did not return while alias lock was held")
	}
	close(releaseFirst)
	require.NoError(t, <-firstErr)
	select {
	case <-secondEntered:
		t.Fatal("canonical confirmation callback ran while alias lock was held")
	default:
	}
}
