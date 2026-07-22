package migrationsupervisor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
	"github.com/markhuangai/dense-mem/internal/service/migrationexecutor"
)

func TestSupervisorProcessesDefaultGatesCutoverAndRequestsRestart(t *testing.T) {
	ctx := context.Background()
	status := supervisorStatus(domain.V2MigrationStateRunning)
	control := &supervisorControlStub{status: status}
	executor := &supervisorExecutorStub{
		results: []*migrationexecutor.RunOnceResult{
			{RunID: "run-1", Fetched: 2},
			{RunID: "run-1", Done: true},
		},
		onCall: func(call int) {
			if call == 2 {
				status.State = domain.V2MigrationStateReadyCutover
				status.Run.State = domain.V2MigrationStateReadyCutover
			}
		},
	}
	restarter := &supervisorRestarterStub{}
	search := &supervisorSearchStub{readiness: readySearchContract()}
	service := New(Config{
		Control:         control,
		Executor:        executor,
		Lock:            &supervisorLockStub{locked: true},
		Restarter:       restarter,
		SearchReadiness: search,
		PageRetryDelays: []time.Duration{0},
	})

	if err := service.tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if executor.calls != 2 {
		t.Fatalf("executor calls = %d, want 2", executor.calls)
	}
	if control.cutoverCalls != 1 {
		t.Fatalf("cutover calls = %d, want 1", control.cutoverCalls)
	}
	if len(restarter.requests) != 1 || restarter.requests[0] == "" {
		t.Fatalf("restart requests = %#v", restarter.requests)
	}
	if got, want := len(control.cutoverReq.GateResults), len(migrationcontrol.DefaultCutoverRequiredGates()); got != want {
		t.Fatalf("gate count = %d, want %d", got, want)
	}
	for _, gate := range control.cutoverReq.GateResults {
		if gate.Outcome != domain.V2MigrationGateOutcomePass {
			t.Fatalf("gate failed: %#v", gate)
		}
		if gate.EvidenceHash == "" || gate.EvidenceRef == "" {
			t.Fatalf("gate missing evidence: %#v", gate)
		}
	}
	if search.calls != 2 {
		t.Fatalf("search readiness calls = %d, want 2", search.calls)
	}
}

func TestSupervisorPausesAfterBoundedRetries(t *testing.T) {
	ctx := context.Background()
	transient := errors.New("neo4j transient timeout")
	control := &supervisorControlStub{status: supervisorStatus(domain.V2MigrationStateRunning)}
	executor := &supervisorExecutorStub{err: transient}
	service := New(Config{
		Control:         control,
		Executor:        executor,
		Lock:            &supervisorLockStub{locked: true},
		PageRetryDelays: []time.Duration{0, 0, 0},
	})

	err := service.tick(ctx)
	if err == nil || !strings.Contains(err.Error(), "paused_retryable after bounded retries") {
		t.Fatalf("tick err = %v", err)
	}
	if executor.calls != 4 {
		t.Fatalf("executor calls = %d, want initial attempt plus 3 retries", executor.calls)
	}
	if control.pauseCalls != 1 {
		t.Fatalf("pause calls = %d, want 1", control.pauseCalls)
	}
	if control.status.State != domain.V2MigrationStatePaused {
		t.Fatalf("status after pause = %#v", control.status)
	}
}

func TestSupervisorRequiresRenewedPreflightForLegacyContract(t *testing.T) {
	ctx := context.Background()
	status := supervisorStatus(domain.V2MigrationStateRunning)
	status.Run.MigrationContractVersion = "dense-mem.v2.1.migration-control.v1"
	control := &supervisorControlStub{status: status}
	executor := &supervisorExecutorStub{}
	service := New(Config{
		Control:  control,
		Executor: executor,
		Lock:     &supervisorLockStub{locked: true},
	})

	if err := service.tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if executor.calls != 0 {
		t.Fatalf("legacy contract should not execute pages, calls = %d", executor.calls)
	}
	got, err := service.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got.RecentErrors) != 1 || !strings.Contains(got.RecentErrors[0], "renew preflight") {
		t.Fatalf("recent errors = %#v", got.RecentErrors)
	}
}

