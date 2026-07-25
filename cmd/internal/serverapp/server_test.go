package serverapp

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestActivePlacementLeaseCoversVerifierAndCommitWindow(t *testing.T) {
	if got := activePlacementLease(60, 10); got != 640*time.Second {
		t.Fatalf("lease = %s, want verifier*10 + commit + buffer", got)
	}
	if got := activePlacementLease(120, 20); got != 1250*time.Second {
		t.Fatalf("lease = %s, want verifier*10 + commit + buffer", got)
	}
	if got := activePlacementLease(1, 1); got != 5*time.Minute {
		t.Fatalf("lease = %s, want 5m minimum", got)
	}
}

func TestActiveWorkerCountUsesBoundedVerifierConcurrency(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "missing", in: 0, want: 1},
		{name: "negative", in: -2, want: 1},
		{name: "configured", in: 5, want: 5},
		{name: "clamped", in: 100, want: 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activeWorkerCount(tt.in); got != tt.want {
				t.Fatalf("worker count = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestConflictReviewDueForTeamHonorsLocalStartAndJitter(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	before := time.Date(2026, 7, 25, 3, 59, 59, 0, time.UTC)
	atStart := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)

	if conflictReviewDueForTeam(before, cfg, "team-a") {
		t.Fatalf("conflict review was due before configured local start")
	}
	if !conflictReviewDueForTeam(atStart, cfg, "team-a") {
		t.Fatalf("conflict review was not due at configured local start")
	}

	jittered := testConflictReviewConfig(t, "UTC", "04:00", "600")
	teamID := "team-with-stable-jitter"
	if conflictReviewDueForTeam(atStart, jittered, teamID) {
		t.Fatalf("conflict review was due before stable jitter elapsed")
	}
	if !conflictReviewDueForTeam(atStart.Add(10*time.Minute), jittered, teamID) {
		t.Fatalf("conflict review was not due after max jitter elapsed")
	}
}

func TestProcessConflictReviewTickShardsProfilePages(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "23:59", "0")
	profiles := &conflictReviewProfileListStub{
		pageSizes: map[int]int{
			100: 100,
		},
	}

	processConflictReviewTick(
		context.Background(),
		observability.New(slog.LevelError),
		profiles,
		&conflictReviewLedgerStub{},
		cfg,
		observability.NoopDiscoverabilityMetrics(),
		"worker-2",
		1,
		3,
	)

	if len(profiles.offsets) != 2 || profiles.offsets[0] != 100 || profiles.offsets[1] != 400 {
		t.Fatalf("profile list offsets = %#v, want [100 400]", profiles.offsets)
	}
}

func TestProcessTeamConflictReviewCompletesEmptyRun(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	ledger := &conflictReviewLedgerStub{
		run: &repository.V2ConflictReviewRunRecord{
			TeamID:      "00000000-0000-0000-0000-000000000001",
			ReviewRunID: "00000000-0000-0000-0000-000000000002",
			Status:      "running",
			WorkerID:    "worker-a",
		},
		claimed: true,
	}

	err := processTeamConflictReview(context.Background(), observability.New(slog.LevelError), ledger, cfg, metrics, ledger.run.TeamID, "worker-a", time.Now().UTC())
	if err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.completes) != 1 {
		t.Fatalf("complete calls = %#v", ledger.completes)
	}
	complete := ledger.completes[0]
	if complete.Status != "completed" || complete.ClaimedCases != 0 {
		t.Fatalf("complete input = %#v", complete)
	}
	samples := metrics.MemoryFunnelSamples()
	if len(samples) != 1 || samples[0].Stage != "conflict_review" || samples[0].Outcome != "empty" {
		t.Fatalf("metrics samples = %#v", samples)
	}
}

