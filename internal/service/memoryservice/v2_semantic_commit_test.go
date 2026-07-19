package memoryservice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestV2SemanticCommitMapsAcceptedReviewIntoAtomicRepositoryInput(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	response := v2SemanticReviewResponse(request.RequestID, false, false)
	result := v2SemanticReviewResultFromResponse(response, 1, "sha256:semantic-response")
	result.OutcomeIDs = []string{uuid.NewString()}
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewV2SemanticCommitService(V2SemanticCommitDependencies{PlacementCommit: commitRepo})

	_, err := svc.CommitV2Semantic(context.Background(), V2SemanticCommitJob{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         uuid.NewString(),
		PlacementRunID:   uuid.NewString(),
		PlacementItemID:  uuid.NewString(),
		WorkerID:         "worker-commit",
		ExpectedAttempts: 2,
		Request:          request,
		Result:           *result,
		ReviewModel:      "stub-semantic-reviewer",
	})
	if err != nil {
		t.Fatalf("CommitV2Semantic returned error: %v", err)
	}

	got := commitRepo.input
	if got.TeamID != teamID || got.OwnerProfileID != ownerID || got.WorkerID != "worker-commit" || got.ExpectedAttempts != 2 {
		t.Fatalf("commit scope = %#v", got)
	}
	if got.Status != string(domain.V2SemanticReviewAccepted) || got.Category != "validated_claim" {
		t.Fatalf("commit status/category = %q/%q", got.Status, got.Category)
	}
	if len(got.EntityResolutions) != 2 {
		t.Fatalf("entity resolutions = %#v", got.EntityResolutions)
	}
	if got.EntityResolutions[0].MentionRef != "person_1" || got.EntityResolutions[0].Action != "reuse" || got.EntityResolutions[0].EntityID != "ent-mark" {
		t.Fatalf("person resolution = %#v", got.EntityResolutions[0])
	}
	if got.EntityResolutions[1].MentionRef != "project_1" || got.EntityResolutions[1].EntityID != "ent-dense-mem" {
		t.Fatalf("project resolution = %#v", got.EntityResolutions[1])
	}
	if len(got.RelationshipObservations) != 1 {
		t.Fatalf("relationship observations = %#v", got.RelationshipObservations)
	}
	relationship := got.RelationshipObservations[0]
	if relationship.SubjectRef != "person_1" || relationship.ObjectRef != "project_1" || relationship.PredicateKey != "works_on" {
		t.Fatalf("relationship mapping = %#v", relationship)
	}
	if relationship.Support == nil || relationship.Support.FragmentID != request.Evidence[0].FragmentID || relationship.Support.Quote != "Mark works on Dense-Mem." {
		t.Fatalf("relationship support = %#v", relationship.Support)
	}
	if got.Payload["response_hash"] != "sha256:semantic-response" {
		t.Fatalf("payload = %#v", got.Payload)
	}
}

func TestV2SemanticCommitRejectsNonAcceptedReview(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewV2SemanticCommitService(V2SemanticCommitDependencies{PlacementCommit: commitRepo})

	_, err := svc.CommitV2Semantic(context.Background(), V2SemanticCommitJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		WorkerID:        "worker-commit",
		Request:         request,
		Result: V2SemanticReviewResult{
			Status: string(domain.V2SemanticReviewReviewRequired),
		},
	})
	if !errors.Is(err, ErrV2SemanticCommitNotAccepted) {
		t.Fatalf("err = %v", err)
	}
	if commitRepo.called {
		t.Fatal("commit repository was called for non-accepted review")
	}
}

