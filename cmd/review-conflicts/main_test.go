package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestParseCLIRejectsInvalidTimezone(t *testing.T) {
	_, err := parseCLI([]string{
		"--team-id", "00000000-0000-0000-0000-000000000001",
		"--timezone", "Mars/Base",
	}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "--timezone is invalid") {
		t.Fatalf("parseCLI error = %v, want invalid timezone", err)
	}
}

func TestParseCLIDefaultsTimezoneFromAppTimezone(t *testing.T) {
	t.Setenv("APP_TIMEZONE", "America/New_York")

	cfg, err := parseCLI([]string{
		"--team-id", "00000000-0000-0000-0000-000000000001",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseCLI returned error: %v", err)
	}
	if cfg.timezone != "America/New_York" {
		t.Fatalf("timezone = %q, want America/New_York", cfg.timezone)
	}
}

func TestParseCLIDefaultsTimezoneToLocal(t *testing.T) {
	t.Setenv("APP_TIMEZONE", "")

	cfg, err := parseCLI([]string{
		"--team-id", "00000000-0000-0000-0000-000000000001",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseCLI returned error: %v", err)
	}
	if cfg.timezone != "Local" {
		t.Fatalf("timezone = %q, want Local", cfg.timezone)
	}
	if cfg.timeoutSecs != reviewConflictDefaultTimeout {
		t.Fatalf("timeoutSecs = %d, want %d", cfg.timeoutSecs, reviewConflictDefaultTimeout)
	}
	if cfg.timeoutSecs <= cfg.leaseSeconds {
		t.Fatalf("timeoutSecs = %d, want greater than leaseSeconds %d", cfg.timeoutSecs, cfg.leaseSeconds)
	}
}

func TestParseCLIParsesTimeout(t *testing.T) {
	cfg, err := parseCLI([]string{
		"--team-id", "00000000-0000-0000-0000-000000000001",
		"--timeout-seconds", "600",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseCLI returned error: %v", err)
	}
	if cfg.timeoutSecs != 600 {
		t.Fatalf("timeoutSecs = %d, want 600", cfg.timeoutSecs)
	}
}

func TestParseCLIRejectsOutOfRangeConflictReviewFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "batch too low",
			args: []string{"--batch-size", "0"},
			want: "--batch-size must be between 1 and 500",
		},
		{
			name: "batch too high",
			args: []string{"--batch-size", "501"},
			want: "--batch-size must be between 1 and 500",
		},
		{
			name: "lease too low",
			args: []string{"--lease-seconds", "29"},
			want: "--lease-seconds must be between 30 and 1800",
		},
		{
			name: "lease too high",
			args: []string{"--lease-seconds", "1801"},
			want: "--lease-seconds must be between 30 and 1800",
		},
		{
			name: "attempts too low",
			args: []string{"--max-attempts", "0"},
			want: "--max-attempts must be between 1 and 20",
		},
		{
			name: "attempts too high",
			args: []string{"--max-attempts", "21"},
			want: "--max-attempts must be between 1 and 20",
		},
		{
			name: "timeout too low",
			args: []string{"--timeout-seconds", "0"},
			want: "--timeout-seconds must be between 1 and 86400",
		},
		{
			name: "timeout too high",
			args: []string{"--timeout-seconds", "86401"},
			want: "--timeout-seconds must be between 1 and 86400",
		},
		{
			name: "timeout equals lease",
			args: []string{"--lease-seconds", "300", "--timeout-seconds", "300"},
			want: "--timeout-seconds must be greater than --lease-seconds",
		},
		{
			name: "timeout below lease",
			args: []string{"--lease-seconds", "301", "--timeout-seconds", "300"},
			want: "--timeout-seconds must be greater than --lease-seconds",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"--team-id", "00000000-0000-0000-0000-000000000001"}
			args = append(args, tt.args...)

			_, err := parseCLI(args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseCLI error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestConflictVerifierConfigurationErrorUsesResolvedFallbacks(t *testing.T) {
	configured := config.Config{
		AIAPIURL:        "https://shared.example.test/v1",
		AIAPIKey:        "shared-key",
		AIVerifierModel: "reviewer-model",
	}
	if err := conflictVerifierConfigurationError(&configured); err != nil {
		t.Fatalf("conflictVerifierConfigurationError returned error for shared verifier configuration: %v", err)
	}

	missing := config.Config{AIVerifierModel: "reviewer-model"}
	err := conflictVerifierConfigurationError(&missing)
	if err == nil {
		t.Fatal("conflictVerifierConfigurationError returned nil for missing endpoint and key")
	}
	if !strings.Contains(err.Error(), "AI_VERIFIER_API_URL or AI_API_URL") ||
		!strings.Contains(err.Error(), "AI_VERIFIER_API_KEY or AI_API_KEY") ||
		strings.Contains(err.Error(), "AI_VERIFIER_MODEL") {
		t.Fatalf("conflictVerifierConfigurationError = %q", err)
	}
}

func TestReviewTeamConflictsCompletesRunAfterParentContextDeadline(t *testing.T) {
	ledger := &reviewConflictLedgerStub{
		run: &repository.ConflictReviewRunRecord{
			TeamID:      "00000000-0000-0000-0000-000000000001",
			ReviewRunID: "00000000-0000-0000-0000-000000000002",
			Status:      "running",
			WorkerID:    "worker-a",
		},
		claimed: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := reviewTeamConflicts(ctx, ledger, cliConfig{
		teamID:       ledger.run.TeamID,
		workerID:     "worker-a",
		batchSize:    1,
		leaseSeconds: 30,
		maxAttempts:  1,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("reviewTeamConflicts returned error: %v", err)
	}
	if out.Status != "completed" {
		t.Fatalf("review output status = %q, want completed", out.Status)
	}
	if len(ledger.completionContextErrors) != 1 || ledger.completionContextErrors[0] != nil {
		t.Fatalf("completion context errors = %#v, want [nil]", ledger.completionContextErrors)
	}
}

type reviewConflictLedgerStub struct {
	run                     *repository.ConflictReviewRunRecord
	claimed                 bool
	completionContextErrors []error
}

func (s *reviewConflictLedgerStub) ReserveRelationshipConflictReviewRun(context.Context, repository.ConflictReviewRunInput) (*repository.ConflictReviewRunRecord, bool, error) {
	return s.run, s.claimed, nil
}

func (*reviewConflictLedgerStub) ClaimRelationshipConflictCases(context.Context, repository.ClaimRelationshipConflictCasesInput) ([]repository.RelationshipConflictCaseRecord, error) {
	return nil, nil
}

func (*reviewConflictLedgerStub) ReviewRelationshipConflictCase(context.Context, repository.ReviewRelationshipConflictCaseInput) (*repository.ReviewRelationshipConflictCaseResult, error) {
	return nil, nil
}

func (*reviewConflictLedgerStub) ProcessPendingConflictDerivedEvidence(context.Context, repository.ClaimConflictDerivedEvidenceTasksInput) (int, error) {
	return 0, nil
}

func (s *reviewConflictLedgerStub) CompleteRelationshipConflictReviewRun(ctx context.Context, _ repository.ConflictReviewRunCompleteInput) error {
	s.completionContextErrors = append(s.completionContextErrors, ctx.Err())
	return nil
}
