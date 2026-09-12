package conflictreview

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/config"
	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestConflictReviewDueForTeamHonorsLocalStartAndJitter(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	before := time.Date(2026, 7, 25, 3, 59, 59, 0, time.UTC)
	atStart := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	if ConflictReviewDueForTeam(before, cfg, "team-a") {
		t.Fatalf("conflict review was due before configured local start")
	}
	if !ConflictReviewDueForTeam(atStart, cfg, "team-a") {
		t.Fatalf("conflict review was not due at configured local start")
	}
	jittered := testConflictReviewConfig(t, "UTC", "04:00", "600")
	teamID := "team-with-stable-jitter"
	if ConflictReviewDueForTeam(atStart, jittered, teamID) {
		t.Fatalf("conflict review was due before stable jitter elapsed")
	}
	if !ConflictReviewDueForTeam(atStart.Add(10*time.Minute), jittered, teamID) {
		t.Fatalf("conflict review was not due after max jitter elapsed")
	}
	if ConflictReviewDueForTeam(time.Now(), &config.Config{AppTimezone: "UTC", ConflictReviewStartTimeLocal: "not-a-time"}, teamID) {
		t.Fatalf("conflict review was due with an invalid start time")
	}
	if !ConflictReviewDueForTeam(time.Date(2026, 7, 25, 5, 0, 0, 0, time.UTC), &config.Config{
		AppTimezone:                  "UTC",
		ConflictReviewStartTimeLocal: "04:00",
		ConflictReviewJitterSeconds:  7200,
	}, teamID) {
		t.Fatalf("conflict review was not due after capped jitter window")
	}
	if !ConflictReviewDueForTeam(time.Now(), &config.Config{ConflictReviewStartTimeLocal: "00:00"}, teamID) {
		t.Fatalf("conflict review was not due with the local timezone fallback")
	}
	if !ConflictReviewDueForTeam(time.Now(), &config.Config{AppTimezone: "invalid/timezone", ConflictReviewStartTimeLocal: "00:00"}, teamID) {
		t.Fatalf("conflict review was not due with the invalid timezone fallback")
	}
}

func TestProcessConflictReviewTickShardsProfilePages(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "23:59", "0")
	profiles := &conflictReviewProfileListStub{
		pageSizes: map[int]int{
			100: 100,
		},
	}
	ProcessConflictReviewTick(
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
	normalized := &conflictReviewProfileListStub{}
	ProcessConflictReviewTick(context.Background(), nil, normalized, &conflictReviewLedgerStub{}, cfg, nil, "worker-normalized", -1, 0)
	if len(normalized.offsets) != 1 || normalized.offsets[0] != 0 {
		t.Fatalf("normalized worker offset = %#v, want [0]", normalized.offsets)
	}
}

