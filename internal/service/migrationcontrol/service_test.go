package migrationcontrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestStatusDefaultsDoNotRequireMigration(t *testing.T) {
	status, err := New(nil, Config{}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != domain.V2MigrationStateNotRequired || !status.DataPlaneAllowed {
		t.Fatalf("status = %#v", status)
	}

	required, err := New(nil, Config{Required: true}).Status(context.Background())
	if err != nil {
		t.Fatalf("required Status: %v", err)
	}
	if required.State != domain.V2MigrationStateRequired || required.DataPlaneAllowed {
		t.Fatalf("required status = %#v", required)
	}
}

func TestPreflightStartPauseResumeStateMachine(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := newStoreStub()
	svc := New(store, Config{Required: true, Now: func() time.Time { return now }})
	ctx := context.Background()

	_, err := svc.Start(ctx, OperatorRequest{Actor: "operator"})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("start without preflight err = %v", err)
	}

	_, err = svc.ApprovePreflight(ctx, OperatorRequest{
		Actor:           "operator",
		BackupReference: "backup-20260717",
		PreflightChecks: map[string]any{
			"postgres_restore_verified": true,
			"neo4j_snapshot_verified":   true,
		},
	})
	if err != nil {
		t.Fatalf("ApprovePreflight: %v", err)
	}
	if store.run.State != domain.V2MigrationStateReady || !store.run.PreflightApproved {
		t.Fatalf("run after preflight = %#v", store.run)
	}

	started, err := svc.Start(ctx, OperatorRequest{Actor: "operator", Reason: "begin rehearsal"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.State != domain.V2MigrationStateRunning || started.Run.StartedAt == nil {
		t.Fatalf("started status = %#v", started)
	}

	paused, err := svc.Pause(ctx, OperatorRequest{Actor: "operator"})
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if paused.State != domain.V2MigrationStatePaused {
		t.Fatalf("paused status = %#v", paused)
	}

	resumed, err := svc.Resume(ctx, OperatorRequest{Actor: "operator"})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.State != domain.V2MigrationStateRunning {
		t.Fatalf("resumed status = %#v", resumed)
	}

	if len(store.actions) != 4 {
		t.Fatalf("operator actions = %#v", store.actions)
	}
}

func TestPreflightRequiresHardBackupChecks(t *testing.T) {
	svc := New(newStoreStub(), Config{Required: true})
	_, err := svc.ApprovePreflight(context.Background(), OperatorRequest{
		BackupReference: "backup",
		PreflightChecks: map[string]any{"postgres_restore_verified": true},
	})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("ApprovePreflight err = %v", err)
	}

	_, err = svc.ApprovePreflight(context.Background(), OperatorRequest{
		PreflightChecks: map[string]any{
			"postgres_restore_verified": true,
			"neo4j_snapshot_verified":   true,
		},
	})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("ApprovePreflight missing backup err = %v", err)
	}

	_, err = svc.ApprovePreflight(context.Background(), OperatorRequest{
		BackupReference: "backup",
		PreflightChecks: map[string]any{"neo4j_snapshot_verified": true},
	})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("ApprovePreflight missing postgres check err = %v", err)
	}
}

func TestPreflightUpdatesExistingRequiredRun(t *testing.T) {
	ctx := context.Background()
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:    "run-1",
		State:    domain.V2MigrationStateRequired,
		Required: true,
	}
	svc := New(store, Config{Required: true})

	status, err := svc.ApprovePreflight(ctx, OperatorRequest{
		BackupReference: "backup-20260717",
		PreflightChecks: map[string]any{
			"postgres_restore_verified": "true",
			"neo4j_snapshot_verified":   "true",
		},
	})
	if err != nil {
		t.Fatalf("ApprovePreflight existing run: %v", err)
	}
	if status.State != domain.V2MigrationStateReady || !status.Run.PreflightApproved {
		t.Fatalf("status = %#v", status)
	}
	if len(store.actions) != 1 || store.actions[0].Actor != "control" {
		t.Fatalf("actions = %#v", store.actions)
	}
	if got := store.actions[0].Metadata["backup_reference"]; got != "backup-20260717" {
		t.Fatalf("backup metadata = %#v", store.actions[0].Metadata)
	}
}

