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

func TestSemanticPlacementWorkerClaimsReviewsAndCompletesAcceptedRun(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	placementRunID := uuid.NewString()
	placementItemID := uuid.NewString()
	ingestID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	reviewResult := semanticReviewResultFromResponse(semanticReviewResponse(request.RequestID, false, false), 1, "sha256:worker-review")
	ledger := &placementWorkerLedgerStub{run: &repository.PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: placementRunID,
		Status:         "processing",
		Attempts:       3,
		MaxAttempts:    5,
	}}
	reviewSource := &placementWorkerReviewSourceStub{job: SemanticReviewJob{
		PlacementItemID: placementItemID,
		Request:         request,
	}}
	review := &placementWorkerReviewStub{result: reviewResult}
	commit := &placementWorkerCommitStub{}
	worker := NewSemanticPlacementWorkerService(SemanticPlacementWorkerDependencies{
		Ledger:       ledger,
		Review:       review,
		Commit:       commit,
		ReviewSource: reviewSource,
		TeamID:       teamID,
		WorkerID:     "worker-canonical",
		Lease:        45 * time.Second,
	})

	processed, err := worker.ProcessNextSemanticPlacement(context.Background())
	if err != nil {
		t.Fatalf("ProcessNextSemanticPlacement returned error: %v", err)
	}
	if !processed {
		t.Fatal("processed = false")
	}
	if ledger.claimTeamID != teamID || ledger.claimWorkerID != "worker-canonical" || ledger.claimLease != 45*time.Second {
		t.Fatalf("claim = team %q worker %q lease %s", ledger.claimTeamID, ledger.claimWorkerID, ledger.claimLease)
	}
	if review.job.TeamID != teamID || review.job.OwnerProfileID != ownerID || review.job.IngestID != ingestID || review.job.PlacementRunID != placementRunID {
		t.Fatalf("review job scope = %#v", review.job)
	}
	if commit.job.WorkerID != "worker-canonical" || commit.job.ExpectedAttempts != 3 || commit.job.MaxAttempts != 5 || commit.job.PlacementItemID != placementItemID {
		t.Fatalf("commit job = %#v", commit.job)
	}
}

func TestSemanticPlacementWorkerReturnsFalseWhenNoRunClaimed(t *testing.T) {
	teamID := uuid.NewString()
	ledger := &placementWorkerLedgerStub{}
	worker := NewSemanticPlacementWorkerService(SemanticPlacementWorkerDependencies{
		Ledger:       ledger,
		Review:       &placementWorkerReviewStub{},
		Commit:       &placementWorkerCommitStub{},
		ReviewSource: &placementWorkerReviewSourceStub{},
		TeamID:       teamID,
		WorkerID:     "worker-canonical",
	})

	processed, err := worker.ProcessNextSemanticPlacement(context.Background())
	if err != nil {
		t.Fatalf("ProcessNextSemanticPlacement returned error: %v", err)
	}
	if processed {
		t.Fatal("processed = true")
	}
	if ledger.claimLease != time.Minute {
		t.Fatalf("claim lease = %s, want default 1m", ledger.claimLease)
	}
}