func TestReviewServiceJoinsAllWorkersOnCancellation(t *testing.T) {
	testConflictReviewConfig(t, "UTC", "04:00", "0")
	t.Setenv("CONFLICT_REVIEW_MAX_CONCURRENCY", "3")
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	teams := &blockingTeamLister{started: make(chan struct{}, 3)}
	service := NewReviewService(teams, &conflictReviewLedgerStub{}, &loaded, observability.New(slog.LevelError), observability.NoopDiscoverabilityMetrics())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()
	for range 3 {
		select {
		case <-teams.started:
		case <-time.After(time.Second):
			t.Fatal("conflict-review worker did not begin")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("conflict-review workers were not joined")
	}
}

func TestNewReviewServiceDefaultsWithoutDependencies(t *testing.T) {
	service := NewReviewService(nil, nil, nil, nil, nil)
	if service == nil {
		t.Fatal("NewReviewService returned nil")
	}
	if service.count != 1 {
		t.Fatalf("default worker count = %d, want 1", service.count)
	}
	if service.metrics == nil {
		t.Fatal("NewReviewService did not install default metrics")
	}
	service.Run(context.Background())
	bounded := NewReviewService(nil, nil, &config.Config{ConflictReviewMaxConcurrency: 99}, nil, nil)
	if bounded.count != 16 {
		t.Fatalf("worker count = %d, want 16", bounded.count)
	}
}

func TestSafeConflictReviewErrorIsBounded(t *testing.T) {
	if got := SafeConflictReviewError(nil); got != "" {
		t.Fatalf("SafeConflictReviewError(nil) = %q, want empty", got)
	}
	if got := SafeConflictReviewError(errors.New("raw database detail")); got != "conflict review repository operation failed" {
		t.Fatalf("SafeConflictReviewError(raw) = %q, want bounded message", got)
	}
}

func TestProcessConflictReviewTickLogsBoundedErrors(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "00:00", "0")
	rawErr := errors.New("raw database error with internal detail")
	profileLogger := &conflictReviewLogCapture{}
	ProcessConflictReviewTick(
		context.Background(),
		profileLogger,
		&conflictReviewProfileListStub{err: rawErr},
		&conflictReviewLedgerStub{},
		cfg,
		observability.NoopDiscoverabilityMetrics(),
		"worker-a",
		0,
		1,
	)
	if len(profileLogger.errs) != 1 || !errors.Is(profileLogger.errs[0], ErrConflictReviewTeamListFailed) {
		t.Fatalf("profile list logged errors = %#v", profileLogger.errs)
	}
	if errors.Is(profileLogger.errs[0], rawErr) {
		t.Fatalf("profile list logged raw error")
	}
	runLogger := &conflictReviewLogCapture{}
	ProcessConflictReviewTick(
		context.Background(),
		runLogger,
		&conflictReviewProfileListStub{pageSizes: map[int]int{0: 1}},
		&conflictReviewLedgerStub{reserveErr: rawErr},
		cfg,
		observability.NoopDiscoverabilityMetrics(),
		"worker-a",
		0,
		1,
	)
	if len(runLogger.errs) != 1 || !errors.Is(runLogger.errs[0], ErrConflictReviewRunFailed) {
		t.Fatalf("run logged errors = %#v", runLogger.errs)
	}
	if errors.Is(runLogger.errs[0], rawErr) {
		t.Fatalf("run logged raw error")
	}
}
func TestProcessTeamConflictReviewCompletesEmptyRun(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	ledger := &conflictReviewLedgerStub{
		run: &conflictcontract.ConflictReviewRunRecord{
			TeamID:      "00000000-0000-0000-0000-000000000001",
			ReviewRunID: "00000000-0000-0000-0000-000000000002",
			Status:      "running",
			WorkerID:    "worker-a",
		},
		claimed: true,
	}
	err := ProcessTeamConflictReview(context.Background(), observability.New(slog.LevelError), ledger, cfg, metrics, ledger.run.TeamID, "worker-a", time.Now().UTC())
	if err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.completes) != 1 {
		t.Fatalf("complete calls = %#v", ledger.completes)
	}
	if len(ledger.derivedInputs) != 1 || ledger.derivedInputs[0] != (conflictcontract.ClaimConflictDerivedEvidenceTasksInput{
		TeamID:      ledger.run.TeamID,
		ReviewRunID: ledger.run.ReviewRunID,
		WorkerID:    "worker-a",
		Limit:       cfg.GetConflictReviewBatchSize(),
		Lease:       time.Duration(cfg.GetConflictReviewLeaseSeconds()) * time.Second,
	}) {
		t.Fatalf("derived evidence retry inputs = %#v", ledger.derivedInputs)
	}
	complete := ledger.completes[0]
	if complete.Status != "completed" || complete.ClaimedCases != 0 {
		t.Fatalf("complete input = %#v", complete)
	}
	samples := metrics.ConflictReviewSamples()
	if len(samples) != 1 || samples[0].Outcome != "empty" {
		t.Fatalf("metrics samples = %#v", samples)
	}
}

func TestProcessTeamConflictReviewSkipsCompletedRun(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	ledger := &conflictReviewLedgerStub{
		run: &conflictcontract.ConflictReviewRunRecord{
			TeamID:      "00000000-0000-0000-0000-000000000003",
			ReviewRunID: "00000000-0000-0000-0000-000000000004",
			Status:      "completed",
			WorkerID:    "worker-completed",
		},
		claimed: true,
	}
	if err := ProcessTeamConflictReview(context.Background(), nil, ledger, cfg, nil, ledger.run.TeamID, ledger.run.WorkerID, time.Now().UTC()); err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.completes) != 0 {
		t.Fatalf("complete calls = %#v, want none", ledger.completes)
	}
}

