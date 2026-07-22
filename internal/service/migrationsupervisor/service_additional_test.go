package migrationsupervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
	"github.com/markhuangai/dense-mem/internal/service/migrationexecutor"
)

func TestSupervisorStartBackgroundRunsLoopAndStop(t *testing.T) {
	ctx := context.Background()
	control := &supervisorControlStub{
		status: supervisorStatus(domain.V2MigrationStateCutOver),
	}
	service := New(Config{
		Control:      control,
		Executor:     &supervisorExecutorStub{},
		Lock:         &supervisorLockStub{locked: true},
		Restarter:    &supervisorRestarterStub{},
		PollInterval: time.Hour,
	})

	if err := service.StartBackground(ctx); err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	defer service.Stop()
	if err := service.StartBackground(ctx); err != nil {
		t.Fatalf("second StartBackground: %v", err)
	}

	deadline := time.After(500 * time.Millisecond)
	for {
		status, err := service.Status(ctx)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.RestartPending {
			break
		}
		select {
		case <-deadline:
			t.Fatal("restart pending was not reported")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	service.Stop()
	service.Wake()
}

func TestSupervisorStartBackgroundRequiresDependencies(t *testing.T) {
	service := New(Config{})

	if err := service.StartBackground(context.Background()); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("StartBackground err = %v", err)
	}
	if _, err := service.Status(context.Background()); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("Status err = %v", err)
	}
}

func TestSupervisorWaitsAfterSourceExhaustedWhileRunStillRunning(t *testing.T) {
	ctx := context.Background()
	control := &supervisorControlStub{status: supervisorStatus(domain.V2MigrationStateRunning)}
	executor := &supervisorExecutorStub{
		results: []*migrationexecutor.RunOnceResult{{RunID: "run-1", Done: true}},
	}
	service := New(Config{
		Control:         control,
		Executor:        executor,
		Lock:            &supervisorLockStub{locked: true},
		PageRetryDelays: []time.Duration{0},
	})

	if err := service.tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if control.statusCalls != 1 {
		t.Fatalf("status calls = %d, want 1", control.statusCalls)
	}
	if control.cutoverCalls != 0 {
		t.Fatalf("cutover calls = %d, want 0", control.cutoverCalls)
	}
}

func TestSupervisorStatusExposesSanitizedLoopError(t *testing.T) {
	ctx := context.Background()
	rawErr := errors.New("postgres: password=secret SELECT * FROM credentials")
	control := &supervisorControlStub{status: supervisorStatus(domain.V2MigrationStateRunning)}
	service := New(Config{
		Control:         control,
		Executor:        &supervisorExecutorStub{err: rawErr},
		Lock:            &supervisorLockStub{locked: true},
		PageRetryDelays: []time.Duration{0},
	})

	err := service.tick(ctx)
	if err == nil {
		t.Fatal("tick err = nil")
	}
	service.setLastError(publicSupervisorError(err))
	status, err := service.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.RecentErrors) != 1 {
		t.Fatalf("recent errors = %#v", status.RecentErrors)
	}
	if status.RecentErrors[0] != supervisorInternalErrorMessage {
		t.Fatalf("recent error = %q", status.RecentErrors[0])
	}
	if strings.Contains(status.RecentErrors[0], "secret") || strings.Contains(status.RecentErrors[0], "credentials") {
		t.Fatalf("recent error leaked raw detail: %q", status.RecentErrors[0])
	}
}

func TestSupervisorOperatorMethodsDelegateAndWake(t *testing.T) {
	ctx := context.Background()
	control := &supervisorControlStub{status: supervisorStatus(domain.V2MigrationStateReady)}
	service := New(Config{Control: control})

	drainWake := func(action string) {
		t.Helper()
		select {
		case <-service.wake:
		default:
			t.Fatalf("%s did not wake the supervisor", action)
		}
	}

	req := migrationcontrol.OperatorRequest{Reason: "operator confirmed"}
	if _, err := service.ApprovePreflight(ctx, req); err != nil {
		t.Fatalf("ApprovePreflight: %v", err)
	}
	drainWake("ApprovePreflight")
	if control.preflightCalls != 1 || control.preflightReq.Reason != req.Reason {
		t.Fatalf("preflight call = %d req = %#v", control.preflightCalls, control.preflightReq)
	}

	if _, err := service.Start(ctx, req); err != nil {
		t.Fatalf("Start: %v", err)
	}
	drainWake("Start")
	if control.startCalls != 1 || control.startReq.Reason != req.Reason {
		t.Fatalf("start call = %d req = %#v", control.startCalls, control.startReq)
	}

	if _, err := service.Resume(ctx, req); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	drainWake("Resume")
	if control.resumeCalls != 1 || control.resumeReq.Reason != req.Reason {
		t.Fatalf("resume call = %d req = %#v", control.resumeCalls, control.resumeReq)
	}

	if _, err := service.Pause(ctx, req); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if control.pauseCalls != 1 || control.pauseReq.Reason != req.Reason {
		t.Fatalf("pause call = %d req = %#v", control.pauseCalls, control.pauseReq)
	}

	cutoverReq := migrationcontrol.CutoverRequest{CorpusHash: "sha256:corpus"}
	if _, err := service.Cutover(ctx, cutoverReq); err != nil {
		t.Fatalf("Cutover: %v", err)
	}
	if control.cutoverCalls != 1 || control.cutoverReq.CorpusHash != cutoverReq.CorpusHash {
		t.Fatalf("cutover call = %d req = %#v", control.cutoverCalls, control.cutoverReq)
	}
}

