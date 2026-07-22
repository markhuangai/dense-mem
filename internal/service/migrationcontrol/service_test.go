package migrationcontrol

import (
	"context"
	"errors"
	"strings"
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
		Actor:            "operator",
		BackupsConfirmed: true,
	})
	if err != nil {
		t.Fatalf("ApprovePreflight: %v", err)
	}
	if store.run.State != domain.V2MigrationStateReady || !store.run.PreflightApproved {
		t.Fatalf("run after preflight = %#v", store.run)
	}
	if store.run.BackupReference != "" {
		t.Fatalf("backup reference should not be written, got %q", store.run.BackupReference)
	}
	assertBackupConfirmationChecks(t, store.run.PreflightChecks)

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

func TestPreflightRequiresOperatorBackupConfirmation(t *testing.T) {
	svc := New(newStoreStub(), Config{Required: true})
	_, err := svc.ApprovePreflight(context.Background(), OperatorRequest{})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("ApprovePreflight missing confirmation err = %v", err)
	}

	_, err = svc.ApprovePreflight(context.Background(), OperatorRequest{BackupsConfirmed: false})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("ApprovePreflight false confirmation err = %v", err)
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
		BackupsConfirmed: true,
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
	if got := store.actions[0].Metadata["backup_confirmation"]; got != true {
		t.Fatalf("preflight metadata = %#v", store.actions[0].Metadata)
	}
	checks, ok := store.actions[0].Metadata["preflight_checks"].(map[string]any)
	if !ok {
		t.Fatalf("preflight metadata = %#v", store.actions[0].Metadata)
	}
	assertBackupConfirmationChecks(t, checks)
	assertBackupConfirmationChecks(t, store.run.PreflightChecks)
	if store.run.BackupReference != "" {
		t.Fatalf("backup reference should not be written, got %q", store.run.BackupReference)
	}
	if store.run.MigrationContractVersion != DefaultMigrationContractVersion {
		t.Fatalf("migration contract = %q", store.run.MigrationContractVersion)
	}
}

func TestPreflightRenewsLegacyContractRun(t *testing.T) {
	ctx := context.Background()
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: "dense-mem.v2.1.migration-control.v1",
		CorpusVersion:            DefaultCorpusVersion,
		State:                    domain.V2MigrationStateRunning,
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "legacy-backup-reference",
	}
	svc := New(store, Config{Required: true})

	status, err := svc.ApprovePreflight(ctx, OperatorRequest{
		BackupsConfirmed: true,
	})
	if err != nil {
		t.Fatalf("ApprovePreflight legacy run: %v", err)
	}
	if status.State != domain.V2MigrationStateReady {
		t.Fatalf("status = %#v", status)
	}
	if store.run.MigrationContractVersion != DefaultMigrationContractVersion {
		t.Fatalf("contract = %q", store.run.MigrationContractVersion)
	}
	if store.run.State != domain.V2MigrationStateReady {
		t.Fatalf("run state = %q", store.run.State)
	}
	if store.run.BackupReference != "" {
		t.Fatalf("legacy backup reference should be cleared, got %q", store.run.BackupReference)
	}

	store.run.State = domain.V2MigrationStateRunning
	store.run.MigrationContractVersion = DefaultMigrationContractVersion
	_, err = svc.ApprovePreflight(ctx, OperatorRequest{
		BackupsConfirmed: true,
	})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("current running preflight err = %v", err)
	}

	store.run.PreflightChecks = map[string]any{"operator_backup_confirmation": true}
	status, err = svc.ApprovePreflight(ctx, OperatorRequest{
		BackupsConfirmed: true,
	})
	if err != nil {
		t.Fatalf("ApprovePreflight incomplete current run: %v", err)
	}
	if status.State != domain.V2MigrationStateReady {
		t.Fatalf("status after incomplete current renewal = %#v", status)
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

func TestTransitionPropagatesUpdateAndRecordErrors(t *testing.T) {
	ctx := context.Background()
	want := errors.New("store failed")

	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: DefaultMigrationContractVersion,
		State:                    domain.V2MigrationStateReady,
		Required:                 true,
		PreflightApproved:        true,
		PreflightChecks:          createdPreflightChecks(),
	}
	store.updateErr = want
	_, err := New(store, Config{Required: true}).Start(ctx, OperatorRequest{})
	if !errors.Is(err, want) {
		t.Fatalf("update err = %v", err)
	}

	store = newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: DefaultMigrationContractVersion,
		State:                    domain.V2MigrationStateReady,
		Required:                 true,
		PreflightApproved:        true,
		PreflightChecks:          createdPreflightChecks(),
	}
	store.recordErr = want
	_, err = New(store, Config{Required: true}).Start(ctx, OperatorRequest{})
	if !errors.Is(err, want) {
		t.Fatalf("record err = %v", err)
	}
}

