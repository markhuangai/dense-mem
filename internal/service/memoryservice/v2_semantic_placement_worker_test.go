package memoryservice

import (
	"context"
	"errors"
	"strings"
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
		MaxAttempts:    5,
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
	if commit.job.WorkerID != "worker-v2" || commit.job.ExpectedAttempts != 3 || commit.job.MaxAttempts != 5 || commit.job.PlacementItemID != placementItemID {
		t.Fatalf("commit job = %#v", commit.job)
	}
}

func TestV2SemanticPlacementWorkerReturnsFalseWhenNoRunClaimed(t *testing.T) {
	teamID := uuid.NewString()
	ledger := &v2PlacementWorkerLedgerStub{}
	worker := NewV2SemanticPlacementWorkerService(V2SemanticPlacementWorkerDependencies{
		Ledger:       ledger,
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
	if ledger.claimLease != time.Minute {
		t.Fatalf("claim lease = %s, want default 1m", ledger.claimLease)
	}
}

func TestV2SemanticPlacementWorkerRequiresDependenciesAndScope(t *testing.T) {
	validDeps := func() V2SemanticPlacementWorkerDependencies {
		return V2SemanticPlacementWorkerDependencies{
			Ledger:       &v2PlacementWorkerLedgerStub{},
			Review:       &v2PlacementWorkerReviewStub{},
			Commit:       &v2PlacementWorkerCommitStub{},
			ReviewSource: &v2PlacementWorkerReviewSourceStub{},
			TeamID:       "team-v2",
			WorkerID:     "worker-v2",
		}
	}
	tests := []struct {
		name string
		edit func(*V2SemanticPlacementWorkerDependencies)
		want string
	}{
		{
			name: "missing ledger",
			edit: func(deps *V2SemanticPlacementWorkerDependencies) {
				deps.Ledger = nil
			},
			want: "ledger repository is required",
		},
		{
			name: "missing review",
			edit: func(deps *V2SemanticPlacementWorkerDependencies) {
				deps.Review = nil
			},
			want: "review service is required",
		},
		{
			name: "missing commit",
			edit: func(deps *V2SemanticPlacementWorkerDependencies) {
				deps.Commit = nil
			},
			want: "commit service is required",
		},
		{
			name: "missing review source",
			edit: func(deps *V2SemanticPlacementWorkerDependencies) {
				deps.ReviewSource = nil
			},
			want: "review source is required",
		},
		{
			name: "missing team",
			edit: func(deps *V2SemanticPlacementWorkerDependencies) {
				deps.TeamID = " "
			},
			want: "team_id is required",
		},
		{
			name: "missing worker",
			edit: func(deps *V2SemanticPlacementWorkerDependencies) {
				deps.WorkerID = " "
			},
			want: "worker_id is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := validDeps()
			tt.edit(&deps)
			worker := NewV2SemanticPlacementWorkerService(deps)

			processed, err := worker.ProcessNextV2SemanticPlacement(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
			if processed {
				t.Fatal("processed = true")
			}
		})
	}
}

func TestV2SemanticPlacementWorkerPropagatesProcessingErrors(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	placementItemID := uuid.NewString()
	run := &repository.V2PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       uuid.NewString(),
		PlacementRunID: uuid.NewString(),
		Status:         "processing",
		Attempts:       1,
		MaxAttempts:    5,
	}
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	accepted := v2SemanticReviewResultFromResponse(v2SemanticReviewResponse(request.RequestID, false, false), 1, "sha256:worker-error")
	reviewJob := V2SemanticReviewJob{PlacementItemID: placementItemID, Request: request}
	errClaim := errors.New("claim failed")
	errSource := errors.New("review source failed")
	errReview := errors.New("review failed")
	errCommit := errors.New("commit failed")

	tests := []struct {
		name          string
		ledger        *v2PlacementWorkerLedgerStub
		reviewSource  *v2PlacementWorkerReviewSourceStub
		review        *v2PlacementWorkerReviewStub
		commit        *v2PlacementWorkerCommitStub
		wantProcessed bool
		wantErr       error
		wantContains  string
		wantRetry     bool
		wantReason    string
	}{
		{
			name:          "claim error leaves run unprocessed",
			ledger:        &v2PlacementWorkerLedgerStub{claimErr: errClaim},
			reviewSource:  &v2PlacementWorkerReviewSourceStub{job: reviewJob},
			review:        &v2PlacementWorkerReviewStub{result: accepted},
			commit:        &v2PlacementWorkerCommitStub{},
			wantProcessed: false,
			wantErr:       errClaim,
		},
		{
			name:          "review source error marks claimed run processed",
			ledger:        &v2PlacementWorkerLedgerStub{run: run},
			reviewSource:  &v2PlacementWorkerReviewSourceStub{err: errSource},
			review:        &v2PlacementWorkerReviewStub{result: accepted},
			commit:        &v2PlacementWorkerCommitStub{},
			wantProcessed: true,
			wantErr:       errSource,
		},
		{
			name:          "review error marks claimed run processed and retryable",
			ledger:        &v2PlacementWorkerLedgerStub{run: run},
			reviewSource:  &v2PlacementWorkerReviewSourceStub{job: reviewJob},
			review:        &v2PlacementWorkerReviewStub{err: errReview},
			commit:        &v2PlacementWorkerCommitStub{},
			wantProcessed: true,
			wantErr:       errReview,
			wantRetry:     true,
			wantReason:    "semantic review failed before completion",
		},
		{
			name:          "nil review result is a typed retryable worker error",
			ledger:        &v2PlacementWorkerLedgerStub{run: run},
			reviewSource:  &v2PlacementWorkerReviewSourceStub{job: reviewJob},
			review:        &v2PlacementWorkerReviewStub{returnNil: true},
			commit:        &v2PlacementWorkerCommitStub{},
			wantProcessed: true,
			wantContains:  "review returned nil result",
			wantRetry:     true,
			wantReason:    "semantic review returned no result",
		},
		{
			name:          "commit error marks claimed run processed",
			ledger:        &v2PlacementWorkerLedgerStub{run: run},
			reviewSource:  &v2PlacementWorkerReviewSourceStub{job: reviewJob},
			review:        &v2PlacementWorkerReviewStub{result: accepted},
			commit:        &v2PlacementWorkerCommitStub{err: errCommit},
			wantProcessed: true,
			wantErr:       errCommit,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worker := NewV2SemanticPlacementWorkerService(V2SemanticPlacementWorkerDependencies{
				Ledger:       tt.ledger,
				Review:       tt.review,
				Commit:       tt.commit,
				ReviewSource: tt.reviewSource,
				TeamID:       teamID,
				WorkerID:     "worker-v2",
			})

			processed, err := worker.ProcessNextV2SemanticPlacement(context.Background())
			if processed != tt.wantProcessed {
				t.Fatalf("processed = %v, want %v", processed, tt.wantProcessed)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantContains) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantContains)
			}
			if tt.wantRetry {
				if tt.commit.job.Result.Status != "retryable" {
					t.Fatalf("retry status = %q, want retryable", tt.commit.job.Result.Status)
				}
				if tt.commit.job.PlacementItemID != placementItemID || tt.commit.job.ExpectedAttempts != run.Attempts || tt.commit.job.MaxAttempts != run.MaxAttempts {
					t.Fatalf("retry job = %#v", tt.commit.job)
				}
				if got := tt.commit.job.Result.ValidationErrors; len(got) != 1 || got[0].Message != tt.wantReason {
					t.Fatalf("retry validation errors = %#v, want reason %q", got, tt.wantReason)
				}
			}
		})
	}
}