func TestSupervisorOperatorMethodsReturnMissingDependency(t *testing.T) {
	ctx := context.Background()
	service := New(Config{})
	req := migrationcontrol.OperatorRequest{}

	if _, err := service.ApprovePreflight(ctx, req); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("ApprovePreflight err = %v", err)
	}
	if _, err := service.Start(ctx, req); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("Start err = %v", err)
	}
	if _, err := service.Pause(ctx, req); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("Pause err = %v", err)
	}
	if _, err := service.Resume(ctx, req); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("Resume err = %v", err)
	}
	if _, err := service.Cutover(ctx, migrationcontrol.CutoverRequest{}); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("Cutover err = %v", err)
	}
}

func TestProviderReadinessUsesEmptyCorpusProbe(t *testing.T) {
	ctx := context.Background()
	status := supervisorStatus(domain.V2MigrationStateReadyCutover)
	status.Run.TotalItems = 0
	status.Run.CompletedItems = 0
	service := New(Config{
		ProviderProbe: &supervisorProviderProbeStub{
			evidence: &GateEvidence{
				Ref:     "dense-mem://migration/runtime/provider-empty-corpus",
				Message: "empty corpus provider probe succeeded",
			},
		},
		RequiredGates: []string{"provider_readiness"},
	})

	gates, err := service.evaluateGates(ctx, status)
	if err != nil {
		t.Fatalf("evaluateGates: %v", err)
	}
	if len(gates) != 1 || gates[0].Outcome != domain.V2MigrationGateOutcomePass {
		t.Fatalf("gates = %#v", gates)
	}
	if gates[0].Metadata["version"] != migrationGateEvidenceVersion {
		t.Fatalf("gate metadata = %#v", gates[0].Metadata)
	}
}

func TestProviderReadinessFailsClosedForUnavailableProbe(t *testing.T) {
	ctx := context.Background()
	status := supervisorStatus(domain.V2MigrationStateReadyCutover)
	status.Run.TotalItems = 0
	status.Run.CompletedItems = 0
	service := New(Config{RequiredGates: []string{"provider_readiness"}})

	gates, err := service.evaluateGates(ctx, status)
	if !errors.Is(err, ErrGateBlocked) {
		t.Fatalf("evaluateGates err = %v", err)
	}
	if !strings.Contains(gates[0].Message, "empty corpus provider probe is unavailable") {
		t.Fatalf("gate message = %q", gates[0].Message)
	}
}

func TestProviderReadinessFailsClosedWhenProbeReturnsNoEvidence(t *testing.T) {
	ctx := context.Background()
	status := supervisorStatus(domain.V2MigrationStateReadyCutover)
	status.Run.TotalItems = 0
	status.Run.CompletedItems = 0
	service := New(Config{
		ProviderProbe: &supervisorProviderProbeStub{},
		RequiredGates: []string{"provider_readiness"},
	})

	gates, err := service.evaluateGates(ctx, status)
	if !errors.Is(err, ErrGateBlocked) {
		t.Fatalf("evaluateGates err = %v", err)
	}
	if !strings.Contains(gates[0].Message, "returned no evidence") {
		t.Fatalf("gate message = %q", gates[0].Message)
	}
}

func TestProviderReadinessSanitizesRawProbeErrors(t *testing.T) {
	ctx := context.Background()
	status := supervisorStatus(domain.V2MigrationStateReadyCutover)
	status.Run.TotalItems = 0
	status.Run.CompletedItems = 0
	service := New(Config{
		ProviderProbe: &supervisorProviderProbeStub{
			err: errors.New("provider response contained token=secret"),
		},
		RequiredGates: []string{"provider_readiness"},
	})

	gates, err := service.evaluateGates(ctx, status)
	if err == nil {
		t.Fatal("evaluateGates err = nil")
	}
	if len(gates) != 1 || gates[0].Message != gateInternalErrorMessage {
		t.Fatalf("gates = %#v", gates)
	}
	if strings.Contains(gates[0].Message, "secret") || strings.Contains(gates[0].Message, "provider response") {
		t.Fatalf("gate message leaked raw detail: %q", gates[0].Message)
	}
}