func TestProcessTeamConflictReviewRecordsDerivedEvidenceRetryFailure(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	ledger := &conflictReviewLedgerStub{
		run: &conflictcontract.ConflictReviewRunRecord{
			TeamID:      "00000000-0000-0000-0000-000000000020",
			ReviewRunID: "00000000-0000-0000-0000-000000000021",
			Status:      "running",
			WorkerID:    "worker-derived-failure",
		},
		claimed:    true,
		derivedErr: errors.New("derived evidence task failed"),
		claimBatches: [][]conflictcontract.RelationshipConflictCaseRecord{{
			{ConflictID: "00000000-0000-0000-0000-000000000022"},
		}},
		reviewResults: map[string]*conflictcontract.ReviewRelationshipConflictCaseResult{
			"00000000-0000-0000-0000-000000000022": {Outcome: conflictcontract.ConflictReviewOutcomeNoop},
		},
	}
	err := ProcessTeamConflictReview(context.Background(), observability.New(slog.LevelError), ledger, cfg, observability.NoopDiscoverabilityMetrics(), ledger.run.TeamID, ledger.run.WorkerID, time.Now().UTC())
	if err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.completes) != 1 {
		t.Fatalf("complete calls = %#v", ledger.completes)
	}
	complete := ledger.completes[0]
	if complete.Status != "failed" || complete.LastError != "derived evidence retry failed" || complete.ClaimedCases != 1 || complete.NoOpCases != 1 {
		t.Fatalf("complete input = %#v", complete)
	}
}
func TestProcessTeamConflictReviewCompletesRunAfterParentContextDeadline(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	ledger := &conflictReviewLedgerStub{
		run: &conflictcontract.ConflictReviewRunRecord{
			TeamID:      "00000000-0000-0000-0000-000000000030",
			ReviewRunID: "00000000-0000-0000-0000-000000000031",
			Status:      "running",
			WorkerID:    "worker-deadline",
		},
		claimed: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ProcessTeamConflictReview(ctx, observability.New(slog.LevelError), ledger, cfg, observability.NoopDiscoverabilityMetrics(), ledger.run.TeamID, ledger.run.WorkerID, time.Now().UTC())
	if err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.completeContextErrors) != 1 || ledger.completeContextErrors[0] != nil {
		t.Fatalf("completion context errors = %#v, want [nil]", ledger.completeContextErrors)
	}
}
func TestProcessTeamConflictReviewCountsMixedOutcomes(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	teamID := "00000000-0000-0000-0000-000000000010"
	runID := "00000000-0000-0000-0000-000000000011"
	ledger := &conflictReviewLedgerStub{
		run: &conflictcontract.ConflictReviewRunRecord{
			TeamID:      teamID,
			ReviewRunID: runID,
			Status:      "running",
			WorkerID:    "worker-b",
		},
		claimed: true,
		claimBatches: [][]conflictcontract.RelationshipConflictCaseRecord{{
			{ConflictID: "00000000-0000-0000-0000-000000000101"},
			{ConflictID: "00000000-0000-0000-0000-000000000102"},
			{ConflictID: "00000000-0000-0000-0000-000000000103"},
			{ConflictID: "00000000-0000-0000-0000-000000000104"},
		}},
		reviewResults: map[string]*conflictcontract.ReviewRelationshipConflictCaseResult{
			"00000000-0000-0000-0000-000000000101": {Outcome: conflictcontract.ConflictReviewOutcomeResolve},
			"00000000-0000-0000-0000-000000000102": {Outcome: conflictcontract.ConflictReviewOutcomeOverdue},
			"00000000-0000-0000-0000-000000000103": {Outcome: conflictcontract.ConflictReviewOutcomeNoop},
		},
		reviewErrs: map[string]error{
			"00000000-0000-0000-0000-000000000104": errors.New("case failed"),
		},
	}
	err := ProcessTeamConflictReview(context.Background(), observability.New(slog.LevelError), ledger, cfg, metrics, teamID, "worker-b", time.Now().UTC())
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
	samples := metrics.ConflictReviewSamples()
	if len(samples) != 1 || samples[0].Outcome != "partial_error" {
		t.Fatalf("metrics samples = %#v", samples)
	}
}
func TestProcessTeamConflictReviewClaimsCasesOneAtATime(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	teamID := "00000000-0000-0000-0000-000000000110"
	runID := "00000000-0000-0000-0000-000000000111"
	ledger := &conflictReviewLedgerStub{
		run: &conflictcontract.ConflictReviewRunRecord{
			TeamID: teamID, ReviewRunID: runID, Status: "running", WorkerID: "worker-one-at-a-time",
		},
		claimed: true,
		claimBatches: [][]conflictcontract.RelationshipConflictCaseRecord{{
			{ConflictID: "00000000-0000-0000-0000-000000000112"},
			{ConflictID: "00000000-0000-0000-0000-000000000113"},
		}},
	}
	err := ProcessTeamConflictReview(context.Background(), observability.New(slog.LevelError), ledger, cfg, observability.NoopDiscoverabilityMetrics(), teamID, ledger.run.WorkerID, time.Now().UTC())
	if err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.claimInputs) == 0 {
		t.Fatal("expected at least one conflict claim")
	}
	if len(ledger.reserveInputs) < 2 {
		t.Fatalf("review-run renewals = %d, want at least 2", len(ledger.reserveInputs))
	}
	for _, input := range ledger.claimInputs {
		if input.Limit != 1 {
			t.Fatalf("conflict claim limit = %d, want 1", input.Limit)
		}
	}
}