func TestProcessTeamConflictReviewCountsMixedOutcomes(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	teamID := "00000000-0000-0000-0000-000000000010"
	runID := "00000000-0000-0000-0000-000000000011"
	ledger := &conflictReviewLedgerStub{
		run: &repository.V2ConflictReviewRunRecord{
			TeamID:      teamID,
			ReviewRunID: runID,
			Status:      "running",
			WorkerID:    "worker-b",
		},
		claimed: true,
		claimBatches: [][]repository.V2RelationshipConflictCaseRecord{{
			{ConflictID: "00000000-0000-0000-0000-000000000101"},
			{ConflictID: "00000000-0000-0000-0000-000000000102"},
			{ConflictID: "00000000-0000-0000-0000-000000000103"},
			{ConflictID: "00000000-0000-0000-0000-000000000104"},
		}},
		reviewResults: map[string]*repository.V2ReviewRelationshipConflictCaseResult{
			"00000000-0000-0000-0000-000000000101": {Outcome: repository.V2ConflictReviewOutcomeResolve},
			"00000000-0000-0000-0000-000000000102": {Outcome: repository.V2ConflictReviewOutcomeOverdue},
			"00000000-0000-0000-0000-000000000103": {Outcome: repository.V2ConflictReviewOutcomeNoop},
		},
		reviewErrs: map[string]error{
			"00000000-0000-0000-0000-000000000104": errors.New("case failed"),
		},
	}

	err := processTeamConflictReview(context.Background(), observability.New(slog.LevelError), ledger, cfg, metrics, teamID, "worker-b", time.Now().UTC())
	if err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.completes) != 1 {
		t.Fatalf("complete calls = %#v", ledger.completes)
	}
	complete := ledger.completes[0]
	if complete.Status != "failed" ||
		complete.LastError != "one or more conflict cases failed" ||
		complete.ClaimedCases != 4 ||
		complete.ResolvedCases != 1 ||
		complete.OverdueCases != 1 ||
		complete.NoOpCases != 1 ||
		complete.FailedCases != 1 {
		t.Fatalf("complete input = %#v", complete)
	}
	samples := metrics.MemoryFunnelSamples()
	if len(samples) != 1 || samples[0].Outcome != "partial_error" {
		t.Fatalf("metrics samples = %#v", samples)
	}
}

func testConflictReviewConfig(t *testing.T, timezone string, start string, jitter string) *config.Config {
	t.Helper()
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	t.Setenv("APP_TIMEZONE", timezone)
	t.Setenv("CONFLICT_REVIEW_START_TIME_LOCAL", start)
	t.Setenv("CONFLICT_REVIEW_JITTER_SECONDS", jitter)
	t.Setenv("CONFLICT_REVIEW_LEASE_SECONDS", "60")
	t.Setenv("CONFLICT_REVIEW_BATCH_SIZE", "10")
	t.Setenv("CONFLICT_REVIEW_MAX_ATTEMPTS", "5")
	t.Setenv("CONFLICT_REVIEW_MAX_CONCURRENCY", "1")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	return &cfg
}

type conflictReviewLedgerStub struct {
	run           *repository.V2ConflictReviewRunRecord
	claimed       bool
	reserveErr    error
	claimBatches  [][]repository.V2RelationshipConflictCaseRecord
	claimErr      error
	reviewResults map[string]*repository.V2ReviewRelationshipConflictCaseResult
	reviewErrs    map[string]error
	completes     []repository.V2ConflictReviewRunCompleteInput
	completeErr   error
}

type conflictReviewProfileListStub struct {
	pageSizes map[int]int
	offsets   []int
}

func (s *conflictReviewProfileListStub) List(_ context.Context, _ int, offset int) ([]*domain.Profile, error) {
	s.offsets = append(s.offsets, offset)
	count := s.pageSizes[offset]
	out := make([]*domain.Profile, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, &domain.Profile{ID: uuid.New()})
	}
	return out, nil
}

func (s *conflictReviewLedgerStub) ReserveV2RelationshipConflictReviewRun(context.Context, repository.V2ConflictReviewRunInput) (*repository.V2ConflictReviewRunRecord, bool, error) {
	return s.run, s.claimed, s.reserveErr
}

func (s *conflictReviewLedgerStub) ClaimV2RelationshipConflictCases(context.Context, repository.V2ClaimRelationshipConflictCasesInput) ([]repository.V2RelationshipConflictCaseRecord, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if len(s.claimBatches) == 0 {
		return nil, nil
	}
	batch := s.claimBatches[0]
	s.claimBatches = s.claimBatches[1:]
	return batch, nil
}

func (s *conflictReviewLedgerStub) ReviewV2RelationshipConflictCase(_ context.Context, input repository.V2ReviewRelationshipConflictCaseInput) (*repository.V2ReviewRelationshipConflictCaseResult, error) {
	if err := s.reviewErrs[input.ConflictID]; err != nil {
		return nil, err
	}
	if result := s.reviewResults[input.ConflictID]; result != nil {
		return result, nil
	}
	return &repository.V2ReviewRelationshipConflictCaseResult{Outcome: repository.V2ConflictReviewOutcomeNoop}, nil
}

func (s *conflictReviewLedgerStub) CompleteV2RelationshipConflictReviewRun(_ context.Context, input repository.V2ConflictReviewRunCompleteInput) error {
	s.completes = append(s.completes, input)
	return s.completeErr
}