func TestSemanticPlacementWorkerRequiresDependenciesAndScope(t *testing.T) {
	validDeps := func() SemanticPlacementWorkerDependencies {
		return SemanticPlacementWorkerDependencies{
			Ledger:       &placementWorkerLedgerStub{},
			Review:       &placementWorkerReviewStub{},
			Commit:       &placementWorkerCommitStub{},
			ReviewSource: &placementWorkerReviewSourceStub{},
			TeamID:       "team-canonical",
			WorkerID:     "worker-canonical",
		}
	}
	tests := []struct {
		name string
		edit func(*SemanticPlacementWorkerDependencies)
		want string
	}{
		{
			name: "missing ledger",
			edit: func(deps *SemanticPlacementWorkerDependencies) {
				deps.Ledger = nil
			},
			want: "ledger repository is required",
		},
		{
			name: "missing review",
			edit: func(deps *SemanticPlacementWorkerDependencies) {
				deps.Review = nil
			},
			want: "review service is required",
		},
		{
			name: "missing commit",
			edit: func(deps *SemanticPlacementWorkerDependencies) {
				deps.Commit = nil
			},
			want: "commit service is required",
		},
		{
			name: "missing review source",
			edit: func(deps *SemanticPlacementWorkerDependencies) {
				deps.ReviewSource = nil
			},
			want: "review source is required",
		},
		{
			name: "missing team",
			edit: func(deps *SemanticPlacementWorkerDependencies) {
				deps.TeamID = " "
			},
			want: "team_id is required",
		},
		{
			name: "missing worker",
			edit: func(deps *SemanticPlacementWorkerDependencies) {
				deps.WorkerID = " "
			},
			want: "worker_id is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := validDeps()
			tt.edit(&deps)
			worker := NewSemanticPlacementWorkerService(deps)

			processed, err := worker.ProcessNextSemanticPlacement(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
			if processed {
				t.Fatal("processed = true")
			}
		})
	}
}