func TestStartRequiresApprovedPreflight(t *testing.T) {
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:             "run-1",
		State:             domain.V2MigrationStateReady,
		Required:          true,
		PreflightApproved: false,
	}
	_, err := New(store, Config{Required: true}).Start(context.Background(), OperatorRequest{})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("Start err = %v", err)
	}
}

func TestPreflightPropagatesStoreErrors(t *testing.T) {
	ctx := context.Background()
	req := OperatorRequest{
		BackupReference: "backup-20260717",
		PreflightChecks: map[string]any{
			"postgres_restore_verified": true,
			"neo4j_snapshot_verified":   true,
		},
	}
	want := errors.New("store failed")

	store := newStoreStub()
	store.createErr = want
	_, err := New(store, Config{Required: true}).ApprovePreflight(ctx, req)
	if !errors.Is(err, want) {
		t.Fatalf("create err = %v", err)
	}

	store = newStoreStub()
	store.run = &domain.V2MigrationRun{RunID: "run-1", State: domain.V2MigrationStateRequired}
	store.updateErr = want
	_, err = New(store, Config{Required: true}).ApprovePreflight(ctx, req)
	if !errors.Is(err, want) {
		t.Fatalf("update err = %v", err)
	}

	store = newStoreStub()
	store.recordErr = want
	_, err = New(store, Config{Required: true}).ApprovePreflight(ctx, req)
	if !errors.Is(err, want) {
		t.Fatalf("record err = %v", err)
	}
}

func TestIllegalTransitionsAndMarkersRemainVisible(t *testing.T) {
	ctx := context.Background()
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:             "run-1",
		State:             domain.V2MigrationStateRunning,
		PreflightApproved: true,
		Required:          true,
	}
	svc := New(store, Config{Required: true})
	_, err := svc.Start(ctx, OperatorRequest{})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("illegal start err = %v", err)
	}

	store.marker = &domain.V2CompatibilityMarker{Status: domain.V2MigrationMarkerCompatible}
	_, err = svc.Pause(ctx, OperatorRequest{})
	if !errors.Is(err, ErrAlreadyCutOver) {
		t.Fatalf("compatible marker err = %v", err)
	}
	status, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status compatible: %v", err)
	}
	if status.State != domain.V2MigrationStateCutOver || !status.DataPlaneAllowed {
		t.Fatalf("compatible status = %#v", status)
	}

	store.marker = &domain.V2CompatibilityMarker{Status: domain.V2MigrationMarkerCorrupt}
	status, err = svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status corrupt: %v", err)
	}
	if status.State != domain.V2MigrationStateIncompatible || status.DataPlaneAllowed {
		t.Fatalf("corrupt status = %#v", status)
	}
	_, err = svc.Pause(ctx, OperatorRequest{})
	if !errors.Is(err, ErrIncompatible) {
		t.Fatalf("corrupt marker action err = %v", err)
	}
}

func TestStoreBackedStatusWithoutRun(t *testing.T) {
	status, err := New(newStoreStub(), Config{Required: true}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != domain.V2MigrationStateRequired || status.DataPlaneAllowed {
		t.Fatalf("status = %#v", status)
	}
}

func TestStatusPropagatesStoreErrors(t *testing.T) {
	ctx := context.Background()
	want := errors.New("store failed")

	store := newStoreStub()
	store.markerErr = want
	_, err := New(store, Config{}).Status(ctx)
	if !errors.Is(err, want) {
		t.Fatalf("marker err = %v", err)
	}

	store = newStoreStub()
	store.runErr = want
	_, err = New(store, Config{}).Status(ctx)
	if !errors.Is(err, want) {
		t.Fatalf("run err = %v", err)
	}

	store = newStoreStub()
	store.run = &domain.V2MigrationRun{RunID: "run-1", State: domain.V2MigrationStateRunning}
	store.actionsErr = want
	_, err = New(store, Config{}).Status(ctx)
	if !errors.Is(err, want) {
		t.Fatalf("actions err = %v", err)
	}
}

func TestActionRequiresStore(t *testing.T) {
	_, err := New(nil, Config{}).Start(context.Background(), OperatorRequest{})
	if err == nil || errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("Start nil store err = %v", err)
	}
}

