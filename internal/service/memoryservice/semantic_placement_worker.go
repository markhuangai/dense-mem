package memoryservice

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type SemanticPlacementReviewSource interface {
	BuildSemanticReviewJob(ctx context.Context, run repository.PlacementRun) (SemanticReviewJob, error)
}

type SemanticPlacementWorkerService interface {
	ProcessNextSemanticPlacement(ctx context.Context) (bool, error)
}

type SemanticPlacementWorkerDependencies struct {
	Ledger       repository.LedgerRepository
	Review       SemanticReviewService
	Commit       SemanticCommitService
	ReviewSource SemanticPlacementReviewSource
	TeamID       string
	WorkerID     string
	Lease        time.Duration
}

type semanticPlacementWorkerService struct {
	ledger       repository.LedgerRepository
	review       SemanticReviewService
	commit       SemanticCommitService
	reviewSource SemanticPlacementReviewSource
	teamID       string
	workerID     string
	lease        time.Duration
}

func NewSemanticPlacementWorkerService(deps SemanticPlacementWorkerDependencies) SemanticPlacementWorkerService {
	lease := deps.Lease
	if lease <= 0 {
		lease = time.Minute
	}
	return &semanticPlacementWorkerService{
		ledger:       deps.Ledger,
		review:       deps.Review,
		commit:       deps.Commit,
		reviewSource: deps.ReviewSource,
		teamID:       strings.TrimSpace(deps.TeamID),
		workerID:     strings.TrimSpace(deps.WorkerID),
		lease:        lease,
	}
}

func (s *semanticPlacementWorkerService) ProcessNextSemanticPlacement(ctx context.Context) (bool, error) {
	if s.ledger == nil {
		return false, errors.New("semantic placement worker: ledger repository is required")
	}
	if s.review == nil {
		return false, errors.New("semantic placement worker: review service is required")
	}
	if s.commit == nil {
		return false, errors.New("semantic placement worker: commit service is required")
	}
	if s.reviewSource == nil {
		return false, errors.New("semantic placement worker: review source is required")
	}
	if s.teamID == "" {
		return false, errors.New("semantic placement worker: team_id is required")
	}
	if s.workerID == "" {
		return false, errors.New("semantic placement worker: worker_id is required")
	}
	run, err := s.ledger.ClaimNextPlacementRun(ctx, s.teamID, s.workerID, s.lease)
	if err != nil {
		return false, err
	}
	if run == nil {
		return false, nil
	}
	reviewJob, err := s.reviewSource.BuildSemanticReviewJob(ctx, *run)
	if err != nil {
		return true, err
	}
	reviewJob = reviewJobWithRunScope(reviewJob, *run)
	result, err := s.review.ReviewSemantic(ctx, reviewJob)
	if err != nil {
		return true, errors.Join(err, s.requeueSemanticPlacement(ctx, *run, reviewJob, "semantic review failed before completion"))
	}
	if result == nil {
		err := errors.New("semantic placement worker: review returned nil result")
		return true, errors.Join(err, s.requeueSemanticPlacement(ctx, *run, reviewJob, "semantic review returned no result"))
	}
	_, err = s.commit.CompleteSemanticPlacement(ctx, commitJobWithRunScope(reviewJob, *run, s.workerID, *result))
	if err != nil {
		return true, errors.Join(err, s.requeueSemanticPlacement(ctx, *run, reviewJob, "semantic commit failed before completion"))
	}
	return true, nil
}

func (s *semanticPlacementWorkerService) requeueSemanticPlacement(
	ctx context.Context,
	run repository.PlacementRun,
	reviewJob SemanticReviewJob,
	reason string,
) error {
	retryable := SemanticReviewResult{
		Status: string(domain.SemanticReviewRetryable),
		ValidationErrors: []verifier.SemanticValidationError{{
			Field:   "semantic_review",
			Message: reason,
		}},
	}
	_, err := s.commit.CompleteSemanticPlacement(ctx, commitJobWithRunScope(reviewJob, run, s.workerID, retryable))
	return err
}

func reviewJobWithRunScope(job SemanticReviewJob, run repository.PlacementRun) SemanticReviewJob {
	if job.TeamID == "" {
		job.TeamID = run.TeamID
	}
	if job.OwnerProfileID == "" {
		job.OwnerProfileID = run.OwnerProfileID
	}
	if job.IngestID == "" {
		job.IngestID = run.IngestID
	}
	if job.PlacementRunID == "" {
		job.PlacementRunID = run.PlacementRunID
	}
	return job
}

func commitJobWithRunScope(
	reviewJob SemanticReviewJob,
	run repository.PlacementRun,
	workerID string,
	result SemanticReviewResult,
) SemanticCommitJob {
	return SemanticCommitJob{
		TeamID:           run.TeamID,
		OwnerProfileID:   run.OwnerProfileID,
		IngestID:         run.IngestID,
		PlacementRunID:   run.PlacementRunID,
		PlacementItemID:  reviewJob.PlacementItemID,
		WorkerID:         workerID,
		ExpectedAttempts: run.Attempts,
		MaxAttempts:      run.MaxAttempts,
		Request:          reviewJob.Request,
		Result:           result,
	}
}
