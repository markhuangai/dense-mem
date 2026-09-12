package conflictreview

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

// TeamLister is the only team surface needed by the conflict-review worker.
// Keeping it narrow prevents the scheduler from acquiring application policy.
type TeamLister interface {
	List(context.Context, int, int) ([]*domain.Team, error)
}

// Ledger is the conflict-review persistence and application surface. The
// scheduler owns timing and worker admission; the runner owns review policy.
type Ledger interface {
	ReserveRelationshipConflictReviewRun(context.Context, conflictcontract.ConflictReviewRunInput) (*conflictcontract.ConflictReviewRunRecord, bool, error)
	ClaimRelationshipConflictCases(context.Context, conflictcontract.ClaimRelationshipConflictCasesInput) ([]conflictcontract.RelationshipConflictCaseRecord, error)
	ReviewRelationshipConflictCase(context.Context, conflictcontract.ReviewRelationshipConflictCaseInput) (*conflictcontract.ReviewRelationshipConflictCaseResult, error)
	ProcessPendingConflictDerivedEvidence(context.Context, conflictcontract.ClaimConflictDerivedEvidenceTasksInput) (int, error)
	CompleteRelationshipConflictReviewRun(context.Context, conflictcontract.ConflictReviewRunCompleteInput) error
}

// ReviewService runs one scheduled conflict-review pass. It deliberately
// exposes no process or storage construction to composition.
type ReviewService struct {
	teams   TeamLister
	ledger  Ledger
	config  *config.Config
	logger  observability.LogProvider
	metrics observability.DiscoverabilityMetrics
	count   int
}

func NewReviewService(teams TeamLister, ledger Ledger, cfg *config.Config, logger observability.LogProvider, metrics observability.DiscoverabilityMetrics) *ReviewService {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	count := 1
	if cfg != nil {
		count = cfg.GetConflictReviewMaxConcurrency()
	}
	if count < 1 {
		count = 1
	}
	if count > 16 {
		count = 16
	}
	return &ReviewService{teams: teams, ledger: ledger, config: cfg, logger: logger, metrics: metrics, count: count}
}

// Run blocks until cancellation and joins every review worker before return.
func (s *ReviewService) Run(ctx context.Context) {
	if s == nil || s.teams == nil || s.ledger == nil || s.config == nil {
		return
	}
	hostname, _ := os.Hostname()
	baseWorkerID := fmt.Sprintf("conflict-review-%s-%d", hostname, os.Getpid())
	var workers sync.WaitGroup
	workers.Add(s.count)
	for workerIndex := 0; workerIndex < s.count; workerIndex++ {
		workerID := fmt.Sprintf("%s-%d", baseWorkerID, workerIndex+1)
		go func(workerID string, workerIndex int) {
			defer workers.Done()
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				ProcessConflictReviewTick(ctx, s.logger, s.teams, s.ledger, s.config, s.metrics, workerID, workerIndex, s.count)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}(workerID, workerIndex)
	}
	workers.Wait()
}

const conflictReviewCompletionTimeout = 15 * time.Second

func ProcessConflictReviewTick(ctx context.Context, logger observability.LogProvider, teams TeamLister, ledger Ledger, cfg *config.Config, metrics observability.DiscoverabilityMetrics, workerID string, workerIndex, workerCount int) {
	const pageSize = 100
	if workerCount < 1 {
		workerCount = 1
	}
	if workerIndex < 0 || workerIndex >= workerCount {
		workerIndex = 0
	}
	now := time.Now()
	for offset := workerIndex * pageSize; ; offset += pageSize * workerCount {
		page, err := teams.List(ctx, pageSize, offset)
		if err != nil {
			if logger != nil {
				logger.Error("conflict review team list failed", ErrConflictReviewTeamListFailed)
			}
			return
		}
		if len(page) == 0 {
			return
		}
		for _, team := range page {
			if team == nil || !ConflictReviewDueForTeam(now, cfg, team.ID.String()) {
				continue
			}
			if err := ProcessTeamConflictReview(ctx, logger, ledger, cfg, metrics, team.ID.String(), workerID, now); err != nil && logger != nil {
				logger.Error("conflict review run failed", ErrConflictReviewRunFailed, observability.String("team_id", team.ID.String()))
			}
		}
		if len(page) < pageSize {
			return
		}
	}
}

