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
	"github.com/markhuangai/dense-mem/internal/service"
)

type searchConvergenceHealthStub struct {
	err error
}

func (s searchConvergenceHealthStub) CheckSearchConvergence(context.Context) error {
	return s.err
}

type searchReconciliationRunnerStub struct {
	results  []service.SearchReconciliationResult
	cancel   context.CancelFunc
	cancelAt int
	calls    int
}

func (s *searchReconciliationRunnerStub) Run(context.Context) (service.SearchReconciliationResult, error) {
	s.calls++
	if s.calls > len(s.results) {
		return service.SearchReconciliationResult{}, errors.New("unexpected reconciliation pass")
	}
	result := s.results[s.calls-1]
	if s.calls == s.cancelAt {
		s.cancel()
	}
	return result, nil
}

type searchConvergenceHealthLogger struct {
	warnings []string
	attrs    []observability.LogAttr
}

func (*searchConvergenceHealthLogger) Info(string, ...observability.LogAttr)         {}
func (*searchConvergenceHealthLogger) Error(string, error, ...observability.LogAttr) {}
func (l *searchConvergenceHealthLogger) Warn(message string, attrs ...observability.LogAttr) {
	l.warnings = append(l.warnings, message)
	l.attrs = append(l.attrs, attrs...)
}
func (*searchConvergenceHealthLogger) Debug(string, ...observability.LogAttr) {}
func (l *searchConvergenceHealthLogger) With(...observability.LogAttr) observability.LogProvider {
	return l
}

func TestSearchConvergenceHealthCheckBoundsRepositoryErrors(t *testing.T) {
	raw := errors.New("pq: secret relation does not exist")
	logger := &searchConvergenceHealthLogger{}
	check := searchConvergenceHealthCheck(searchConvergenceHealthStub{err: raw}, logger)

	err := check(context.Background())

	if !errors.Is(err, errSearchConvergenceQueryFailed) || errors.Is(err, raw) {
		t.Fatalf("health error = %v", err)
	}
	if len(logger.warnings) != 1 || logger.warnings[0] != "search_convergence_health_query_failed" {
		t.Fatalf("warnings = %#v", logger.warnings)
	}
	if len(logger.attrs) != 1 || logger.attrs[0].Key != "error_code" || logger.attrs[0].Value != "search_convergence_query_failed" {
		t.Fatalf("warning attrs = %#v", logger.attrs)
	}
}

func TestSearchConvergenceHealthCheckDoesNotLogExpectedDegradation(t *testing.T) {
	logger := &searchConvergenceHealthLogger{}
	check := searchConvergenceHealthCheck(searchConvergenceHealthStub{err: repository.ErrSearchConvergenceAttentionRequired}, logger)

	err := check(context.Background())

	if !errors.Is(err, repository.ErrSearchConvergenceAttentionRequired) {
		t.Fatalf("health error = %v", err)
	}
	if len(logger.warnings) != 0 {
		t.Fatalf("warnings = %#v", logger.warnings)
	}
}

func TestStartSearchReconciliationDrainsOnlyWhileProgressing(t *testing.T) {
	for _, test := range []struct {
		name    string
		results []service.SearchReconciliationResult
		calls   int
	}{
		{
			name: "drains remaining drift",
			results: []service.SearchReconciliationResult{
				{UpdatedCount: 256, DriftedCount: 2},
				{UpdatedCount: 2, DriftedCount: 0},
			},
			calls: 2,
		},
		{
			name:    "stops without progress",
			results: []service.SearchReconciliationResult{{UpdatedCount: 0, DriftedCount: 2}},
			calls:   1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			runner := &searchReconciliationRunnerStub{results: test.results, cancel: cancel, cancelAt: test.calls}

			startSearchReconciliation(ctx, runner, nil)

			if runner.calls != test.calls {
				t.Fatalf("reconciliation passes = %d, want %d", runner.calls, test.calls)
			}
		})
	}
}