func TestProcessTeamConflictReviewFailsWhenLeaseIsLost(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	teamID := "00000000-0000-0000-0000-000000000120"
	runID := "00000000-0000-0000-0000-000000000121"
	ledger := &sequencedConflictReviewLedgerStub{
		conflictReviewLedgerStub: conflictReviewLedgerStub{},
		reserves: []conflictReviewReserveResponse{
			{run: &conflictcontract.ConflictReviewRunRecord{TeamID: teamID, ReviewRunID: runID, Status: "running", WorkerID: "worker-lease-lost"}, claimed: true},
			{run: &conflictcontract.ConflictReviewRunRecord{TeamID: teamID, ReviewRunID: runID, Status: "running", WorkerID: "another-worker"}, claimed: true},
		},
	}

	err := ProcessTeamConflictReview(context.Background(), observability.New(slog.LevelError), ledger, cfg, observability.NoopDiscoverabilityMetrics(), teamID, "worker-lease-lost", time.Now().UTC())
	if err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.completes) != 1 {
		t.Fatalf("complete calls = %#v", ledger.completes)
	}
	complete := ledger.completes[0]
	if complete.Status != "failed" || complete.LastError != "conflict review run lease lost" {
		t.Fatalf("complete input = %#v", complete)
	}
}

func TestProcessTeamConflictReviewReturnsReservationError(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	rawErr := errors.New("reservation failed")
	ledger := &conflictReviewLedgerStub{reserveErr: rawErr}

	err := ProcessTeamConflictReview(context.Background(), observability.New(slog.LevelError), ledger, cfg, observability.NoopDiscoverabilityMetrics(), "00000000-0000-0000-0000-000000000130", "worker-reservation-error", time.Now().UTC())
	if !errors.Is(err, rawErr) {
		t.Fatalf("processTeamConflictReview error = %v, want %v", err, rawErr)
	}
	if len(ledger.completes) != 0 {
		t.Fatalf("complete calls = %#v, want none", ledger.completes)
	}
}

func TestProcessTeamConflictReviewFailsOnRenewalError(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	teamID := "00000000-0000-0000-0000-000000000140"
	runID := "00000000-0000-0000-0000-000000000141"
	rawErr := errors.New("renewal failed")
	ledger := &sequencedConflictReviewLedgerStub{
		conflictReviewLedgerStub: conflictReviewLedgerStub{},
		reserves: []conflictReviewReserveResponse{
			{run: &conflictcontract.ConflictReviewRunRecord{TeamID: teamID, ReviewRunID: runID, Status: "running", WorkerID: "worker-renewal-error"}, claimed: true},
			{err: rawErr},
		},
	}

	if err := ProcessTeamConflictReview(context.Background(), nil, ledger, cfg, nil, teamID, "worker-renewal-error", time.Now().UTC()); err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.completes) != 1 || ledger.completes[0].LastError != SafeConflictReviewError(rawErr) {
		t.Fatalf("complete inputs = %#v", ledger.completes)
	}
}