func TestCutoverRequiresReadyRunConfirmationCorpusAndPassingGates(t *testing.T) {
	ctx := context.Background()
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: DefaultMigrationContractVersion,
		State:                    domain.V2MigrationStateReadyCutover,
		Required:                 true,
		PreflightApproved:        true,
		PreflightChecks:          createdPreflightChecks(),
	}
	svc := New(store, Config{
		Required:             true,
		CutoverRequiredGates: []string{"operator_backup_confirmation", "no_neo4j_fallback"},
	})

	_, err := svc.Cutover(ctx, CutoverRequest{
		GateResults: passingCutoverGates("operator_backup_confirmation", "no_neo4j_fallback"),
	})
	if !errors.Is(err, ErrCutoverGate) {
		t.Fatalf("Cutover missing corpus hash err = %v", err)
	}

	store.run.CorpusHash = "sha256:corpus"
	_, err = svc.Cutover(ctx, CutoverRequest{
		CorpusHash:  "sha256:corpus",
		GateResults: passingCutoverGates("operator_backup_confirmation"),
	})
	if !errors.Is(err, ErrCutoverGate) {
		t.Fatalf("Cutover missing gate err = %v", err)
	}

	store.commitErr = repository.ErrV2MigrationCutoverBlocked
	_, err = svc.Cutover(ctx, CutoverRequest{
		CorpusHash:  "sha256:corpus",
		GateResults: passingCutoverGates("operator_backup_confirmation", "no_neo4j_fallback"),
	})
	if !errors.Is(err, ErrCutoverBlocked) {
		t.Fatalf("Cutover repository blocked err = %v", err)
	}
}

func TestCutoverRejectsUnknownGateAndInvalidEvidenceHash(t *testing.T) {
	ctx := context.Background()
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: DefaultMigrationContractVersion,
		State:                    domain.V2MigrationStateReadyCutover,
		Required:                 true,
		PreflightApproved:        true,
		CorpusHash:               "sha256:run-corpus",
		PreflightChecks:          createdPreflightChecks(),
	}
	svc := New(store, Config{
		Required:             true,
		CutoverRequiredGates: []string{"operator_backup_confirmation"},
	})

	_, err := svc.Cutover(ctx, CutoverRequest{
		GateResults: passingCutoverGates("operator_backup_confirmation", "unexpected_gate"),
	})
	if !errors.Is(err, ErrCutoverGate) {
		t.Fatalf("unknown gate err = %v", err)
	}

	gates := passingCutoverGates("operator_backup_confirmation")
	gates[0].EvidenceHash = "sha256:not-a-digest"
	_, err = svc.Cutover(ctx, CutoverRequest{GateResults: gates})
	if !errors.Is(err, ErrCutoverGate) {
		t.Fatalf("invalid hash err = %v", err)
	}
}

