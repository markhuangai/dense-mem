//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamapp "github.com/markhuangai/dense-mem/internal/dream"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestResolveFeedbackRecordsDiagnosticAfterConfirmationLockRelease(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	teamID := createLedgerTeam(t, adminDB, rls, "dream-confirmation-diagnostic-lock")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-confirmation-diagnostic-owner")
	lockStore := NewStore(appDB, rls)
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: uuid.MustParse(teamID), OwnerID: uuid.MustParse(ownerID),
	})

	for _, tc := range []struct {
		name     string
		decision string
	}{
		{name: "lifecycle feedback", decision: "reinforce"},
		{name: "confirmation", decision: "confirm_true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hypothesisID := uuid.NewString()
			fixture := &confirmationLockFixtureRepository{record: dreamcontract.HypothesisRecord{
				TeamID: teamID, HypothesisID: hypothesisID, CycleRunID: uuid.NewString(),
				CreatedByProfileID: ownerID, Status: string(domain.DreamStatusProposed),
				Statement: "A fixture hypothesis requires independent evidence.",
			}}
			store := &confirmationLockServiceRepository{
				DreamRepository: fixture,
				lockStore:       lockStore,
			}
			diagnostics := &confirmationLockReacquireDiagnostics{
				lockStore: lockStore, teamID: teamID, hypothesisID: hypothesisID,
			}
			deps := dreamapp.Dependencies{Store: store, Diagnostics: diagnostics}
			request := dreamapp.ResolveFeedbackRequest{DreamID: hypothesisID, Decision: tc.decision}
			if tc.decision == "confirm_true" {
				deps.Remember = confirmationLockRememberService{result: completedConfirmationRememberResult(uuid.NewString())}
				request.Evidence = []rememberapp.RememberEvidenceInput{{Content: "Independent fixture evidence."}}
			}

			result, err := dreamapp.New(deps).ResolveFeedback(ctx, "", request)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, diagnostics.recordCalls)
			require.NoError(t, diagnostics.reacquireErr)
			require.True(t, diagnostics.reacquired)
		})
	}
}

type confirmationLockFixtureRepository struct {
	dreamcontract.DreamRepository
	record dreamcontract.HypothesisRecord
}

func (r *confirmationLockFixtureRepository) GetHypothesis(context.Context, dreamcontract.GetHypothesisInput) (*dreamcontract.HypothesisRecord, error) {
	record := r.record
	return &record, nil
}

func (r *confirmationLockFixtureRepository) UpdateHypothesisStatus(_ context.Context, input dreamcontract.UpdateHypothesisStatusInput) (*dreamcontract.HypothesisRecord, error) {
	record := r.record
	record.Status = input.Status
	record.InvalidatedReason = input.InvalidatedReason
	r.record = record
	return &record, nil
}

func (r *confirmationLockFixtureRepository) SubmitHypothesis(_ context.Context, input dreamcontract.SubmitHypothesisInput) (*dreamcontract.HypothesisRecord, error) {
	record := r.record
	record.Status = string(domain.DreamStatusSubmitted)
	record.SubmittedIngestID = input.SubmittedIngestID
	record.SubmittedDecision = input.Decision
	record.InvalidatedReason = input.InvalidatedReason
	r.record = record
	return &record, nil
}

type confirmationLockServiceRepository struct {
	dreamcontract.DreamRepository
	lockStore *Store
}

func (r *confirmationLockServiceRepository) WithHypothesisConfirmationLock(
	ctx context.Context,
	teamID string,
	hypothesisID string,
	fn func(dreamcontract.DreamRepository) error,
) error {
	return r.lockStore.WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func(DreamRepository) error {
		return fn(r)
	})
}

type confirmationLockReacquireDiagnostics struct {
	lockStore    *Store
	teamID       string
	hypothesisID string
	recordCalls  int
	reacquired   bool
	reacquireErr error
}

func (r *confirmationLockReacquireDiagnostics) RecordDreamDiagnostic(ctx context.Context, _ dreamcontract.DreamDiagnosticCaptureInput) error {
	r.recordCalls++
	r.reacquireErr = r.lockStore.WithHypothesisConfirmationLock(ctx, r.teamID, r.hypothesisID, func(DreamRepository) error {
		r.reacquired = true
		return nil
	})
	return r.reacquireErr
}

func (*confirmationLockReacquireDiagnostics) RecordDreamRunDiagnostics(context.Context, dreamcontract.DreamDiagnosticCaptureInput) error {
	return nil
}

func (*confirmationLockReacquireDiagnostics) ListDreamDiagnostics(context.Context, dreamcontract.DreamDiagnosticListInput) (dreamcontract.DreamDiagnosticPage, error) {
	return dreamcontract.DreamDiagnosticPage{}, nil
}

func (*confirmationLockReacquireDiagnostics) GetDreamDiagnostic(context.Context, string, string, string) (*dreamcontract.DreamDiagnosticCapture, error) {
	return nil, dreamcontract.ErrDreamDiagnosticNotFound
}