func TestProcessTeamConflictReviewFailsOnConflictClaimError(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	teamID := "00000000-0000-0000-0000-000000000150"
	runID := "00000000-0000-0000-0000-000000000151"
	ledger := &sequencedConflictReviewLedgerStub{
		conflictReviewLedgerStub: conflictReviewLedgerStub{
			claimErr: errors.New("claim failed"),
		},
		reserves: []conflictReviewReserveResponse{
			{run: &conflictcontract.ConflictReviewRunRecord{TeamID: teamID, ReviewRunID: runID, Status: "running", WorkerID: "worker-claim-error"}, claimed: true},
			{run: &conflictcontract.ConflictReviewRunRecord{TeamID: teamID, ReviewRunID: runID, Status: "running", WorkerID: "worker-claim-error"}, claimed: true},
		},
	}

	if err := ProcessTeamConflictReview(context.Background(), nil, ledger, cfg, nil, teamID, "worker-claim-error", time.Now().UTC()); err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.completes) != 1 || ledger.completes[0].LastError != SafeConflictReviewError(ledger.claimErr) {
		t.Fatalf("complete inputs = %#v", ledger.completes)
	}
}

func TestProcessTeamConflictReviewIgnoresDuplicateClaims(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	teamID := "00000000-0000-0000-0000-000000000160"
	runID := "00000000-0000-0000-0000-000000000161"
	ledger := &conflictReviewLedgerStub{
		run:     &conflictcontract.ConflictReviewRunRecord{TeamID: teamID, ReviewRunID: runID, Status: "running", WorkerID: "worker-duplicate"},
		claimed: true,
		claimBatches: [][]conflictcontract.RelationshipConflictCaseRecord{{
			{ConflictID: "00000000-0000-0000-0000-000000000162"},
			{ConflictID: "00000000-0000-0000-0000-000000000162"},
		}},
	}

	if err := ProcessTeamConflictReview(context.Background(), nil, ledger, cfg, nil, teamID, ledger.run.WorkerID, time.Now().UTC()); err != nil {
		t.Fatalf("processTeamConflictReview returned error: %v", err)
	}
	if len(ledger.completes) != 1 || ledger.completes[0].ClaimedCases != 1 || ledger.completes[0].NoOpCases != 1 {
		t.Fatalf("complete inputs = %#v", ledger.completes)
	}
}

func TestProcessTeamConflictReviewReturnsCompletionError(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	rawErr := errors.New("completion failed")
	ledger := &conflictReviewLedgerStub{
		run: &conflictcontract.ConflictReviewRunRecord{
			TeamID:      "00000000-0000-0000-0000-000000000170",
			ReviewRunID: "00000000-0000-0000-0000-000000000171",
			Status:      "running",
			WorkerID:    "worker-completion-error",
		},
		claimed:     true,
		completeErr: rawErr,
	}

	err := ProcessTeamConflictReview(context.Background(), nil, ledger, cfg, nil, ledger.run.TeamID, ledger.run.WorkerID, time.Now().UTC())
	if !errors.Is(err, rawErr) {
		t.Fatalf("processTeamConflictReview error = %v, want %v", err, rawErr)
	}
}

type conflictReviewReserveResponse struct {
	run     *conflictcontract.ConflictReviewRunRecord
	claimed bool
	err     error
}

type sequencedConflictReviewLedgerStub struct {
	conflictReviewLedgerStub
	reserves []conflictReviewReserveResponse
}

func (s *sequencedConflictReviewLedgerStub) ReserveRelationshipConflictReviewRun(_ context.Context, input conflictcontract.ConflictReviewRunInput) (*conflictcontract.ConflictReviewRunRecord, bool, error) {
	s.reserveInputs = append(s.reserveInputs, input)
	index := len(s.reserveInputs) - 1
	if index < len(s.reserves) {
		response := s.reserves[index]
		return response.run, response.claimed, response.err
	}
	return s.run, s.claimed, s.reserveErr
}