func TestCutoverRejectsBlockedStateAndMissingPreflight(t *testing.T) {
	ctx := context.Background()
	gates := passingCutoverGates("operator_backup_confirmation")

	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:             "run-1",
		State:             domain.V2MigrationStateRunning,
		Required:          true,
		PreflightApproved: true,
		CorpusHash:        "sha256:run-corpus",
		PreflightChecks:   createdPreflightChecks(),
	}
	svc := New(store, Config{Required: true, CutoverRequiredGates: []string{"operator_backup_confirmation"}})
	_, err := svc.Cutover(ctx, CutoverRequest{GateResults: gates})
	if !errors.Is(err, ErrCutoverBlocked) {
		t.Fatalf("blocked state err = %v", err)
	}

	store.run.State = domain.V2MigrationStateReadyCutover
	store.run.PreflightApproved = false
	_, err = svc.Cutover(ctx, CutoverRequest{GateResults: gates})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("missing preflight err = %v", err)
	}

	store.run.PreflightApproved = true
	store.run.PreflightChecks = map[string]any{"operator_backup_confirmation": false}
	_, err = svc.Cutover(ctx, CutoverRequest{GateResults: gates})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("missing backup confirmation err = %v", err)
	}
}

func TestCutoverGateValidationRejectsIncompleteGateEvidence(t *testing.T) {
	ctx := context.Background()
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: DefaultMigrationContractVersion,
		State:                    domain.V2MigrationStateReadyCutover,
		Required:                 true,
		PreflightApproved:        true,
		CorpusHash:               "sha256:run-corpus",
		PreflightChecks:          createdPreflightChecks(),
	}
	svc := New(store, Config{Required: true, CutoverRequiredGates: []string{"operator_backup_confirmation"}})

	cases := map[string]func([]domain.V2MigrationGateResult) []domain.V2MigrationGateResult{
		"empty name": func(gates []domain.V2MigrationGateResult) []domain.V2MigrationGateResult {
			gates[0].GateName = " "
			return gates
		},
		"failed outcome": func(gates []domain.V2MigrationGateResult) []domain.V2MigrationGateResult {
			gates[0].Outcome = "failed"
			return gates
		},
		"missing evidence ref": func(gates []domain.V2MigrationGateResult) []domain.V2MigrationGateResult {
			gates[0].EvidenceRef = ""
			return gates
		},
		"missing message": func(gates []domain.V2MigrationGateResult) []domain.V2MigrationGateResult {
			gates[0].Message = ""
			return gates
		},
		"missing version": func(gates []domain.V2MigrationGateResult) []domain.V2MigrationGateResult {
			gates[0].Metadata = map[string]any{"version": ""}
			return gates
		},
		"non-string version": func(gates []domain.V2MigrationGateResult) []domain.V2MigrationGateResult {
			gates[0].Metadata = map[string]any{"version": 2}
			return gates
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Cutover(ctx, CutoverRequest{
				GateResults: mutate(passingCutoverGates("operator_backup_confirmation")),
			})
			if !errors.Is(err, ErrCutoverGate) {
				t.Fatalf("Cutover err = %v", err)
			}
		})
	}

	duplicate := append(passingCutoverGates("operator_backup_confirmation"), passingCutoverGates("operator_backup_confirmation")...)
	_, err := svc.Cutover(ctx, CutoverRequest{GateResults: duplicate})
	if !errors.Is(err, ErrCutoverGate) {
		t.Fatalf("duplicate gate err = %v", err)
	}
}

