package migrationsupervisor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
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