func TestSupervisorFailsClosedForUnknownReleaseGate(t *testing.T) {
	ctx := context.Background()
	service := New(Config{RequiredGates: []string{"unknown_gate"}})
	gates, err := service.evaluateGates(ctx, supervisorStatus(domain.V2MigrationStateReadyCutover))
	if !errors.Is(err, ErrGateBlocked) {
		t.Fatalf("evaluateGates err = %v", err)
	}
	if len(gates) != 1 || gates[0].Outcome != domain.V2MigrationGateOutcomeFail {
		t.Fatalf("gates = %#v", gates)
	}
	if !strings.Contains(gates[0].Message, "no embedded release evidence") {
		t.Fatalf("gate message = %q", gates[0].Message)
	}
}

func TestTelemetryGateRequiresPersistedOperatorActions(t *testing.T) {
	ctx := context.Background()
	service := New(Config{RequiredGates: []string{"telemetry_audit"}})
	status := supervisorStatus(domain.V2MigrationStateReadyCutover)
	status.Actions = nil

	gates, err := service.evaluateGates(ctx, status)
	if !errors.Is(err, ErrGateBlocked) {
		t.Fatalf("evaluateGates missing actions err = %v", err)
	}
	if gates[0].Outcome != domain.V2MigrationGateOutcomeFail {
		t.Fatalf("gate = %#v", gates[0])
	}

	status.Actions = []domain.V2MigrationOperatorAction{{Action: domain.V2MigrationActionStarted}}
	gates, err = service.evaluateGates(ctx, status)
	if err != nil {
		t.Fatalf("evaluateGates with action: %v", err)
	}
	if gates[0].Outcome != domain.V2MigrationGateOutcomePass {
		t.Fatalf("gate = %#v", gates[0])
	}
}