func TestCutoverPropagatesStoreAndPreflightLookupErrors(t *testing.T) {
	ctx := context.Background()
	want := errors.New("store failed")

	store := newStoreStub()
	_, err := New(store, Config{Required: true}).Cutover(ctx, CutoverRequest{})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("nil run err = %v", err)
	}

	store = newStoreStub()
	store.markerErr = want
	_, err = New(store, Config{Required: true}).Cutover(ctx, CutoverRequest{})
	if !errors.Is(err, want) {
		t.Fatalf("marker err = %v", err)
	}

	store = newStoreStub()
	store.runErr = want
	_, err = New(store, Config{Required: true}).Cutover(ctx, CutoverRequest{})
	if !errors.Is(err, want) {
		t.Fatalf("run err = %v", err)
	}

	store = newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: DefaultMigrationContractVersion,
		State:                    domain.V2MigrationStateReadyCutover,
		Required:                 true,
		PreflightApproved:        true,
		CorpusHash:               "sha256:run-corpus",
		PreflightChecks:          createdPreflightChecks(),
	}
	store.commitErr = want
	_, err = New(store, Config{
		Required:             true,
		CutoverRequiredGates: []string{"operator_backup_confirmation"},
	}).Cutover(ctx, CutoverRequest{
		CorpusHash:  "sha256:run-corpus",
		GateResults: passingCutoverGates("operator_backup_confirmation"),
	})
	if !errors.Is(err, want) {
		t.Fatalf("commit err = %v", err)
	}
}

func TestCutoverCommitsMarkerAndOperatorAction(t *testing.T) {
	now := time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC)
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: DefaultMigrationContractVersion,
		State:                    domain.V2MigrationStateReadyCutover,
		Required:                 true,
		PreflightApproved:        true,
		CorpusHash:               "sha256:run-corpus",
		PreflightChecks:          createdPreflightChecks(),
	}
	svc := New(store, Config{
		Required:             true,
		CutoverRequiredGates: []string{"operator_backup_confirmation"},
		Now:                  func() time.Time { return now },
	})

	status, err := svc.Cutover(context.Background(), CutoverRequest{
		Actor:       "operator",
		RemoteIP:    "192.0.2.55",
		Reason:      "final cutover",
		GateResults: passingCutoverGates("operator_backup_confirmation"),
		Metadata:    map[string]any{"release": "v2.1.1"},
	})

	if err != nil {
		t.Fatalf("Cutover: %v", err)
	}
	if status.State != domain.V2MigrationStateCutOver || !status.DataPlaneAllowed {
		t.Fatalf("cutover status = %#v", status)
	}
	if store.commitInput.MarkerVersion != DefaultCutoverMarkerVersion {
		t.Fatalf("marker version = %q", store.commitInput.MarkerVersion)
	}
	if store.commitInput.CorpusHash != "sha256:run-corpus" {
		t.Fatalf("corpus hash = %q", store.commitInput.CorpusHash)
	}
	if store.commitInput.OperatorAction.Action != domain.V2MigrationActionCutoverCommitted {
		t.Fatalf("operator action = %#v", store.commitInput.OperatorAction)
	}
	if store.commitInput.OperatorAction.Actor != "operator" || store.commitInput.OperatorAction.RemoteIP != "192.0.2.55" {
		t.Fatalf("operator actor = %#v", store.commitInput.OperatorAction)
	}
	if _, ok := store.commitInput.OperatorAction.Metadata["gate_report_hash"]; !ok {
		t.Fatalf("operator metadata missing gate_report_hash: %#v", store.commitInput.OperatorAction.Metadata)
	}
}

func TestCutoverRequiresFinalizedRunCorpusHashAndRejectsMismatch(t *testing.T) {
	store := newStoreStub()
	store.run = &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: DefaultMigrationContractVersion,
		State:                    domain.V2MigrationStateReadyCutover,
		Required:                 true,
		PreflightApproved:        true,
		PreflightChecks:          createdPreflightChecks(),
	}
	svc := New(store, Config{Required: true, CutoverRequiredGates: []string{"operator_backup_confirmation"}})

	_, err := svc.Cutover(context.Background(), CutoverRequest{
		CorpusHash:  "sha256:request-corpus",
		GateResults: passingCutoverGates("operator_backup_confirmation"),
	})
	if !errors.Is(err, ErrCutoverGate) {
		t.Fatalf("Cutover missing finalized corpus hash err = %v", err)
	}

	store.run.CorpusHash = "sha256:run-corpus"
	_, err = svc.Cutover(context.Background(), CutoverRequest{
		CorpusHash:  "sha256:request-corpus",
		GateResults: passingCutoverGates("operator_backup_confirmation"),
	})
	if !errors.Is(err, ErrCutoverGate) {
		t.Fatalf("Cutover mismatched corpus hash err = %v", err)
	}

	_, err = svc.Cutover(context.Background(), CutoverRequest{
		CorpusHash:  "sha256:run-corpus",
		GateResults: passingCutoverGates("operator_backup_confirmation"),
	})
	if err != nil {
		t.Fatalf("Cutover matching corpus hash: %v", err)
	}
	if store.commitInput.CorpusHash != "sha256:run-corpus" {
		t.Fatalf("corpus hash = %q", store.commitInput.CorpusHash)
	}
}