func TestV2SemanticPlacementCompletionClosesReviewRequiredWithoutSemanticCommit(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewV2SemanticCommitService(V2SemanticCommitDependencies{PlacementCommit: commitRepo})

	completed, err := svc.CompleteV2SemanticPlacement(context.Background(), V2SemanticCommitJob{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         uuid.NewString(),
		PlacementRunID:   uuid.NewString(),
		PlacementItemID:  uuid.NewString(),
		WorkerID:         "worker-commit",
		ExpectedAttempts: 1,
		Request:          request,
		Result: V2SemanticReviewResult{
			Status:       string(domain.V2SemanticReviewReviewRequired),
			ResponseHash: "sha256:review-required",
			OutcomeIDs:   []string{uuid.NewString()},
		},
		ReviewModel: "stub-semantic-reviewer",
	})
	if err != nil {
		t.Fatalf("CompleteV2SemanticPlacement returned error: %v", err)
	}
	if completed.Status != string(domain.V2SemanticReviewReviewRequired) || completed.Terminal == nil || completed.SemanticCommit != nil {
		t.Fatalf("completed = %#v", completed)
	}
	if commitRepo.called {
		t.Fatal("semantic commit repository path was called")
	}
	if !commitRepo.terminalCalled {
		t.Fatal("terminal repository path was not called")
	}
	if commitRepo.terminalInput.Category != "candidate" || commitRepo.terminalInput.Status != string(domain.V2SemanticReviewReviewRequired) {
		t.Fatalf("terminal input = %#v", commitRepo.terminalInput)
	}
}

func TestV2SemanticPlacementCompletionCommitsAcceptedReview(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	result := v2SemanticReviewResultFromResponse(v2SemanticReviewResponse(request.RequestID, false, false), 1, "sha256:accepted")
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewV2SemanticCommitService(V2SemanticCommitDependencies{PlacementCommit: commitRepo})

	completed, err := svc.CompleteV2SemanticPlacement(context.Background(), V2SemanticCommitJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		WorkerID:        "worker-commit",
		Request:         request,
		Result:          *result,
	})
	if err != nil {
		t.Fatalf("CompleteV2SemanticPlacement returned error: %v", err)
	}
	if completed.Status != string(domain.V2SemanticReviewAccepted) || completed.SemanticCommit == nil || completed.Terminal != nil {
		t.Fatalf("completed = %#v", completed)
	}
	if !commitRepo.called || commitRepo.terminalCalled {
		t.Fatalf("commit paths called = semantic:%v terminal:%v", commitRepo.called, commitRepo.terminalCalled)
	}
}

func TestV2SemanticPlacementCompletionKeepsRetryableLeaseOpen(t *testing.T) {
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewV2SemanticCommitService(V2SemanticCommitDependencies{PlacementCommit: commitRepo})

	completed, err := svc.CompleteV2SemanticPlacement(context.Background(), V2SemanticCommitJob{
		ExpectedAttempts: 2,
		MaxAttempts:      3,
		Result:           V2SemanticReviewResult{Status: string(domain.V2SemanticReviewRetryable)},
	})
	if err != nil {
		t.Fatalf("CompleteV2SemanticPlacement returned error: %v", err)
	}
	if completed.Status != string(domain.V2SemanticReviewRetryable) || completed.SemanticCommit != nil || completed.Terminal != nil {
		t.Fatalf("completed = %#v", completed)
	}
	if commitRepo.called || commitRepo.terminalCalled {
		t.Fatalf("repository paths called = semantic:%v terminal:%v", commitRepo.called, commitRepo.terminalCalled)
	}
}