func TestPrivateMemoryPrepareBootErrorBoundsRawFailure(t *testing.T) {
	raw := errors.New("pq: password secret relation does not exist")
	err := privateMemoryPrepareBootError(raw)

	if !errors.Is(err, errPrivateMemoryPrepareFailed) {
		t.Fatalf("prepare error = %v", err)
	}
	if errors.Is(err, raw) || err.Error() != "private-memory erasure preparation failed" {
		t.Fatalf("prepare error exposed raw failure: %v", err)
	}
	if err := privateMemoryPrepareBootError(nil); err != nil {
		t.Fatalf("nil prepare error = %v", err)
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

func TestProcessConflictReviewTickLogsBoundedErrors(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "00:00", "0")
	rawErr := errors.New("raw database error with internal detail")

	profileLogger := &conflictReviewLogCapture{}
	processConflictReviewTick(
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
	if len(profileLogger.errs) != 1 || !errors.Is(profileLogger.errs[0], errConflictReviewTeamListFailed) {
		t.Fatalf("profile list logged errors = %#v", profileLogger.errs)
	}
	if errors.Is(profileLogger.errs[0], rawErr) {
		t.Fatalf("profile list logged raw error")
	}

	runLogger := &conflictReviewLogCapture{}
	processConflictReviewTick(
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
	if len(runLogger.errs) != 1 || !errors.Is(runLogger.errs[0], errConflictReviewRunFailed) {
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
		run: &repository.ConflictReviewRunRecord{
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
	if len(ledger.derivedInputs) != 1 || ledger.derivedInputs[0] != (repository.ClaimConflictDerivedEvidenceTasksInput{
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

func TestProcessTeamConflictReviewRecordsDerivedEvidenceRetryFailure(t *testing.T) {
	cfg := testConflictReviewConfig(t, "UTC", "04:00", "0")
	ledger := &conflictReviewLedgerStub{
		run: &repository.ConflictReviewRunRecord{
			TeamID:      "00000000-0000-0000-0000-000000000020",
			ReviewRunID: "00000000-0000-0000-0000-000000000021",
			Status:      "running",
			WorkerID:    "worker-derived-failure",
		},
		claimed:    true,
		derivedErr: errors.New("derived evidence task failed"),
		claimBatches: [][]repository.RelationshipConflictCaseRecord{{
			{ConflictID: "00000000-0000-0000-0000-000000000022"},
		}},
		reviewResults: map[string]*repository.ReviewRelationshipConflictCaseResult{
			"00000000-0000-0000-0000-000000000022": {Outcome: repository.ConflictReviewOutcomeNoop},
		},
	}

	err := processTeamConflictReview(context.Background(), observability.New(slog.LevelError), ledger, cfg, observability.NoopDiscoverabilityMetrics(), ledger.run.TeamID, ledger.run.WorkerID, time.Now().UTC())
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
		run: &repository.ConflictReviewRunRecord{
			TeamID:      "00000000-0000-0000-0000-000000000030",
			ReviewRunID: "00000000-0000-0000-0000-000000000031",
			Status:      "running",
			WorkerID:    "worker-deadline",
		},
		claimed: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := processTeamConflictReview(ctx, observability.New(slog.LevelError), ledger, cfg, observability.NoopDiscoverabilityMetrics(), ledger.run.TeamID, ledger.run.WorkerID, time.Now().UTC())
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
		run: &repository.ConflictReviewRunRecord{
			TeamID:      teamID,
			ReviewRunID: runID,
			Status:      "running",
			WorkerID:    "worker-b",
		},
		claimed: true,
		claimBatches: [][]repository.RelationshipConflictCaseRecord{{
			{ConflictID: "00000000-0000-0000-0000-000000000101"},
			{ConflictID: "00000000-0000-0000-0000-000000000102"},
			{ConflictID: "00000000-0000-0000-0000-000000000103"},
			{ConflictID: "00000000-0000-0000-0000-000000000104"},
		}},
		reviewResults: map[string]*repository.ReviewRelationshipConflictCaseResult{
			"00000000-0000-0000-0000-000000000101": {Outcome: repository.ConflictReviewOutcomeResolve},
			"00000000-0000-0000-0000-000000000102": {Outcome: repository.ConflictReviewOutcomeOverdue},
			"00000000-0000-0000-0000-000000000103": {Outcome: repository.ConflictReviewOutcomeNoop},
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
	samples := metrics.ConflictReviewSamples()
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
	run                   *repository.ConflictReviewRunRecord
	claimed               bool
	reserveErr            error
	claimBatches          [][]repository.RelationshipConflictCaseRecord
	claimErr              error
	derivedErr            error
	derivedInputs         []repository.ClaimConflictDerivedEvidenceTasksInput
	reviewResults         map[string]*repository.ReviewRelationshipConflictCaseResult
	reviewErrs            map[string]error
	completes             []repository.ConflictReviewRunCompleteInput
	completeContextErrors []error
	completeErr           error
}

type conflictReviewProfileListStub struct {
	pageSizes map[int]int
	offsets   []int
	err       error
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

func (l *conflictReviewLogCapture) Warn(string, ...observability.LogAttr) {}

func (l *conflictReviewLogCapture) Debug(string, ...observability.LogAttr) {}

func (l *conflictReviewLogCapture) With(...observability.LogAttr) observability.LogProvider {
	return l
}

func (s *conflictReviewLedgerStub) ReserveRelationshipConflictReviewRun(context.Context, repository.ConflictReviewRunInput) (*repository.ConflictReviewRunRecord, bool, error) {
	return s.run, s.claimed, s.reserveErr
}

func (s *conflictReviewLedgerStub) ClaimRelationshipConflictCases(context.Context, repository.ClaimRelationshipConflictCasesInput) ([]repository.RelationshipConflictCaseRecord, error) {
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

func (s *conflictReviewLedgerStub) ReviewRelationshipConflictCase(_ context.Context, input repository.ReviewRelationshipConflictCaseInput) (*repository.ReviewRelationshipConflictCaseResult, error) {
	if err := s.reviewErrs[input.ConflictID]; err != nil {
		return nil, err
	}
	if result := s.reviewResults[input.ConflictID]; result != nil {
		return result, nil
	}
	return &repository.ReviewRelationshipConflictCaseResult{Outcome: repository.ConflictReviewOutcomeNoop}, nil
}

func (s *conflictReviewLedgerStub) ProcessPendingConflictDerivedEvidence(_ context.Context, input repository.ClaimConflictDerivedEvidenceTasksInput) (int, error) {
	s.derivedInputs = append(s.derivedInputs, input)
	return 0, s.derivedErr
}

func (s *conflictReviewLedgerStub) CompleteRelationshipConflictReviewRun(ctx context.Context, input repository.ConflictReviewRunCompleteInput) error {
	s.completes = append(s.completes, input)
	s.completeContextErrors = append(s.completeContextErrors, ctx.Err())
	return s.completeErr
}