func TestPreflightPropagatesStoreErrors(t *testing.T) {
	ctx := context.Background()
	req := OperatorRequest{
		BackupsConfirmed: true,
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

	store.marker = &domain.V2CompatibilityMarker{Status: "pending"}
	_, err = svc.Pause(ctx, OperatorRequest{})
	if err != nil {
		t.Fatalf("unknown marker status should remain mutable: %v", err)
	}
}

func TestStatusFreshMarkerDoesNotRequestNeo4jDisconnect(t *testing.T) {
	store := newStoreStub()
	store.marker = &domain.V2CompatibilityMarker{
		Status: domain.V2MigrationMarkerCompatible,
		Metadata: map[string]any{
			"fresh_install": true,
		},
	}

	status, err := New(store, Config{}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != domain.V2MigrationStateCutOver || !status.DataPlaneAllowed {
		t.Fatalf("status = %#v", status)
	}
	if status.ReadinessMessage != "fresh V2 authority marker present" {
		t.Fatalf("message = %q", status.ReadinessMessage)
	}
}

func TestStatusFreshMarkerAcceptsStringMetadata(t *testing.T) {
	store := newStoreStub()
	store.marker = &domain.V2CompatibilityMarker{
		Status:   domain.V2MigrationMarkerCompatible,
		Metadata: map[string]any{"fresh_install": " true "},
	}

	status, err := New(store, Config{}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.ReadinessMessage != "fresh V2 authority marker present" {
		t.Fatalf("message = %q", status.ReadinessMessage)
	}
}

func TestStatusCompatibleMarkerRequestsNeo4jDisconnectForCutover(t *testing.T) {
	for name, metadata := range map[string]map[string]any{
		"missing":    nil,
		"false bool": {"fresh_install": false},
		"other type": {"fresh_install": 1},
	} {
		t.Run(name, func(t *testing.T) {
			store := newStoreStub()
			store.marker = &domain.V2CompatibilityMarker{
				Status:   domain.V2MigrationMarkerCompatible,
				Metadata: metadata,
			}

			status, err := New(store, Config{}).Status(context.Background())
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.ReadinessMessage != "compatible V2 migration marker present; neo4j_disconnect_required" {
				t.Fatalf("message = %q", status.ReadinessMessage)
			}
		})
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
		domain.V2MigrationStateReady:        "backup confirmation recorded; start is allowed",
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
			status := statusFromRunMarker(true, DefaultMigrationContractVersion, &domain.V2MigrationRun{
				RunID:                    "run-1",
				MigrationContractVersion: DefaultMigrationContractVersion,
				State:                    state,
				Required:                 true,
				PreflightApproved:        true,
				PreflightChecks:          createdPreflightChecks(),
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

func TestTransitionTableRejectsUnsupportedEdges(t *testing.T) {
	valid := map[string]string{
		domain.V2MigrationStateRequired:     domain.V2MigrationStateReady,
		domain.V2MigrationStatePreflight:    domain.V2MigrationStateFailed,
		domain.V2MigrationStateReady:        domain.V2MigrationStateRunning,
		domain.V2MigrationStateRunning:      domain.V2MigrationStateVerifying,
		domain.V2MigrationStatePaused:       domain.V2MigrationStateRunning,
		domain.V2MigrationStateFailed:       domain.V2MigrationStateRunning,
		domain.V2MigrationStateVerifying:    domain.V2MigrationStateReadyCutover,
		domain.V2MigrationStateReadyCutover: domain.V2MigrationStateCutOver,
	}
	for from, to := range valid {
		if !canTransition(from, to) {
			t.Fatalf("expected transition %s -> %s", from, to)
		}
	}
	if canTransition(domain.V2MigrationStateCutOver, domain.V2MigrationStateRunning) {
		t.Fatal("cutover should not transition back to running")
	}
	if canTransition("unknown", domain.V2MigrationStateRunning) {
		t.Fatal("unknown state should not transition")
	}
}

func TestCutoverGateHashRejectsUnserializableMetadata(t *testing.T) {
	gates := passingCutoverGates("operator_backup_confirmation")
	gates[0].Metadata["bad"] = make(chan struct{})
	_, err := cutoverGateReportHash(gates)
	if !errors.Is(err, ErrCutoverGate) {
		t.Fatalf("hash err = %v", err)
	}
}

func TestValidSHA256RefRejectsMalformedValues(t *testing.T) {
	values := []string{
		"",
		strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("A", 64),
		"sha256:" + strings.Repeat("g", 64),
	}
	for _, value := range values {
		if validSHA256Ref(value) {
			t.Fatalf("value should be invalid: %q", value)
		}
	}
}

type storeStub struct {
	run         *domain.V2MigrationRun
	marker      *domain.V2CompatibilityMarker
	actions     []domain.V2MigrationOperatorAction
	commitInput repository.V2CommitCutoverInput
	runErr      error
	markerErr   error
	actionsErr  error
	createErr   error
	updateErr   error
	recordErr   error
	commitErr   error
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
	if input.MigrationContractVersion != "" {
		s.run.MigrationContractVersion = input.MigrationContractVersion
	}
	if input.CorpusVersion != "" {
		s.run.CorpusVersion = input.CorpusVersion
	}
	s.run.PreflightApproved = s.run.PreflightApproved || input.PreflightApproved
	if input.ClearBackupReference {
		s.run.BackupReference = ""
	}
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

func (s *storeStub) CommitCutover(_ context.Context, input repository.V2CommitCutoverInput) (*domain.V2CompatibilityMarker, error) {
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	s.commitInput = input
	s.run.State = domain.V2MigrationStateCutOver
	s.run.CorpusHash = input.CorpusHash
	completed := input.Now
	s.run.CompletedAt = &completed
	s.run.CutoverAt = &completed
	s.marker = &domain.V2CompatibilityMarker{
		MarkerID:       "marker-1",
		MarkerKind:     domain.V2MigrationMarkerKindCutover,
		Version:        input.MarkerVersion,
		Status:         domain.V2MigrationMarkerCompatible,
		RunID:          input.RunID,
		CorpusHash:     input.CorpusHash,
		GateReportHash: input.GateReportHash,
		Metadata:       input.Metadata,
		CreatedAt:      input.Now,
	}
	return s.GetLatestMarker(context.Background())
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

func passingCutoverGates(names ...string) []domain.V2MigrationGateResult {
	out := make([]domain.V2MigrationGateResult, 0, len(names))
	for _, name := range names {
		out = append(out, domain.V2MigrationGateResult{
			GateName:     name,
			Outcome:      domain.V2MigrationGateOutcomePass,
			EvidenceRef:  "local://" + name,
			EvidenceHash: "sha256:" + strings.Repeat("a", 64),
			Message:      name + " passed",
			Metadata:     map[string]any{"version": "test-v1"},
		})
	}
	return out
}

func createdPreflightChecks() map[string]any {
	return map[string]any{
		"operator_backup_confirmation": true,
		"postgres_backup_confirmed":    true,
		"neo4j_backup_confirmed":       true,
		"confirmation_scope":           "operator",
		"backup_verification":          "not_performed",
	}
}