func testConflictReviewConfig(t *testing.T, timezone string, start string, jitter string) *config.Config {
	t.Helper()
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	t.Setenv("APP_TIMEZONE", timezone)
	t.Setenv("CONFLICT_REVIEW_START_TIME_LOCAL", start)
	t.Setenv("CONFLICT_REVIEW_JITTER_SECONDS", jitter)
	t.Setenv("CONFLICT_REVIEW_LEASE_SECONDS", "120")
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
	run                   *conflictcontract.ConflictReviewRunRecord
	claimed               bool
	reserveErr            error
	reserveInputs         []conflictcontract.ConflictReviewRunInput
	claimBatches          [][]conflictcontract.RelationshipConflictCaseRecord
	claimInputs           []conflictcontract.ClaimRelationshipConflictCasesInput
	claimErr              error
	derivedErr            error
	derivedInputs         []conflictcontract.ClaimConflictDerivedEvidenceTasksInput
	reviewResults         map[string]*conflictcontract.ReviewRelationshipConflictCaseResult
	reviewErrs            map[string]error
	completes             []conflictcontract.ConflictReviewRunCompleteInput
	completeContextErrors []error
	completeErr           error
}
type conflictReviewProfileListStub struct {
	pageSizes map[int]int
	offsets   []int
	err       error
}

type blockingTeamLister struct {
	mu      sync.Mutex
	started chan struct{}
}

func (l *blockingTeamLister) List(ctx context.Context, _ int, _ int) ([]*domain.Team, error) {
	l.mu.Lock()
	if l.started != nil {
		l.started <- struct{}{}
	}
	l.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *conflictReviewProfileListStub) List(_ context.Context, _ int, offset int) ([]*domain.Team, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.offsets = append(s.offsets, offset)
	count := s.pageSizes[offset]
	out := make([]*domain.Team, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, &domain.Team{ID: uuid.New()})
	}
	return out, nil
}

type conflictReviewLogCapture struct {
	errs []error
}

func (l *conflictReviewLogCapture) Info(string, ...observability.LogAttr) {}
func (l *conflictReviewLogCapture) Error(_ string, err error, _ ...observability.LogAttr) {
	l.errs = append(l.errs, err)
}
func (l *conflictReviewLogCapture) Warn(string, ...observability.LogAttr)  {}
func (l *conflictReviewLogCapture) Debug(string, ...observability.LogAttr) {}
func (l *conflictReviewLogCapture) With(...observability.LogAttr) observability.LogProvider {
	return l
}
func (s *conflictReviewLedgerStub) ReserveRelationshipConflictReviewRun(_ context.Context, input conflictcontract.ConflictReviewRunInput) (*conflictcontract.ConflictReviewRunRecord, bool, error) {
	s.reserveInputs = append(s.reserveInputs, input)
	return s.run, s.claimed, s.reserveErr
}
func (s *conflictReviewLedgerStub) ClaimRelationshipConflictCases(_ context.Context, input conflictcontract.ClaimRelationshipConflictCasesInput) ([]conflictcontract.RelationshipConflictCaseRecord, error) {
	s.claimInputs = append(s.claimInputs, input)
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
func (s *conflictReviewLedgerStub) ReviewRelationshipConflictCase(_ context.Context, input conflictcontract.ReviewRelationshipConflictCaseInput) (*conflictcontract.ReviewRelationshipConflictCaseResult, error) {
	if err := s.reviewErrs[input.ConflictID]; err != nil {
		return nil, err
	}
	if result := s.reviewResults[input.ConflictID]; result != nil {
		return result, nil
	}
	return &conflictcontract.ReviewRelationshipConflictCaseResult{Outcome: conflictcontract.ConflictReviewOutcomeNoop}, nil
}
func (s *conflictReviewLedgerStub) ProcessPendingConflictDerivedEvidence(_ context.Context, input conflictcontract.ClaimConflictDerivedEvidenceTasksInput) (int, error) {
	s.derivedInputs = append(s.derivedInputs, input)
	return 0, s.derivedErr
}
func (s *conflictReviewLedgerStub) CompleteRelationshipConflictReviewRun(ctx context.Context, input conflictcontract.ConflictReviewRunCompleteInput) error {
	s.completes = append(s.completes, input)
	s.completeContextErrors = append(s.completeContextErrors, ctx.Err())
	return s.completeErr
}