func (*confirmationLockReacquireDiagnostics) PurgeExpiredDreamDiagnostics(context.Context, int) (int, error) {
	return 0, nil
}

type confirmationLockRememberService struct {
	result *rememberapp.RememberResult
}

func (s confirmationLockRememberService) Remember(context.Context, rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
	return s.result, nil
}

func completedConfirmationRememberResult(ingestID string) *rememberapp.RememberResult {
	terminal := &rememberapp.TerminalRememberResult{
		ContractVersion: domain.ContractVersion,
		SubmissionID:    ingestID,
		SubmissionKind:  "remember",
		ProcessingState: string(rememberapp.TerminalProcessingCompleted),
		Kind:            rememberapp.ResultKindTerminal,
	}
	return &rememberapp.RememberResult{
		ContractVersion: terminal.ContractVersion,
		IngestID:        terminal.SubmissionID,
		SubmissionID:    terminal.SubmissionID,
		SubmissionKind:  terminal.SubmissionKind,
		ProcessingState: terminal.ProcessingState,
		Kind:            rememberapp.ResultKindTerminal,
		Terminal:        terminal,
	}
}

var _ dreamcontract.DreamRepository = (*confirmationLockServiceRepository)(nil)
var _ dreamcontract.DreamDiagnosticRepository = (*confirmationLockReacquireDiagnostics)(nil)

func TestHypothesisConfirmationLockAdmitsOneCallback(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	repo := newDreamFixtureStore(appDB, rls)
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

	repo := newDreamFixtureStore(appDB, rls)
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

func TestHypothesisConfirmationLockAllowsNestedRepositoryUseWithinPoolBudget(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(2)
	repo := newDreamFixtureStore(appDB, rls)
	teamID := uuid.NewString()
	hypothesisID := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstReady := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)
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
	go func() {
		secondErr <- repo.WithHypothesisConfirmationLock(ctx, teamID, uuid.NewString(), func(DreamRepository) error {
			return nil
		})
	}()
	require.ErrorIs(t, <-secondErr, ErrDreamConfirmationBusy)
	close(releaseFirst)
	require.NoError(t, <-firstErr)
	followupErr := repo.WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func(DreamRepository) error { return nil })
	require.NoError(t, followupErr)
}

func TestHypothesisConfirmationLockRejectsPoolWithoutApplicationCapacity(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	repo := newDreamFixtureStore(appDB, rls)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	callbackCalled := false
	err = repo.WithHypothesisConfirmationLock(ctx, uuid.NewString(), uuid.NewString(), func(DreamRepository) error {
		callbackCalled = true
		return nil
	})
	require.ErrorIs(t, err, ErrDreamConfirmationBusy)
	require.False(t, callbackCalled)
}

func TestHypothesisConfirmationLockDiscardsFailedCleanupConnection(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lockConn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)

	require.NoError(t, discardDreamConfirmationLockConnection(lockConn))
	require.ErrorIs(t, lockConn.PingContext(ctx), sql.ErrConnDone)
	require.NoError(t, sqlDB.PingContext(ctx))
}

func TestHypothesisConfirmationLockBoundsDifferentHypothesesWithinPoolBudget(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(dreamConfirmationLockAdmissionLimit + 1)
	sqlDB.SetMaxIdleConns(dreamConfirmationLockAdmissionLimit + 1)
	repo := newDreamFixtureStore(appDB, rls)
	teamID := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	entered := make(chan struct{}, dreamConfirmationLockAdmissionLimit)
	errs := make(chan error, dreamConfirmationLockAdmissionLimit)
	for i := 0; i < dreamConfirmationLockAdmissionLimit; i++ {
		hypothesisID := uuid.NewString()
		go func() {
			errs <- repo.WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func(DreamRepository) error {
				entered <- struct{}{}
				<-release
				return nil
			})
		}()
	}
	for i := 0; i < dreamConfirmationLockAdmissionLimit; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("confirmation lock callback did not start")
		}
	}

	extraEntered := make(chan struct{})
	extraErr := make(chan error, 1)
	go func() {
		extraErr <- repo.WithHypothesisConfirmationLock(ctx, teamID, uuid.NewString(), func(DreamRepository) error {
			close(extraEntered)
			return nil
		})
	}()
	select {
	case <-extraEntered:
		t.Fatal("confirmation lock callback exceeded the configured admission bound")
	case err := <-extraErr:
		require.ErrorIs(t, err, ErrDreamConfirmationBusy)
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation lock admission did not return while the bound was full")
	}
	close(release)
	for i := 0; i < dreamConfirmationLockAdmissionLimit; i++ {
		require.NoError(t, <-errs)
	}
}

func TestHypothesisConfirmationLockUsesCanonicalAlias(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-confirmation-canonical-lock")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-confirmation-owner")
	semanticRepo := newDreamFixtureStore(appDB, rls)
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
