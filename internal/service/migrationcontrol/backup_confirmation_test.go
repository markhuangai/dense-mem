package migrationcontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestStartResumeAndCutoverRequireCurrentBackupConfirmation(t *testing.T) {
	ctx := context.Background()
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: "dense-mem.v2.1.migration-control.v2",
		State:                    domain.V2MigrationStateReady,
		Required:                 true,
		PreflightApproved:        true,
		PreflightChecks:          createdPreflightChecks(),
	}
	svc := New(store, Config{Required: true})
	_, err := svc.Start(ctx, OperatorRequest{})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("Start stale contract err = %v", err)
	}

	store.run.MigrationContractVersion = DefaultMigrationContractVersion
	store.run.PreflightChecks = map[string]any{"operator_backup_confirmation": true}
	_, err = svc.Start(ctx, OperatorRequest{})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("Start incomplete confirmation err = %v", err)
	}

	store.run.State = domain.V2MigrationStatePaused
	store.run.PreflightChecks = createdPreflightChecks()
	status, err := svc.Resume(ctx, OperatorRequest{})
	if err != nil {
		t.Fatalf("Resume current confirmation: %v", err)
	}
	if status.State != domain.V2MigrationStateRunning {
		t.Fatalf("resume status = %#v", status)
	}

	store.run.State = domain.V2MigrationStateReadyCutover
	store.run.CorpusHash = "sha256:run-corpus"
	store.run.MigrationContractVersion = "dense-mem.v2.1.migration-control.v2"
	_, err = svc.Cutover(ctx, CutoverRequest{
		GateResults: passingCutoverGates("operator_backup_confirmation"),
	})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("Cutover stale contract err = %v", err)
	}
}

func TestStatusReportsRenewalWhenBackupConfirmationIsStale(t *testing.T) {
	ctx := context.Background()
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: "dense-mem.v2.1.migration-control.v2",
		State:                    domain.V2MigrationStateReady,
		Required:                 true,
		PreflightApproved:        true,
		PreflightChecks:          createdPreflightChecks(),
	}
	svc := New(store, Config{Required: true})

	status, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status stale contract: %v", err)
	}
	if status.ReadinessMessage != "backup confirmation must be renewed before migration can continue" {
		t.Fatalf("stale contract message = %q", status.ReadinessMessage)
	}

	store.run.MigrationContractVersion = DefaultMigrationContractVersion
	store.run.PreflightChecks = map[string]any{"operator_backup_confirmation": true}
	status, err = svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status incomplete confirmation: %v", err)
	}
	if status.ReadinessMessage != "backup confirmation must be renewed before migration can continue" {
		t.Fatalf("incomplete confirmation message = %q", status.ReadinessMessage)
	}

	store.run.PreflightChecks = createdPreflightChecks()
	status, err = svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status current confirmation: %v", err)
	}
	if status.ReadinessMessage != "backup confirmation recorded; start is allowed" {
		t.Fatalf("current confirmation message = %q", status.ReadinessMessage)
	}
}

func TestPreflightRenewalAllowsReadyRunWithStaleConfirmation(t *testing.T) {
	ctx := context.Background()
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: "dense-mem.v2.1.migration-control.v2",
		State:                    domain.V2MigrationStateReady,
		Required:                 true,
		PreflightApproved:        true,
		PreflightChecks:          createdPreflightChecks(),
	}
	svc := New(store, Config{Required: true})

	status, err := svc.ApprovePreflight(ctx, OperatorRequest{
		BackupsConfirmed: true,
	})
	if err != nil {
		t.Fatalf("ApprovePreflight ready stale run: %v", err)
	}
	if status.State != domain.V2MigrationStateReady {
		t.Fatalf("ready renewal status = %#v", status)
	}
	if store.run.MigrationContractVersion != DefaultMigrationContractVersion {
		t.Fatalf("renewed contract = %q", store.run.MigrationContractVersion)
	}
	assertBackupConfirmationChecks(t, store.run.PreflightChecks)
}

func TestPreflightRenewalKeepsNonRetryableFailedRunTerminal(t *testing.T) {
	ctx := context.Background()
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: "dense-mem.v2.1.migration-control.v2",
		State:                    domain.V2MigrationStateFailed,
		Required:                 true,
		PreflightApproved:        true,
		PreflightChecks:          createdPreflightChecks(),
		Retryable:                false,
	}
	svc := New(store, Config{Required: true})

	status, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status non-retryable failed run: %v", err)
	}
	if status.ReadinessMessage != "migration failed; inspect errors and resume only if retryable" {
		t.Fatalf("non-retryable failed message = %q", status.ReadinessMessage)
	}

	_, err = svc.ApprovePreflight(ctx, OperatorRequest{
		BackupsConfirmed: true,
	})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("ApprovePreflight non-retryable failed err = %v", err)
	}
	if store.run.State != domain.V2MigrationStateFailed || store.run.Retryable {
		t.Fatalf("run should remain non-retryable failed: %#v", store.run)
	}

	store.run.Retryable = true
	status, err = svc.ApprovePreflight(ctx, OperatorRequest{
		BackupsConfirmed: true,
	})
	if err != nil {
		t.Fatalf("ApprovePreflight retryable failed run: %v", err)
	}
	if status.State != domain.V2MigrationStateReady {
		t.Fatalf("retryable failed renewal status = %#v", status)
	}
}

func assertBackupConfirmationChecks(t *testing.T, checks map[string]any) {
	t.Helper()
	want := createdPreflightChecks()
	if len(checks) != len(want) {
		t.Fatalf("preflight checks = %#v", checks)
	}
	for key, value := range want {
		if checks[key] != value {
			t.Fatalf("preflight checks[%s] = %#v, want %#v; all checks = %#v", key, checks[key], value, checks)
		}
	}
}