func TestV2SemanticPlacementCompletionClosesRetryableWhenPlacementAttemptsExhausted(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewV2SemanticCommitService(V2SemanticCommitDependencies{PlacementCommit: commitRepo})

	completed, err := svc.CompleteV2SemanticPlacement(context.Background(), V2SemanticCommitJob{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         uuid.NewString(),
		PlacementRunID:   uuid.NewString(),
		PlacementItemID:  uuid.NewString(),
		WorkerID:         "worker-commit",
		ExpectedAttempts: 3,
		MaxAttempts:      3,
		Request:          request,
		Result: V2SemanticReviewResult{
			Status:       string(domain.V2SemanticReviewRetryable),
			ResponseHash: "sha256:retryable",
			OutcomeIDs:   []string{uuid.NewString()},
		},
		ReviewModel: "stub-semantic-reviewer",
	})
	if err != nil {
		t.Fatalf("CompleteV2SemanticPlacement returned error: %v", err)
	}
	if completed.Status != string(domain.V2SemanticReviewTerminalFailure) || completed.Terminal == nil || completed.SemanticCommit != nil {
		t.Fatalf("completed = %#v", completed)
	}
	if commitRepo.called || !commitRepo.terminalCalled {
		t.Fatalf("repository paths called = semantic:%v terminal:%v", commitRepo.called, commitRepo.terminalCalled)
	}
	if commitRepo.terminalInput.Status != string(domain.V2SemanticReviewTerminalFailure) || commitRepo.terminalInput.Category != "failed" {
		t.Fatalf("terminal input = %#v", commitRepo.terminalInput)
	}
	if commitRepo.terminalInput.Payload["placement_attempts"] != 3 || commitRepo.terminalInput.Payload["max_attempts"] != 3 {
		t.Fatalf("terminal payload = %#v", commitRepo.terminalInput.Payload)
	}
	messages, ok := commitRepo.terminalInput.Payload["validation_errors"].([]string)
	if !ok || len(messages) != 1 || messages[0] != "placement_attempts: retryable semantic review exhausted placement attempts" {
		t.Fatalf("terminal validation errors = %#v", commitRepo.terminalInput.Payload["validation_errors"])
	}
}

func TestV2SemanticPlacementCompletionMapsTerminalCategories(t *testing.T) {
	tests := []struct {
		status   string
		category string
	}{
		{status: string(domain.V2SemanticReviewRejected), category: "candidate"},
		{status: string(domain.V2SemanticReviewQuarantined), category: "quarantined"},
		{status: string(domain.V2SemanticReviewTerminalFailure), category: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			commitRepo := &v2SemanticCommitRepoStub{}
			svc := NewV2SemanticCommitService(V2SemanticCommitDependencies{PlacementCommit: commitRepo})

			completed, err := svc.CompleteV2SemanticPlacement(context.Background(), V2SemanticCommitJob{
				TeamID:          " team-v2 ",
				OwnerProfileID:  " owner-v2 ",
				IngestID:        " ingest-v2 ",
				PlacementRunID:  " run-v2 ",
				PlacementItemID: " item-v2 ",
				WorkerID:        " worker-v2 ",
				Result:          V2SemanticReviewResult{Status: tt.status, ResponseHash: " hash-v2 "},
			})
			if err != nil {
				t.Fatalf("CompleteV2SemanticPlacement returned error: %v", err)
			}
			if completed.Status != tt.status || completed.Terminal == nil {
				t.Fatalf("completed = %#v", completed)
			}
			if commitRepo.terminalInput.Category != tt.category || v2TerminalResponseHash(commitRepo.terminalInput) != "hash-v2" {
				t.Fatalf("terminal input = %#v", commitRepo.terminalInput)
			}
			if commitRepo.terminalInput.TeamID != "team-v2" || commitRepo.terminalInput.WorkerID != "worker-v2" {
				t.Fatalf("normalized terminal scope = %#v", commitRepo.terminalInput)
			}
		})
	}
}

func TestV2SemanticCommitMapsObjectValueRelationship(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	request.RelationshipObservations[0].ObjectRef = ""
	request.RelationshipObservations[0].ObjectValue = &verifier.V2SemanticValueObservation{
		Ref:   "release_date",
		Type:  "date",
		Value: "2026-07-17",
	}
	result := v2SemanticReviewResultFromResponse(v2SemanticReviewResponse(request.RequestID, false, false), 1, "sha256:value")
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewV2SemanticCommitService(V2SemanticCommitDependencies{PlacementCommit: commitRepo})

	_, err := svc.CommitV2Semantic(context.Background(), V2SemanticCommitJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		WorkerID:        "worker-commit",
		Request:         request,
		Result:          *result,
		PromoteToFact:   true,
	})
	if err != nil {
		t.Fatalf("CommitV2Semantic returned error: %v", err)
	}
	if commitRepo.input.Category != "fact" {
		t.Fatalf("category = %q", commitRepo.input.Category)
	}
	relationship := commitRepo.input.RelationshipObservations[0]
	if relationship.ObjectRef != "" || relationship.ObjectValue == nil {
		t.Fatalf("relationship object = %#v", relationship)
	}
	if relationship.ObjectValue.Ref != "release_date" || relationship.ObjectValue.CanonicalValue != "2026-07-17" {
		t.Fatalf("object value = %#v", relationship.ObjectValue)
	}
}