func TestReadinessMessagesForMigrationStates(t *testing.T) {
	cases := map[string]string{
		domain.V2MigrationStateReady:        "migration preflight approved; start is allowed",
		domain.V2MigrationStateRunning:      "migration is running",
		domain.V2MigrationStatePaused:       "migration is paused at a durable checkpoint",
		domain.V2MigrationStateFailed:       "migration failed; inspect errors and resume only if retryable",
		domain.V2MigrationStateVerifying:    "migration is validating cutover gates",
		domain.V2MigrationStateReadyCutover: "migration is validating cutover gates",
		domain.V2MigrationStateCutOver:      "migration cutover is complete",
		domain.V2MigrationStateIncompatible: "migration marker is incompatible",
		"unknown":                           "legacy migration is required; use the private control portal",
	}
	for state, want := range cases {
		t.Run(state, func(t *testing.T) {
			status := statusFromRunMarker(true, &domain.V2MigrationRun{
				RunID:    "run-1",
				State:    state,
				Required: true,
			}, nil, nil)
			if status.ReadinessMessage != want {
				t.Fatalf("message = %q, want %q", status.ReadinessMessage, want)
			}
			if state == domain.V2MigrationStateCutOver && !status.DataPlaneAllowed {
				t.Fatalf("cutover should allow data plane: %#v", status)
			}
		})
	}
}

type storeStub struct {
	run        *domain.V2MigrationRun
	marker     *domain.V2CompatibilityMarker
	actions    []domain.V2MigrationOperatorAction
	runErr     error
	markerErr  error
	actionsErr error
	createErr  error
	updateErr  error
	recordErr  error
}

func newStoreStub() *storeStub {
	return &storeStub{}
}

func (s *storeStub) GetLatestRun(context.Context) (*domain.V2MigrationRun, error) {
	if s.runErr != nil {
		return nil, s.runErr
	}
	return cloneRun(s.run), nil
}

func (s *storeStub) CreateRun(_ context.Context, input repository.V2CreateMigrationRunInput) (*domain.V2MigrationRun, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: input.MigrationContractVersion,
		CorpusVersion:            input.CorpusVersion,
		SourceKind:               input.SourceKind,
		State:                    input.State,
		Phase:                    input.Phase,
		Required:                 input.Required,
		PreflightApproved:        input.PreflightApproved,
		BackupReference:          input.BackupReference,
		PreflightChecks:          input.PreflightChecks,
		Retryable:                true,
		CreatedAt:                input.Now,
		UpdatedAt:                input.Now,
	}
	return cloneRun(s.run), nil
}

func (s *storeStub) UpdateRunState(_ context.Context, input repository.V2UpdateMigrationRunStateInput) (*domain.V2MigrationRun, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.run == nil || s.run.State != input.FromState {
		return nil, errors.New("stale state")
	}
	s.run.State = input.ToState
	s.run.Phase = input.Phase
	s.run.PreflightApproved = s.run.PreflightApproved || input.PreflightApproved
	if input.BackupReference != "" {
		s.run.BackupReference = input.BackupReference
	}
	if input.PreflightChecks != nil {
		s.run.PreflightChecks = input.PreflightChecks
	}
	s.run.LastError = input.LastError
	s.run.Retryable = input.Retryable
	if input.ToState == domain.V2MigrationStateRunning && s.run.StartedAt == nil {
		started := input.Now
		s.run.StartedAt = &started
	}
	s.run.UpdatedAt = input.Now
	return cloneRun(s.run), nil
}

func (s *storeStub) GetLatestMarker(context.Context) (*domain.V2CompatibilityMarker, error) {
	if s.markerErr != nil {
		return nil, s.markerErr
	}
	if s.marker == nil {
		return nil, nil
	}
	marker := *s.marker
	return &marker, nil
}

func (s *storeStub) RecordOperatorAction(_ context.Context, action domain.V2MigrationOperatorAction) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	s.actions = append(s.actions, action)
	return nil
}

func (s *storeStub) ListOperatorActions(context.Context, string, int) ([]domain.V2MigrationOperatorAction, error) {
	if s.actionsErr != nil {
		return nil, s.actionsErr
	}
	return append([]domain.V2MigrationOperatorAction(nil), s.actions...), nil
}

func cloneRun(run *domain.V2MigrationRun) *domain.V2MigrationRun {
	if run == nil {
		return nil
	}
	out := *run
	if run.PreflightChecks != nil {
		out.PreflightChecks = map[string]any{}
		for key, value := range run.PreflightChecks {
			out.PreflightChecks[key] = value
		}
	}
	return &out
}