func supervisorStatus(state string) *domain.V2MigrationControlStatus {
	now := time.Date(2026, 7, 22, 7, 43, 0, 0, time.UTC)
	run := &domain.V2MigrationRun{
		RunID:                    "run-1",
		MigrationContractVersion: migrationcontrol.DefaultMigrationContractVersion,
		CorpusVersion:            migrationcontrol.DefaultCorpusVersion,
		SourceKind:               "neo4j",
		State:                    state,
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "sha256:preflight-attestation",
		PreflightChecks: map[string]any{
			"backup_snapshots_created":       true,
			"postgres_backup_created":        true,
			"postgres_backup_reference_hash": "sha256:pg",
			"neo4j_snapshot_created":         true,
			"neo4j_snapshot_reference_hash":  "sha256:neo4j",
			"attestation_scope":              "creation_only",
			"rollback_assurance":             "backup_and_snapshot_creation_attested_restore_not_verified",
		},
		CorpusHash:     "sha256:corpus",
		TotalItems:     3,
		CompletedItems: 3,
		Retryable:      true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	return &domain.V2MigrationControlStatus{
		State:            state,
		Required:         true,
		DataPlaneAllowed: false,
		ReadinessMessage: "migration test",
		Run:              run,
		Actions: []domain.V2MigrationOperatorAction{
			{RunID: run.RunID, Action: domain.V2MigrationActionPreflightApproved},
			{RunID: run.RunID, Action: domain.V2MigrationActionStarted},
		},
	}
}

func readySearchContract() *repository.V2SearchReadiness {
	return &repository.V2SearchReadiness{
		Ready: true,
		Contract: &repository.V2ActiveSearchContract{
			EmbeddingContractID:     "embedding-contract-1",
			SearchIndexGenerationID: "search-generation-1",
			IndexStrategy:           "hnsw",
		},
	}
}

type supervisorControlStub struct {
	status         *domain.V2MigrationControlStatus
	statusErr      error
	preflightErr   error
	startErr       error
	pauseErr       error
	resumeErr      error
	cutoverErr     error
	preflightCalls int
	startCalls     int
	pauseCalls     int
	resumeCalls    int
	cutoverCalls   int
	preflightReq   migrationcontrol.OperatorRequest
	startReq       migrationcontrol.OperatorRequest
	pauseReq       migrationcontrol.OperatorRequest
	resumeReq      migrationcontrol.OperatorRequest
	cutoverReq     migrationcontrol.CutoverRequest
}

func (s *supervisorControlStub) Status(context.Context) (*domain.V2MigrationControlStatus, error) {
	if s.statusErr != nil {
		return nil, s.statusErr
	}
	return s.status, nil
}

func (s *supervisorControlStub) ApprovePreflight(
	_ context.Context,
	req migrationcontrol.OperatorRequest,
) (*domain.V2MigrationControlStatus, error) {
	s.preflightCalls++
	s.preflightReq = req
	if s.preflightErr != nil {
		return nil, s.preflightErr
	}
	return s.status, nil
}

func (s *supervisorControlStub) Start(
	_ context.Context,
	req migrationcontrol.OperatorRequest,
) (*domain.V2MigrationControlStatus, error) {
	s.startCalls++
	s.startReq = req
	if s.startErr != nil {
		return nil, s.startErr
	}
	return s.status, nil
}

func (s *supervisorControlStub) Pause(
	_ context.Context,
	req migrationcontrol.OperatorRequest,
) (*domain.V2MigrationControlStatus, error) {
	s.pauseCalls++
	s.pauseReq = req
	if s.pauseErr != nil {
		return nil, s.pauseErr
	}
	if s.status != nil {
		s.status.State = domain.V2MigrationStatePaused
		if s.status.Run != nil {
			s.status.Run.State = domain.V2MigrationStatePaused
		}
	}
	return s.status, nil
}

func (s *supervisorControlStub) Resume(
	_ context.Context,
	req migrationcontrol.OperatorRequest,
) (*domain.V2MigrationControlStatus, error) {
	s.resumeCalls++
	s.resumeReq = req
	if s.resumeErr != nil {
		return nil, s.resumeErr
	}
	return s.status, nil
}

func (s *supervisorControlStub) Cutover(
	_ context.Context,
	req migrationcontrol.CutoverRequest,
) (*domain.V2MigrationControlStatus, error) {
	s.cutoverCalls++
	s.cutoverReq = req
	if s.cutoverErr != nil {
		return nil, s.cutoverErr
	}
	if s.status != nil {
		s.status.State = domain.V2MigrationStateCutOver
		if s.status.Run != nil {
			s.status.Run.State = domain.V2MigrationStateCutOver
		}
	}
	return s.status, nil
}

type supervisorExecutorStub struct {
	results []*migrationexecutor.RunOnceResult
	err     error
	calls   int
	onCall  func(int)
}

func (s *supervisorExecutorStub) RunOnce(context.Context) (*migrationexecutor.RunOnceResult, error) {
	s.calls++
	if s.onCall != nil {
		s.onCall(s.calls)
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.calls <= len(s.results) {
		return s.results[s.calls-1], nil
	}
	return &migrationexecutor.RunOnceResult{Done: true}, nil
}

type supervisorLockStub struct {
	locked bool
	calls  int
	lease  supervisorLeaseStub
}

func (s *supervisorLockStub) TryLock(context.Context) (interface {
	Release(context.Context) error
}, error) {
	s.calls++
	if !s.locked {
		return nil, nil
	}
	return &s.lease, nil
}

type supervisorLeaseStub struct {
	releases int
}

func (s *supervisorLeaseStub) Release(context.Context) error {
	s.releases++
	return nil
}

type supervisorRestarterStub struct {
	requests []string
}

func (s *supervisorRestarterStub) RequestRestart(reason string) {
	s.requests = append(s.requests, reason)
}

type supervisorSearchStub struct {
	readiness *repository.V2SearchReadiness
	err       error
	calls     int
}

func (s *supervisorSearchStub) CheckSearchReadiness(context.Context) (*repository.V2SearchReadiness, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.readiness, nil
}