func TestV2SemanticCommitRejectsMismatchedReviewRefsBeforeRepositoryCall(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	tests := []struct {
		name   string
		mutate func(*V2SemanticReviewResult)
	}{
		{
			name: "unknown entity ref",
			mutate: func(result *V2SemanticReviewResult) {
				result.EntityResults[0].Ref = "missing_entity"
			},
		},
		{
			name: "unknown relationship ref",
			mutate: func(result *V2SemanticReviewResult) {
				result.RelationshipResults[0].Ref = "missing_relationship"
			},
		},
		{
			name: "unresolved predicate",
			mutate: func(result *V2SemanticReviewResult) {
				result.RelationshipResults[0].PredicateStatus = "ambiguous"
				result.RelationshipResults[0].PredicateKey = nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := v2SemanticReviewServiceRequest(teamID, ownerID)
			result := v2SemanticReviewResultFromResponse(v2SemanticReviewResponse(request.RequestID, false, false), 1, "sha256:bad-ref")
			tt.mutate(result)
			commitRepo := &v2SemanticCommitRepoStub{}
			svc := NewV2SemanticCommitService(V2SemanticCommitDependencies{PlacementCommit: commitRepo})

			_, err := svc.CommitV2Semantic(context.Background(), V2SemanticCommitJob{
				TeamID:          teamID,
				OwnerProfileID:  ownerID,
				IngestID:        uuid.NewString(),
				PlacementRunID:  uuid.NewString(),
				PlacementItemID: uuid.NewString(),
				WorkerID:        "worker-commit",
				Request:         request,
				Result:          *result,
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if commitRepo.called || commitRepo.terminalCalled {
				t.Fatalf("repository paths called = semantic:%v terminal:%v", commitRepo.called, commitRepo.terminalCalled)
			}
		})
	}
}

func TestV2SemanticCommitRequiresPlacementRepository(t *testing.T) {
	svc := NewV2SemanticCommitService(V2SemanticCommitDependencies{})
	if _, err := svc.CommitV2Semantic(context.Background(), V2SemanticCommitJob{}); err == nil {
		t.Fatal("CommitV2Semantic returned nil error")
	}
	if _, err := svc.CompleteV2SemanticPlacement(context.Background(), V2SemanticCommitJob{}); err == nil {
		t.Fatal("CompleteV2SemanticPlacement returned nil error")
	}
}

type v2SemanticCommitRepoStub struct {
	called         bool
	terminalCalled bool
	input          repository.V2CommitPlacementSemanticInput
	terminalInput  repository.V2CompletePlacementReviewInput
}

func (s *v2SemanticCommitRepoStub) CommitPlacementSemanticResult(
	_ context.Context,
	input repository.V2CommitPlacementSemanticInput,
) (*repository.V2CommitPlacementSemanticResult, error) {
	s.called = true
	s.input = input
	return &repository.V2CommitPlacementSemanticResult{Status: input.Status, OutcomeID: uuid.NewString()}, nil
}

func (s *v2SemanticCommitRepoStub) CompletePlacementReviewResult(
	_ context.Context,
	input repository.V2CompletePlacementReviewInput,
) (*repository.V2CompletePlacementReviewResult, error) {
	s.terminalCalled = true
	s.terminalInput = input
	return &repository.V2CompletePlacementReviewResult{Status: input.Status, OutcomeID: uuid.NewString()}, nil
}

func v2TerminalResponseHash(input repository.V2CompletePlacementReviewInput) string {
	value, _ := input.Payload["response_hash"].(string)
	return value
}