func TestSemanticPlacementWorkerPropagatesProcessingErrors(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	placementItemID := uuid.NewString()
	run := &repository.PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       uuid.NewString(),
		PlacementRunID: uuid.NewString(),
		Status:         "processing",
		Attempts:       1,
		MaxAttempts:    5,
	}
	request := semanticReviewServiceRequest(teamID, ownerID)
	accepted := semanticReviewResultFromResponse(semanticReviewResponse(request.RequestID, false, false), 1, "sha256:worker-error")
	reviewJob := SemanticReviewJob{PlacementItemID: placementItemID, Request: request}
	errClaim := errors.New("claim failed")
	errSource := errors.New("review source failed")
	errReview := errors.New("review failed")
	errCommit := errors.New("commit failed")

	tests := []struct {
		name          string
		ledger        *placementWorkerLedgerStub
		reviewSource  *placementWorkerReviewSourceStub
		review        *placementWorkerReviewStub
		commit        *placementWorkerCommitStub
		wantProcessed bool
		wantErr       error
		wantContains  string
		wantRetry     bool
		wantReason    string
	}{
		{
			name:          "claim error leaves run unprocessed",
			ledger:        &placementWorkerLedgerStub{claimErr: errClaim},
			reviewSource:  &placementWorkerReviewSourceStub{job: reviewJob},
			review:        &placementWorkerReviewStub{result: accepted},
			commit:        &placementWorkerCommitStub{},
			wantProcessed: false,
			wantErr:       errClaim,
		},
		{
			name:          "review source error marks claimed run processed",
			ledger:        &placementWorkerLedgerStub{run: run},
			reviewSource:  &placementWorkerReviewSourceStub{err: errSource},
			review:        &placementWorkerReviewStub{result: accepted},
			commit:        &placementWorkerCommitStub{},
			wantProcessed: true,
			wantErr:       errSource,
		},
		{
			name:          "review error marks claimed run processed and retryable",
			ledger:        &placementWorkerLedgerStub{run: run},
			reviewSource:  &placementWorkerReviewSourceStub{job: reviewJob},
			review:        &placementWorkerReviewStub{err: errReview},
			commit:        &placementWorkerCommitStub{},
			wantProcessed: true,
			wantErr:       errReview,
			wantRetry:     true,
			wantReason:    "semantic review failed before completion",
		},
		{
			name:          "nil review result is a typed retryable worker error",
			ledger:        &placementWorkerLedgerStub{run: run},
			reviewSource:  &placementWorkerReviewSourceStub{job: reviewJob},
			review:        &placementWorkerReviewStub{returnNil: true},
			commit:        &placementWorkerCommitStub{},
			wantProcessed: true,
			wantContains:  "review returned nil result",
			wantRetry:     true,
			wantReason:    "semantic review returned no result",
		},
		{
			name:          "commit error marks claimed run processed and retryable",
			ledger:        &placementWorkerLedgerStub{run: run},
			reviewSource:  &placementWorkerReviewSourceStub{job: reviewJob},
			review:        &placementWorkerReviewStub{result: accepted},
			commit:        &placementWorkerCommitStub{errs: []error{errCommit}},
			wantProcessed: true,
			wantErr:       errCommit,
			wantRetry:     true,
			wantReason:    "semantic commit failed before completion",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worker := NewSemanticPlacementWorkerService(SemanticPlacementWorkerDependencies{
				Ledger:       tt.ledger,
				Review:       tt.review,
				Commit:       tt.commit,
				ReviewSource: tt.reviewSource,
				TeamID:       teamID,
				WorkerID:     "worker-canonical",
			})

			processed, err := worker.ProcessNextSemanticPlacement(context.Background())
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

type placementWorkerLedgerStub struct {
	run           *repository.PlacementRun
	claimErr      error
	claimTeamID   string
	claimWorkerID string
	claimLease    time.Duration
}

func (s *placementWorkerLedgerStub) CreateIngest(context.Context, repository.CreateIngestInput) (*repository.CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *placementWorkerLedgerStub) GetPlacementRun(context.Context, repository.GetPlacementRunInput) (*repository.CreateIngestResult, error) {
	return nil, errors.New("unexpected GetPlacementRun")
}

func (s *placementWorkerLedgerStub) AdvanceSourceRevision(context.Context, repository.AdvanceSourceRevisionInput) (*repository.SourceRevisionResult, error) {
	return nil, errors.New("unexpected AdvanceSourceRevision")
}

func (s *placementWorkerLedgerStub) AppendSecurityEvent(context.Context, repository.SecurityEventInput) (string, error) {
	return "", errors.New("unexpected AppendSecurityEvent")
}

func (s *placementWorkerLedgerStub) AppendPlacementOutcome(context.Context, repository.PlacementOutcomeInput) (string, error) {
	return "", errors.New("unexpected AppendPlacementOutcome")
}

func (s *placementWorkerLedgerStub) ClaimNextPlacementRun(_ context.Context, teamID string, workerID string, lease time.Duration) (*repository.PlacementRun, error) {
	s.claimTeamID = teamID
	s.claimWorkerID = workerID
	s.claimLease = lease
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return s.run, nil
}

func (s *placementWorkerLedgerStub) FinishPlacementRun(context.Context, string, string, string, string, string) error {
	return errors.New("unexpected FinishPlacementRun")
}

type placementWorkerReviewSourceStub struct {
	err error
	job SemanticReviewJob
}

func (s *placementWorkerReviewSourceStub) BuildSemanticReviewJob(context.Context, repository.PlacementRun) (SemanticReviewJob, error) {
	if s.err != nil {
		return SemanticReviewJob{}, s.err
	}
	return s.job, nil
}

type placementWorkerReviewStub struct {
	err       error
	job       SemanticReviewJob
	result    *SemanticReviewResult
	returnNil bool
}

func (s *placementWorkerReviewStub) ReviewSemantic(_ context.Context, job SemanticReviewJob) (*SemanticReviewResult, error) {
	s.job = job
	if s.err != nil {
		return nil, s.err
	}
	if s.returnNil {
		return nil, nil
	}
	if s.result == nil {
		return &SemanticReviewResult{Status: "retryable"}, nil
	}
	return s.result, nil
}

type placementWorkerCommitStub struct {
	err  error
	errs []error
	job  SemanticCommitJob
}

func (s *placementWorkerCommitStub) CommitSemantic(context.Context, SemanticCommitJob) (*repository.CommitPlacementSemanticResult, error) {
	return nil, errors.New("unexpected CommitSemantic")
}

func (s *placementWorkerCommitStub) CompleteSemanticPlacement(_ context.Context, job SemanticCommitJob) (*SemanticPlacementCompletionResult, error) {
	s.job = job
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return &SemanticPlacementCompletionResult{Status: job.Result.Status}, nil
}