type v2PlacementWorkerLedgerStub struct {
	run           *repository.V2PlacementRun
	claimErr      error
	claimTeamID   string
	claimWorkerID string
	claimLease    time.Duration
}

func (s *v2PlacementWorkerLedgerStub) CreateIngest(context.Context, repository.V2CreateIngestInput) (*repository.V2CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *v2PlacementWorkerLedgerStub) GetPlacementRun(context.Context, repository.V2GetPlacementRunInput) (*repository.V2CreateIngestResult, error) {
	return nil, errors.New("unexpected GetPlacementRun")
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
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return s.run, nil
}

func (s *v2PlacementWorkerLedgerStub) FinishPlacementRun(context.Context, string, string, string, string, string) error {
	return errors.New("unexpected FinishPlacementRun")
}

type v2PlacementWorkerReviewSourceStub struct {
	err error
	job V2SemanticReviewJob
}

func (s *v2PlacementWorkerReviewSourceStub) BuildV2SemanticReviewJob(context.Context, repository.V2PlacementRun) (V2SemanticReviewJob, error) {
	if s.err != nil {
		return V2SemanticReviewJob{}, s.err
	}
	return s.job, nil
}

type v2PlacementWorkerReviewStub struct {
	err       error
	job       V2SemanticReviewJob
	result    *V2SemanticReviewResult
	returnNil bool
}

func (s *v2PlacementWorkerReviewStub) ReviewV2Semantic(_ context.Context, job V2SemanticReviewJob) (*V2SemanticReviewResult, error) {
	s.job = job
	if s.err != nil {
		return nil, s.err
	}
	if s.returnNil {
		return nil, nil
	}
	if s.result == nil {
		return &V2SemanticReviewResult{Status: "retryable"}, nil
	}
	return s.result, nil
}

type v2PlacementWorkerCommitStub struct {
	err error
	job V2SemanticCommitJob
}

func (s *v2PlacementWorkerCommitStub) CommitV2Semantic(context.Context, V2SemanticCommitJob) (*repository.V2CommitPlacementSemanticResult, error) {
	return nil, errors.New("unexpected CommitV2Semantic")
}

func (s *v2PlacementWorkerCommitStub) CompleteV2SemanticPlacement(_ context.Context, job V2SemanticCommitJob) (*V2SemanticPlacementCompletionResult, error) {
	s.job = job
	if s.err != nil {
		return nil, s.err
	}
	return &V2SemanticPlacementCompletionResult{Status: job.Result.Status}, nil
}
