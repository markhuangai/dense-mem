package migrationcontrol

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestResumeDefersRepairAssessmentToTransactionalTransition(t *testing.T) {
	now := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: DefaultMigrationContractVersion,
		State:                    domain.V2MigrationStatePaused,
		Required:                 true,
		PreflightApproved:        true,
		PreflightChecks:          createdPreflightChecks(),
		Retryable:                true,
	}
	store.repair = &domain.V2MigrationRepairSummary{
		Required:            true,
		OrphanReviews:       2,
		AbandonedProcessing: 1,
		RetryableFailures:   3,
		RepairedItems:       6,
		ClaimEpochBefore:    5,
		ClaimEpochAfter:     5,
	}
	svc := New(store, Config{Required: true, Now: func() time.Time { return now }})

	status, err := svc.Resume(context.Background(), OperatorRequest{
		Actor:    "operator",
		RemoteIP: "127.0.0.1",
		Reason:   "resume after config change",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if status.State != domain.V2MigrationStateRunning || status.Repair != nil {
		t.Fatalf("status = %#v", status)
	}
	if len(store.actions) != 1 {
		t.Fatalf("actions = %#v", store.actions)
	}
	if store.repairAssessCalls != 0 {
		t.Fatalf("repair assessment calls = %d, want 0", store.repairAssessCalls)
	}
	action := store.actions[0]
	if action.Action != domain.V2MigrationActionRepairResumed || action.Actor != "operator" || action.RemoteIP != "127.0.0.1" {
		t.Fatalf("repair action = %#v", action)
	}
}

func TestResumeReturnsCutoverBlockedForDeterministicRepairBlocker(t *testing.T) {
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: DefaultMigrationContractVersion,
		State:                    domain.V2MigrationStatePaused,
		Required:                 true,
		PreflightApproved:        true,
		PreflightChecks:          createdPreflightChecks(),
		Retryable:                true,
	}
	store.repairErr = fmt.Errorf("%w: blocked_items=1", repository.ErrV2MigrationCutoverBlocked)
	svc := New(store, Config{Required: true})

	_, err := svc.Resume(context.Background(), OperatorRequest{})
	if !errors.Is(err, ErrCutoverBlocked) {
		t.Fatalf("Resume err = %v, want ErrCutoverBlocked", err)
	}
}

func (s *storeStub) AssessMigrationRepair(context.Context, string) (*domain.V2MigrationRepairSummary, error) {
	s.repairAssessCalls++
	if s.repairErr != nil {
		return nil, s.repairErr
	}
	if s.repair != nil {
		repair := *s.repair
		return &repair, nil
	}
	return &domain.V2MigrationRepairSummary{}, nil
}

func (s *storeStub) RepairAndResumeMigration(_ context.Context, input repository.V2RepairAndResumeMigrationInput) (*domain.V2MigrationRun, error) {
	if s.repairErr != nil {
		return nil, s.repairErr
	}
	if s.run == nil || s.run.State != input.FromState {
		return nil, errors.New("stale state")
	}
	s.run.State = domain.V2MigrationStateRunning
	s.run.Phase = "migration"
	s.run.LastError = ""
	s.run.Retryable = true
	if s.run.StartedAt == nil {
		started := input.Now
		s.run.StartedAt = &started
	}
	s.run.UpdatedAt = input.Now
	if input.OperatorAction.Action == "" {
		input.OperatorAction.Action = domain.V2MigrationActionResumed
		if s.repair != nil && s.repair.Required {
			input.OperatorAction.Action = domain.V2MigrationActionRepairResumed
		}
	}
	if input.OperatorAction.Action != "" {
		s.actions = append(s.actions, input.OperatorAction)
	}
	return cloneRun(s.run), nil
}
