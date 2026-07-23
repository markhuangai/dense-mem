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

type V2SemanticPlacementReviewSource interface {
	BuildV2SemanticReviewJob(ctx context.Context, run repository.V2PlacementRun) (V2SemanticReviewJob, error)
}

type V2SemanticPlacementWorkerService interface {
	ProcessNextV2SemanticPlacement(ctx context.Context) (bool, error)
}

type V2SemanticPlacementWorkerDependencies struct {
	Ledger       repository.V2LedgerRepository
	Review       V2SemanticReviewService
	Commit       V2SemanticCommitService
	ReviewSource V2SemanticPlacementReviewSource
	TeamID       string
	WorkerID     string
	Lease        time.Duration
}

type v2SemanticPlacementWorkerService struct {
	ledger       repository.V2LedgerRepository
	review       V2SemanticReviewService
	commit       V2SemanticCommitService
	reviewSource V2SemanticPlacementReviewSource
	teamID       string
	workerID     string
	lease        time.Duration
}

func NewV2SemanticPlacementWorkerService(deps V2SemanticPlacementWorkerDependencies) V2SemanticPlacementWorkerService {
	lease := deps.Lease
	if lease <= 0 {
		lease = time.Minute
	}
	return &v2SemanticPlacementWorkerService{
		ledger:       deps.Ledger,
		review:       deps.Review,
		commit:       deps.Commit,
		reviewSource: deps.ReviewSource,
		teamID:       strings.TrimSpace(deps.TeamID),
		workerID:     strings.TrimSpace(deps.WorkerID),
		lease:        lease,
	}
}

func (s *v2SemanticPlacementWorkerService) ProcessNextV2SemanticPlacement(ctx context.Context) (bool, error) {
	if s.ledger == nil {
		return false, errors.New("v2 semantic placement worker: ledger repository is required")
	}
	if s.review == nil {
		return false, errors.New("v2 semantic placement worker: review service is required")
	}
	if s.commit == nil {
		return false, errors.New("v2 semantic placement worker: commit service is required")
	}
	if s.reviewSource == nil {
		return false, errors.New("v2 semantic placement worker: review source is required")
	}
	if s.teamID == "" {
		return false, errors.New("v2 semantic placement worker: team_id is required")
	}
	if s.workerID == "" {
		return false, errors.New("v2 semantic placement worker: worker_id is required")
	}
	run, err := s.ledger.ClaimNextPlacementRun(ctx, s.teamID, s.workerID, s.lease)
	if err != nil {
		return false, err
	}
	if run == nil {
		return false, nil
	}
	reviewJob, err := s.reviewSource.BuildV2SemanticReviewJob(ctx, *run)
	if err != nil {
		return true, err
	}
	reviewJob = v2ReviewJobWithRunScope(reviewJob, *run)
	result, err := s.review.ReviewV2Semantic(ctx, reviewJob)
	if err != nil {
		return true, errors.Join(err, s.requeueV2SemanticPlacement(ctx, *run, reviewJob, "semantic review failed before completion"))
	}
	if result == nil {
		err := errors.New("v2 semantic placement worker: review returned nil result")
		return true, errors.Join(err, s.requeueV2SemanticPlacement(ctx, *run, reviewJob, "semantic review returned no result"))
	}
	_, err = s.commit.CompleteV2SemanticPlacement(ctx, v2CommitJobWithRunScope(reviewJob, *run, s.workerID, *result))
	if err != nil {
		return true, err
	}
	return true, nil
}

func (s *v2SemanticPlacementWorkerService) requeueV2SemanticPlacement(
	ctx context.Context,
	run repository.V2PlacementRun,
	reviewJob V2SemanticReviewJob,
	reason string,
) error {
	retryable := V2SemanticReviewResult{
		Status: string(domain.V2SemanticReviewRetryable),
		ValidationErrors: []verifier.V2SemanticValidationError{{
			Field:   "semantic_review",
			Message: reason,
		}},
	}
	_, err := s.commit.CompleteV2SemanticPlacement(ctx, v2CommitJobWithRunScope(reviewJob, run, s.workerID, retryable))
	return err
}

func v2ReviewJobWithRunScope(job V2SemanticReviewJob, run repository.V2PlacementRun) V2SemanticReviewJob {
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

func v2CommitJobWithRunScope(
	reviewJob V2SemanticReviewJob,
	run repository.V2PlacementRun,
	workerID string,
	result V2SemanticReviewResult,
) V2SemanticCommitJob {
	return V2SemanticCommitJob{
		TeamID:           run.TeamID,
		OwnerProfileID:   run.OwnerProfileID,
		IngestID:         run.IngestID,
		PlacementRunID:   run.PlacementRunID,
		PlacementItemID:  reviewJob.PlacementItemID,
		WorkerID:         workerID,
		ExpectedAttempts: run.Attempts,
		MaxAttempts:      run.MaxAttempts,
		MigrationRunID:   run.MigrationRunID,
		MigrationEpoch:   run.MigrationEpoch,
		Request:          reviewJob.Request,
		Result:           result,
	}
}