func ProcessTeamConflictReview(ctx context.Context, logger observability.LogProvider, ledger Ledger, cfg *config.Config, metrics observability.DiscoverabilityMetrics, teamID, workerID string, now time.Time) error {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	started := time.Now()
	outcome := "completed"
	defer func() {
		observability.RecordConflictReviewDuration(observability.WithMetricIdentity(ctx, teamID, ""), metrics, time.Since(started).Seconds(), outcome)
	}()
	lease := time.Duration(cfg.GetConflictReviewLeaseSeconds()) * time.Second
	runInput := conflictcontract.ConflictReviewRunInput{TeamID: teamID, WorkerID: workerID, LocalRunDate: now, Timezone: cfg.GetAppTimezone(), Lease: lease}
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, runInput)
	if err != nil {
		outcome = "error"
		return err
	}
	if run == nil || !claimed || run.Status == "completed" {
		outcome = "skipped"
		return nil
	}
	counts := conflictcontract.ConflictReviewRunCompleteInput{TeamID: teamID, ReviewRunID: run.ReviewRunID, WorkerID: workerID, Status: "completed"}
	if _, err := ledger.ProcessPendingConflictDerivedEvidence(ctx, conflictcontract.ClaimConflictDerivedEvidenceTasksInput{TeamID: teamID, ReviewRunID: run.ReviewRunID, WorkerID: workerID, Limit: cfg.GetConflictReviewBatchSize(), Lease: lease}); err != nil {
		counts.Status = "failed"
		counts.LastError = "derived evidence retry failed"
		outcome = "partial_error"
	}
	attempted := map[string]struct{}{}
	for {
		renewed, owned, err := ledger.ReserveRelationshipConflictReviewRun(ctx, runInput)
		if err != nil {
			counts.Status = "failed"
			counts.LastError = SafeConflictReviewError(err)
			outcome = "failed"
			break
		}
		if renewed == nil || !owned || renewed.ReviewRunID != run.ReviewRunID || renewed.WorkerID != workerID {
			counts.Status = "failed"
			counts.LastError = "conflict review run lease lost"
			outcome = "failed"
			break
		}
		run = renewed
		excluded := make([]string, 0, len(attempted))
		for id := range attempted {
			excluded = append(excluded, id)
		}
		cases, err := ledger.ClaimRelationshipConflictCases(ctx, conflictcontract.ClaimRelationshipConflictCasesInput{
			TeamID: teamID, WorkerID: workerID, ReviewRunID: run.ReviewRunID,
			Limit: 1, Lease: lease, MaxAttempts: cfg.GetConflictReviewMaxAttempts(), Now: time.Now().UTC(), ExcludedConflictIDs: excluded,
		})
		if err != nil {
			counts.Status = "failed"
			counts.LastError = SafeConflictReviewError(err)
			outcome = "failed"
			break
		}
		if len(cases) == 0 {
			break
		}
		for _, conflictCase := range cases {
			if _, ok := attempted[conflictCase.ConflictID]; ok {
				continue
			}
			attempted[conflictCase.ConflictID] = struct{}{}
			counts.ClaimedCases++
			result, err := ledger.ReviewRelationshipConflictCase(ctx, conflictcontract.ReviewRelationshipConflictCaseInput{TeamID: teamID, WorkerID: workerID, ReviewRunID: run.ReviewRunID, ConflictID: conflictCase.ConflictID, Now: time.Now().UTC()})
			if err != nil {
				counts.FailedCases++
				if logger != nil {
					logger.Error("conflict review case failed", ErrConflictReviewCaseFailed, observability.String("team_id", teamID), observability.String("conflict_id", conflictCase.ConflictID))
				}
				continue
			}
			switch result.Outcome {
			case conflictcontract.ConflictReviewOutcomeResolve:
				counts.ResolvedCases++
			case conflictcontract.ConflictReviewOutcomeOverdue:
				counts.OverdueCases++
			default:
				counts.NoOpCases++
			}
		}
	}
	if counts.FailedCases > 0 && counts.Status == "completed" {
		counts.Status = "failed"
		counts.LastError = "one or more conflict cases failed"
		outcome = "partial_error"
	} else if counts.ClaimedCases == 0 && counts.Status == "completed" {
		outcome = "empty"
	}
	completeCtx, completeCancel := context.WithTimeout(context.WithoutCancel(ctx), conflictReviewCompletionTimeout)
	defer completeCancel()
	if err := ledger.CompleteRelationshipConflictReviewRun(completeCtx, counts); err != nil {
		outcome = "error"
		return err
	}
	return nil
}

var (
	ErrConflictReviewTeamListFailed = errors.New("conflict review team list failed")
	ErrConflictReviewRunFailed      = errors.New("conflict review run failed")
	ErrConflictReviewCaseFailed     = errors.New("conflict review case failed")
)

func SafeConflictReviewError(err error) string {
	if err == nil {
		return ""
	}
	return "conflict review repository operation failed"
}

func ConflictReviewDueForTeam(now time.Time, cfg *config.Config, teamID string) bool {
	location := conflictReviewLocation(cfg.GetAppTimezone())
	localNow := now.In(location)
	start, err := time.Parse("15:04", cfg.GetConflictReviewStartTimeLocal())
	if err != nil {
		return false
	}
	year, month, day := localNow.Date()
	scheduled := time.Date(year, month, day, start.Hour(), start.Minute(), 0, 0, location)
	jitterSeconds := cfg.GetConflictReviewJitterSeconds()
	if jitterSeconds > 3600 {
		jitterSeconds = 3600
	}
	if jitterSeconds > 0 {
		delay := int(crc32.ChecksumIEEE([]byte(teamID))) % (jitterSeconds + 1)
		scheduled = scheduled.Add(time.Duration(delay) * time.Second)
	}
	return !localNow.Before(scheduled)
}

var conflictReviewLocationCache sync.Map

func conflictReviewLocation(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.Local
	}
	if cached, ok := conflictReviewLocationCache.Load(name); ok {
		return cached.(*time.Location)
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	actual, _ := conflictReviewLocationCache.LoadOrStore(name, location)
	return actual.(*time.Location)
}
