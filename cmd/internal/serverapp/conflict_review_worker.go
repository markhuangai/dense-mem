package serverapp

import (
	"context"
	"errors"
	"fmt"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"hash/crc32"
	"os"
	"strings"
	"sync"
	"time"
)

func startConflictReviewWorkers(
	ctx context.Context,
	logger observability.LogProvider,
	teams service.TeamService,
	ledger conflictReviewLedger,
	cfg *config.Config,
	metrics observability.DiscoverabilityMetrics,
) {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	hostname, _ := os.Hostname()
	baseWorkerID := fmt.Sprintf("conflict-review-%s-%d", hostname, os.Getpid())
	count := cfg.GetConflictReviewMaxConcurrency()
	if count < 1 {
		count = 1
	}
	if count > 16 {
		count = 16
	}
	for workerIndex := 0; workerIndex < count; workerIndex++ {
		workerID := fmt.Sprintf("%s-%d", baseWorkerID, workerIndex+1)
		go func(workerID string, workerIndex int) {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				processConflictReviewTick(ctx, logger, teams, ledger, cfg, metrics, workerID, workerIndex, count)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}(workerID, workerIndex)
	}
}

type conflictReviewTeamLister interface {
	List(ctx context.Context, limit, offset int) ([]*domain.Team, error)
}

func processConflictReviewTick(
	ctx context.Context,
	logger observability.LogProvider,
	teams conflictReviewTeamLister,
	ledger conflictReviewLedger,
	cfg *config.Config,
	metrics observability.DiscoverabilityMetrics,
	workerID string,
	workerIndex int,
	workerCount int,
) {
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
			logger.Error("conflict review team list failed", errConflictReviewTeamListFailed)
			return
		}
		if len(page) == 0 {
			return
		}
		for _, team := range page {
			if team == nil || !conflictReviewDueForTeam(now, cfg, team.ID.String()) {
				continue
			}
			if err := processTeamConflictReview(ctx, logger, ledger, cfg, metrics, team.ID.String(), workerID, now); err != nil {
				logger.Error("conflict review run failed", errConflictReviewRunFailed, observability.String("team_id", team.ID.String()))
			}
		}
		if len(page) < pageSize {
			return
		}
	}
}

const conflictReviewCompletionTimeout = 15 * time.Second

func processTeamConflictReview(
	ctx context.Context,
	logger observability.LogProvider,
	ledger conflictReviewLedger,
	cfg *config.Config,
	metrics observability.DiscoverabilityMetrics,
	teamID string,
	workerID string,
	now time.Time,
) error {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	started := time.Now()
	outcome := "completed"
	defer func() {
		observability.RecordConflictReviewDuration(observability.WithMetricIdentity(ctx, teamID, ""), metrics, time.Since(started).Seconds(), outcome)
	}()
	lease := time.Duration(cfg.GetConflictReviewLeaseSeconds()) * time.Second
	runInput := repository.ConflictReviewRunInput{TeamID: teamID, WorkerID: workerID, LocalRunDate: now, Timezone: cfg.GetAppTimezone(), Lease: lease}
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, runInput)
	if err != nil {
		outcome = "error"
		return err
	}
	if run == nil || !claimed || run.Status == "completed" {
		outcome = "skipped"
		return nil
	}
	counts := repository.ConflictReviewRunCompleteInput{
		TeamID:      teamID,
		ReviewRunID: run.ReviewRunID,
		WorkerID:    workerID,
		Status:      "completed",
	}
	if _, err := ledger.ProcessPendingConflictDerivedEvidence(ctx, repository.ClaimConflictDerivedEvidenceTasksInput{
		TeamID:      teamID,
		ReviewRunID: run.ReviewRunID,
		WorkerID:    workerID,
		Limit:       cfg.GetConflictReviewBatchSize(),
		Lease:       lease,
	}); err != nil {
		counts.Status = "failed"
		counts.LastError = "derived evidence retry failed"
		outcome = "partial_error"
	}
	attempted := map[string]struct{}{}
	for {
		renewed, owned, err := ledger.ReserveRelationshipConflictReviewRun(ctx, runInput)
		if err != nil {
			counts.Status = "failed"
			counts.LastError = safeConflictReviewError(err)
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
		cases, err := ledger.ClaimRelationshipConflictCases(ctx, repository.ClaimRelationshipConflictCasesInput{
			TeamID:      teamID,
			WorkerID:    workerID,
			ReviewRunID: run.ReviewRunID,
			// Claims are leased per case while synchronous resolution may spend
			// the full embedding timeout before the next case is processed.
			Limit:               1,
			Lease:               lease,
			MaxAttempts:         cfg.GetConflictReviewMaxAttempts(),
			Now:                 time.Now().UTC(),
			ExcludedConflictIDs: excluded,
		})
		if err != nil {
			counts.Status = "failed"
			counts.LastError = safeConflictReviewError(err)
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
			result, err := ledger.ReviewRelationshipConflictCase(ctx, repository.ReviewRelationshipConflictCaseInput{
				TeamID:      teamID,
				WorkerID:    workerID,
				ReviewRunID: run.ReviewRunID,
				ConflictID:  conflictCase.ConflictID,
				Now:         time.Now().UTC(),
			})
			if err != nil {
				counts.FailedCases++
				logger.Error("conflict review case failed", errConflictReviewCaseFailed, observability.String("team_id", teamID), observability.String("conflict_id", conflictCase.ConflictID))
				continue
			}
			switch result.Outcome {
			case repository.ConflictReviewOutcomeResolve:
				counts.ResolvedCases++
			case repository.ConflictReviewOutcomeOverdue:
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
	errConflictReviewTeamListFailed = errors.New("conflict review team list failed")
	errConflictReviewRunFailed      = errors.New("conflict review run failed")
	errConflictReviewCaseFailed     = errors.New("conflict review case failed")
)

func safeConflictReviewError(err error) string {
	if err == nil {
		return ""
	}
	return "conflict review repository operation failed"
}

type conflictReviewLedger interface {
	ReserveRelationshipConflictReviewRun(context.Context, repository.ConflictReviewRunInput) (*repository.ConflictReviewRunRecord, bool, error)
	ClaimRelationshipConflictCases(context.Context, repository.ClaimRelationshipConflictCasesInput) ([]repository.RelationshipConflictCaseRecord, error)
	ReviewRelationshipConflictCase(context.Context, repository.ReviewRelationshipConflictCaseInput) (*repository.ReviewRelationshipConflictCaseResult, error)
	ProcessPendingConflictDerivedEvidence(context.Context, repository.ClaimConflictDerivedEvidenceTasksInput) (int, error)
	CompleteRelationshipConflictReviewRun(context.Context, repository.ConflictReviewRunCompleteInput) error
}

func conflictReviewDueForTeam(now time.Time, cfg *config.Config, teamID string) bool {
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
