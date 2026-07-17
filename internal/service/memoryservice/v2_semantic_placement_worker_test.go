package memoryservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestV2SemanticPlacementWorkerClaimsReviewsAndCompletesAcceptedRun(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	placementRunID := uuid.NewString()
	placementItemID := uuid.NewString()
	ingestID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	reviewResult := v2SemanticReviewResultFromResponse(v2SemanticReviewResponse(request.RequestID, false, false), 1, "sha256:worker-review")
	ledger := &v2PlacementWorkerLedgerStub{run: &repository.V2PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: placementRunID,
		Status:         "processing",
		Attempts:       3,
	}}
	reviewSource := &v2PlacementWorkerReviewSourceStub{job: V2SemanticReviewJob{
		PlacementItemID: placementItemID,
		Request:         request,
	}}
	review := &v2PlacementWorkerReviewStub{result: reviewResult}
	commit := &v2PlacementWorkerCommitStub{}
	worker := NewV2SemanticPlacementWorkerService(V2SemanticPlacementWorkerDependencies{
		Ledger:       ledger,
		Review:       review,
		Commit:       commit,
		ReviewSource: reviewSource,
		TeamID:       teamID,
		WorkerID:     "worker-v2",
		Lease:        45 * time.Second,
	})

	processed, err := worker.ProcessNextV2SemanticPlacement(context.Background())
	if err != nil {
		t.Fatalf("ProcessNextV2SemanticPlacement returned error: %v", err)
	}
	if !processed {
		t.Fatal("processed = false")
	}
	if ledger.claimTeamID != teamID || ledger.claimWorkerID != "worker-v2" || ledger.claimLease != 45*time.Second {
		t.Fatalf("claim = team %q worker %q lease %s", ledger.claimTeamID, ledger.claimWorkerID, ledger.claimLease)
	}
	if review.job.TeamID != teamID || review.job.OwnerProfileID != ownerID || review.job.IngestID != ingestID || review.job.PlacementRunID != placementRunID {
		t.Fatalf("review job scope = %#v", review.job)
	}
	if commit.job.WorkerID != "worker-v2" || commit.job.ExpectedAttempts != 3 || commit.job.PlacementItemID != placementItemID {
		t.Fatalf("commit job = %#v", commit.job)
	}
}

func TestV2SemanticPlacementWorkerReturnsFalseWhenNoRunClaimed(t *testing.T) {
	teamID := uuid.NewString()
	worker := NewV2SemanticPlacementWorkerService(V2SemanticPlacementWorkerDependencies{
		Ledger:       &v2PlacementWorkerLedgerStub{},
		Review:       &v2PlacementWorkerReviewStub{},
		Commit:       &v2PlacementWorkerCommitStub{},
		ReviewSource: &v2PlacementWorkerReviewSourceStub{},
		TeamID:       teamID,
		WorkerID:     "worker-v2",
	})

	processed, err := worker.ProcessNextV2SemanticPlacement(context.Background())
	if err != nil {
		t.Fatalf("ProcessNextV2SemanticPlacement returned error: %v", err)
	}
	if processed {
		t.Fatal("processed = true")
	}
}

type v2PlacementWorkerLedgerStub struct {
	run           *repository.V2PlacementRun
	claimTeamID   string
	claimWorkerID string
	claimLease    time.Duration
}

func (s *v2PlacementWorkerLedgerStub) CreateIngest(context.Context, repository.V2CreateIngestInput) (*repository.V2CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *v2PlacementWorkerLedgerStub) AdvanceSourceRevision(context.Context, repository.V2AdvanceSourceRevisionInput) (*repository.V2SourceRevisionResult, error) {
	return nil, errors.New("unexpected AdvanceSourceRevision")
}

func (s *v2PlacementWorkerLedgerStub) AppendSecurityEvent(context.Context, repository.V2SecurityEventInput) (string, error) {
	return "", errors.New("unexpected AppendSecurityEvent")
}

func (s *v2PlacementWorkerLedgerStub) AppendPlacementOutcome(context.Context, repository.V2PlacementOutcomeInput) (string, error) {
	return "", errors.New("unexpected AppendPlacementOutcome")
}

func (s *v2PlacementWorkerLedgerStub) ClaimNextPlacementRun(_ context.Context, teamID string, workerID string, lease time.Duration) (*repository.V2PlacementRun, error) {
	s.claimTeamID = teamID
	s.claimWorkerID = workerID
	s.claimLease = lease
	return s.run, nil
}

func (s *v2PlacementWorkerLedgerStub) FinishPlacementRun(context.Context, string, string, string, string) error {
	return errors.New("unexpected FinishPlacementRun")
}

type v2PlacementWorkerReviewSourceStub struct {
	job V2SemanticReviewJob
}

func (s *v2PlacementWorkerReviewSourceStub) BuildV2SemanticReviewJob(context.Context, repository.V2PlacementRun) (V2SemanticReviewJob, error) {
	return s.job, nil
}

type v2PlacementWorkerReviewStub struct {
	job    V2SemanticReviewJob
	result *V2SemanticReviewResult
}

func (s *v2PlacementWorkerReviewStub) ReviewV2Semantic(_ context.Context, job V2SemanticReviewJob) (*V2SemanticReviewResult, error) {
	s.job = job
	if s.result == nil {
		return &V2SemanticReviewResult{Status: "retryable"}, nil
	}
	return s.result, nil
}

type v2PlacementWorkerCommitStub struct {
	job V2SemanticCommitJob
}

func (s *v2PlacementWorkerCommitStub) CommitV2Semantic(context.Context, V2SemanticCommitJob) (*repository.V2CommitPlacementSemanticResult, error) {
	return nil, errors.New("unexpected CommitV2Semantic")
}

func (s *v2PlacementWorkerCommitStub) CompleteV2SemanticPlacement(_ context.Context, job V2SemanticCommitJob) (*V2SemanticPlacementCompletionResult, error) {
	s.job = job
	return &V2SemanticPlacementCompletionResult{Status: job.Result.Status}, nil
}