func TestProviderReadinessFailsClosedForNonemptyCorpusWithoutPlacement(t *testing.T) {
	ctx := context.Background()
	status := supervisorStatus(domain.V2MigrationStateReadyCutover)
	status.Run.TotalItems = 1
	status.Run.CompletedItems = 0
	service := New(Config{RequiredGates: []string{"provider_readiness"}})

	gates, err := service.evaluateGates(ctx, status)
	if !errors.Is(err, ErrGateBlocked) {
		t.Fatalf("evaluateGates err = %v", err)
	}
	if !strings.Contains(gates[0].Message, "no successful provider-backed placement") {
		t.Fatalf("gate message = %q", gates[0].Message)
	}
}

func TestSearchGateFailsClosedWhenReadinessUnavailable(t *testing.T) {
	ctx := context.Background()
	service := New(Config{RequiredGates: []string{"vector_contract_current"}})

	gates, err := service.evaluateGates(ctx, supervisorStatus(domain.V2MigrationStateReadyCutover))
	if !errors.Is(err, ErrGateBlocked) {
		t.Fatalf("evaluateGates err = %v", err)
	}
	if !strings.Contains(gates[0].Message, "search readiness check is unavailable") {
		t.Fatalf("gate message = %q", gates[0].Message)
	}
}

func TestSearchGateFailsClosedWhenReadinessFails(t *testing.T) {
	ctx := context.Background()
	service := New(Config{
		SearchReadiness: &supervisorSearchStub{},
		RequiredGates:   []string{"search_index_backlog"},
	})

	gates, err := service.evaluateGates(ctx, supervisorStatus(domain.V2MigrationStateReadyCutover))
	if !errors.Is(err, ErrGateBlocked) {
		t.Fatalf("evaluateGates err = %v", err)
	}
	if !strings.Contains(gates[0].Message, "search readiness failed") {
		t.Fatalf("gate message = %q", gates[0].Message)
	}
}

func TestEvaluateGatesReportsAllFailures(t *testing.T) {
	ctx := context.Background()
	status := supervisorStatus(domain.V2MigrationStateReadyCutover)
	status.Actions = nil
	service := New(Config{RequiredGates: []string{"unknown_gate", "telemetry_audit"}})

	gates, err := service.evaluateGates(ctx, status)
	if !errors.Is(err, ErrGateBlocked) {
		t.Fatalf("evaluateGates err = %v", err)
	}
	if len(gates) != 2 {
		t.Fatalf("gate count = %d, want 2: %#v", len(gates), gates)
	}
	for _, gate := range gates {
		if gate.Outcome != domain.V2MigrationGateOutcomeFail {
			t.Fatalf("gate outcome = %#v", gate)
		}
	}
}

func TestGateResultFailsClosedWhenEvidenceMetadataCannotHash(t *testing.T) {
	gate, err := gateResult("bad_metadata", GateEvidence{
		Ref:     "dense-mem://migration/test/bad-metadata",
		Message: "bad metadata",
		Details: map[string]any{"bad": func() {}},
	}, nil)

	if !errors.Is(err, ErrGateBlocked) {
		t.Fatalf("gateResult err = %v", err)
	}
	if gate.Outcome != domain.V2MigrationGateOutcomeFail {
		t.Fatalf("gate = %#v", gate)
	}
	if gate.EvidenceHash == "" {
		t.Fatalf("gate missing fallback hash: %#v", gate)
	}
	if gate.Message != "migration gate evidence metadata is not serializable" {
		t.Fatalf("gate message = %q", gate.Message)
	}
	if gate.Metadata["evidence_hash_error"] != "metadata_not_serializable" {
		t.Fatalf("gate metadata = %#v", gate.Metadata)
	}
}

func TestStaticReleaseEvidenceSourcesExist(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	for gate, entry := range staticReleaseEvidence {
		for _, source := range entry.sources {
			if _, err := os.Stat(filepath.Join(repoRoot, source)); err != nil {
				t.Fatalf("gate %s source %s does not exist: %v", gate, source, err)
			}
		}
	}
}

type supervisorProviderProbeStub struct {
	evidence *GateEvidence
	err      error
	calls    int
}

func (s *supervisorProviderProbeStub) Probe(context.Context) (*GateEvidence, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.evidence, nil
}
