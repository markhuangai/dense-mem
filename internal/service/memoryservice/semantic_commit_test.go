package memoryservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestV2SemanticCommitMapsAcceptedReviewIntoAtomicRepositoryInput(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	targetID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	request.Evidence[0].SourceID = uuid.NewString()
	request.Evidence[0].SourceRevisionID = uuid.NewString()
	request.EntityMentions[1].IdentityContext = map[string]any{"repo": "dense-mem"}
	validFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	validTo := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	request.RelationshipObservations[0].ValidFrom = &validFrom
	request.RelationshipObservations[0].ValidTo = &validTo
	request.RelationshipObservations[0].Polarity = "-"
	request.RelationshipObservations[0].CorrectionTarget = &verifier.V2RelationshipCorrectionTarget{
		RelationshipID:  targetID,
		ExpectedVersion: 3,
	}
	response := semanticReviewResponse(request.RequestID, false, false)
	result := semanticReviewResultFromResponse(response, 1, "sha256:semantic-response")
	result.OutcomeIDs = []string{uuid.NewString()}
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewSemanticCommitService(SemanticCommitDependencies{PlacementCommit: commitRepo})

	_, err := svc.CommitSemantic(context.Background(), SemanticCommitJob{
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
		t.Fatalf("CommitSemantic returned error: %v", err)
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
	if got.EntityResolutions[1].IdentityContext["repo"] != "dense-mem" {
		t.Fatalf("project identity context = %#v", got.EntityResolutions[1].IdentityContext)
	}
	if len(got.RelationshipObservations) != 1 {
		t.Fatalf("relationship observations = %#v", got.RelationshipObservations)
	}
	relationship := got.RelationshipObservations[0]
	if relationship.SubjectRef != "person_1" || relationship.ObjectRef != "project_1" || relationship.PredicateKey != "works_on" {
		t.Fatalf("relationship mapping = %#v", relationship)
	}
	if relationship.PredicateCandidate == nil ||
		relationship.PredicateCandidate.PredicateKey != "works_on" ||
		relationship.PredicateCandidate.PredicateVersion != 1 ||
		relationship.PredicateCandidate.RelationshipKind != "state" {
		t.Fatalf("relationship predicate candidate = %#v", relationship.PredicateCandidate)
	}
	if relationship.Polarity != "-" {
		t.Fatalf("relationship polarity = %q, want -", relationship.Polarity)
	}
	if relationship.CorrectionTarget == nil || relationship.CorrectionTarget.RelationshipID != targetID || relationship.CorrectionTarget.ExpectedVersion != 3 {
		t.Fatalf("correction target = %#v", relationship.CorrectionTarget)
	}
	if relationship.ValidFrom == nil || !relationship.ValidFrom.Equal(validFrom) {
		t.Fatalf("valid_from = %#v", relationship.ValidFrom)
	}
	if relationship.ValidTo == nil || !relationship.ValidTo.Equal(validTo) {
		t.Fatalf("valid_to = %#v", relationship.ValidTo)
	}
	if relationship.Support == nil || relationship.Support.FragmentID != request.Evidence[0].FragmentID || relationship.Support.Quote != "Mark works on Dense-Mem." {
		t.Fatalf("relationship support = %#v", relationship.Support)
	}
	if relationship.Support.SourceID != request.Evidence[0].SourceID || relationship.Support.SourceRevisionID != request.Evidence[0].SourceRevisionID {
		t.Fatalf("relationship support source scope = %#v", relationship.Support)
	}
	if got.Payload["response_hash"] != "sha256:semantic-response" {
		t.Fatalf("payload = %#v", got.Payload)
	}
}

func TestV2SemanticCommitRejectsNonAcceptedReview(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewSemanticCommitService(SemanticCommitDependencies{PlacementCommit: commitRepo})

	_, err := svc.CommitSemantic(context.Background(), SemanticCommitJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		WorkerID:        "worker-commit",
		Request:         request,
		Result: SemanticReviewResult{
			Status: string(domain.V2SemanticReviewReviewRequired),
		},
	})
	if !errors.Is(err, ErrSemanticCommitNotAccepted) {
		t.Fatalf("err = %v", err)
	}
	if commitRepo.called {
		t.Fatal("commit repository was called for non-accepted review")
	}
}

func TestV2SemanticPlacementCompletionClosesReviewRequiredWithoutSemanticCommit(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewSemanticCommitService(SemanticCommitDependencies{PlacementCommit: commitRepo})

	completed, err := svc.CompleteSemanticPlacement(context.Background(), SemanticCommitJob{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         uuid.NewString(),
		PlacementRunID:   uuid.NewString(),
		PlacementItemID:  uuid.NewString(),
		WorkerID:         "worker-commit",
		ExpectedAttempts: 1,
		Request:          request,
		Result: SemanticReviewResult{
			Status:       string(domain.V2SemanticReviewReviewRequired),
			ResponseHash: "sha256:review-required",
			OutcomeIDs:   []string{uuid.NewString()},
		},
		ReviewModel: "stub-semantic-reviewer",
	})
	if err != nil {
		t.Fatalf("CompleteSemanticPlacement returned error: %v", err)
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
	request := semanticReviewServiceRequest(teamID, ownerID)
	result := semanticReviewResultFromResponse(semanticReviewResponse(request.RequestID, false, false), 1, "sha256:accepted")
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewSemanticCommitService(SemanticCommitDependencies{PlacementCommit: commitRepo})

	completed, err := svc.CompleteSemanticPlacement(context.Background(), SemanticCommitJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		WorkerID:        "worker-commit",
		Request:         request,
		Result:          *result,
		ReviewModel:     "stub-semantic-reviewer",
	})
	if err != nil {
		t.Fatalf("CompleteSemanticPlacement returned error: %v", err)
	}
	if completed.Status != string(domain.V2SemanticReviewAccepted) || completed.SemanticCommit == nil || completed.Terminal != nil {
		t.Fatalf("completed = %#v", completed)
	}
	if !commitRepo.called || commitRepo.terminalCalled {
		t.Fatalf("commit paths called = semantic:%v terminal:%v", commitRepo.called, commitRepo.terminalCalled)
	}
}

func TestV2SemanticPlacementCompletionRequeuesRetryableReview(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewSemanticCommitService(SemanticCommitDependencies{PlacementCommit: commitRepo})

	completed, err := svc.CompleteSemanticPlacement(context.Background(), SemanticCommitJob{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         uuid.NewString(),
		PlacementRunID:   uuid.NewString(),
		PlacementItemID:  uuid.NewString(),
		WorkerID:         "worker-commit",
		ExpectedAttempts: 2,
		MaxAttempts:      3,
		Result:           SemanticReviewResult{Status: string(domain.V2SemanticReviewRetryable)},
	})
	if err != nil {
		t.Fatalf("CompleteSemanticPlacement returned error: %v", err)
	}
	if completed.Status != string(domain.V2SemanticReviewRetryable) || completed.SemanticCommit != nil || completed.Terminal != nil {
		t.Fatalf("completed = %#v", completed)
	}
	if commitRepo.called || commitRepo.terminalCalled || !commitRepo.retryCalled {
		t.Fatalf("repository paths called = semantic:%v terminal:%v retry:%v", commitRepo.called, commitRepo.terminalCalled, commitRepo.retryCalled)
	}
	if commitRepo.retryInput.ExpectedAttempts != 2 || commitRepo.retryInput.WorkerID != "worker-commit" {
		t.Fatalf("retry input = %#v", commitRepo.retryInput)
	}
}

func TestV2SemanticPlacementCompletionClosesRetryableWhenPlacementAttemptsExhausted(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewSemanticCommitService(SemanticCommitDependencies{PlacementCommit: commitRepo})

	completed, err := svc.CompleteSemanticPlacement(context.Background(), SemanticCommitJob{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         uuid.NewString(),
		PlacementRunID:   uuid.NewString(),
		PlacementItemID:  uuid.NewString(),
		WorkerID:         "worker-commit",
		ExpectedAttempts: 3,
		MaxAttempts:      3,
		Request:          request,
		Result: SemanticReviewResult{
			Status:       string(domain.V2SemanticReviewRetryable),
			ResponseHash: "sha256:retryable",
			OutcomeIDs:   []string{uuid.NewString()},
			FailureStage: semanticFailureStageVerification,
			FailureClass: semanticFailureClassTimeout,
		},
		ReviewModel: "stub-semantic-reviewer",
	})
	if err != nil {
		t.Fatalf("CompleteSemanticPlacement returned error: %v", err)
	}
	if completed.Status != string(domain.V2SemanticReviewTerminalFailure) || completed.Terminal == nil || completed.SemanticCommit != nil {
		t.Fatalf("completed = %#v", completed)
	}
	if commitRepo.called || !commitRepo.terminalCalled || commitRepo.retryCalled {
		t.Fatalf("repository paths called = semantic:%v terminal:%v retry:%v", commitRepo.called, commitRepo.terminalCalled, commitRepo.retryCalled)
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
	if commitRepo.terminalInput.Payload["failure_stage"] != semanticFailureStageVerification ||
		commitRepo.terminalInput.Payload["failure_class"] != semanticFailureClassTimeout ||
		commitRepo.terminalInput.Payload["retryable_exhausted"] != true {
		t.Fatalf("terminal failure payload = %#v", commitRepo.terminalInput.Payload)
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
			svc := NewSemanticCommitService(SemanticCommitDependencies{PlacementCommit: commitRepo})

			completed, err := svc.CompleteSemanticPlacement(context.Background(), SemanticCommitJob{
				TeamID:          " team-v2 ",
				OwnerProfileID:  " owner-v2 ",
				IngestID:        " ingest-v2 ",
				PlacementRunID:  " run-v2 ",
				PlacementItemID: " item-v2 ",
				WorkerID:        " worker-v2 ",
				Result:          SemanticReviewResult{Status: tt.status, ResponseHash: " hash-v2 "},
			})
			if err != nil {
				t.Fatalf("CompleteSemanticPlacement returned error: %v", err)
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
	request := semanticReviewServiceRequest(teamID, ownerID)
	request.RelationshipObservations[0].ObjectRef = ""
	request.RelationshipObservations[0].ObjectValue = &verifier.V2SemanticValueObservation{
		Ref:     "release_date",
		Type:    "date",
		Value:   "2026-07-17",
		Display: "July 17, 2026",
		Unit:    "calendar_day",
	}
	result := semanticReviewResultFromResponse(semanticReviewResponse(request.RequestID, false, false), 1, "sha256:value")
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewSemanticCommitService(SemanticCommitDependencies{PlacementCommit: commitRepo})

	_, err := svc.CommitSemantic(context.Background(), SemanticCommitJob{
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
		t.Fatalf("CommitSemantic returned error: %v", err)
	}
	if commitRepo.input.Category != "fact" {
		t.Fatalf("category = %q", commitRepo.input.Category)
	}
	relationship := commitRepo.input.RelationshipObservations[0]
	if relationship.ObjectRef != "" || relationship.ObjectValue == nil {
		t.Fatalf("relationship object = %#v", relationship)
	}
	if relationship.ObjectValue.Ref != "release_date" ||
		relationship.ObjectValue.CanonicalValue != "2026-07-17" ||
		relationship.ObjectValue.Display != "July 17, 2026" ||
		relationship.ObjectValue.Unit != "calendar_day" {
		t.Fatalf("object value = %#v", relationship.ObjectValue)
	}
}

func TestV2SemanticCommitRejectsMismatchedReviewRefsBeforeRepositoryCall(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	tests := []struct {
		name   string
		mutate func(*SemanticReviewResult)
	}{
		{
			name: "unknown entity ref",
			mutate: func(result *SemanticReviewResult) {
				result.EntityResults[0].Ref = "missing_entity"
			},
		},
		{
			name: "unknown relationship ref",
			mutate: func(result *SemanticReviewResult) {
				result.RelationshipResults[0].Ref = "missing_relationship"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := semanticReviewServiceRequest(teamID, ownerID)
			result := semanticReviewResultFromResponse(semanticReviewResponse(request.RequestID, false, false), 1, "sha256:bad-ref")
			tt.mutate(result)
			commitRepo := &v2SemanticCommitRepoStub{}
			svc := NewSemanticCommitService(SemanticCommitDependencies{PlacementCommit: commitRepo})

			_, err := svc.CommitSemantic(context.Background(), SemanticCommitJob{
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

func TestV2SemanticCommitMapsUnresolvedPredicateToRelationshipReview(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	request.RelationshipObservations[0].Polarity = "-"
	result := semanticReviewResultFromResponse(semanticReviewResponse(request.RequestID, false, false), 1, "sha256:unresolved-predicate")
	result.RelationshipResults[0].PredicateStatus = "ambiguous"
	result.RelationshipResults[0].PredicateKey = nil
	commitRepo := &v2SemanticCommitRepoStub{}
	svc := NewSemanticCommitService(SemanticCommitDependencies{PlacementCommit: commitRepo})

	_, err := svc.CommitSemantic(context.Background(), SemanticCommitJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		WorkerID:        "worker-commit",
		Request:         request,
		Result:          *result,
		ReviewModel:     "stub-semantic-reviewer",
	})
	if err != nil {
		t.Fatalf("CommitSemantic returned error: %v", err)
	}
	if !commitRepo.called || commitRepo.terminalCalled {
		t.Fatalf("repository paths called = semantic:%v terminal:%v", commitRepo.called, commitRepo.terminalCalled)
	}
	if len(commitRepo.input.RelationshipObservations) != 0 {
		t.Fatalf("canonical relationship observations = %#v", commitRepo.input.RelationshipObservations)
	}
	if len(commitRepo.input.RelationshipReviews) != 1 {
		t.Fatalf("relationship reviews = %#v", commitRepo.input.RelationshipReviews)
	}
	review := commitRepo.input.RelationshipReviews[0]
	if review.Ref != request.RelationshipObservations[0].Ref || review.Reason != "predicate_needs_review" {
		t.Fatalf("relationship review = %#v", review)
	}
	if review.Polarity != "-" {
		t.Fatalf("relationship review polarity = %q, want -", review.Polarity)
	}
	if review.Confidence == nil || *review.Confidence != result.RelationshipResults[0].Confidence ||
		review.Rationale != result.RelationshipResults[0].Rationale ||
		review.Model != "stub-semantic-reviewer" ||
		review.ResponseHash != result.ResponseHash {
		t.Fatalf("relationship review verification = %#v", review)
	}
	if review.Support == nil ||
		review.Support.FragmentID != request.Evidence[0].FragmentID ||
		review.Support.Quote != request.RelationshipObservations[0].Quote {
		t.Fatalf("relationship review support = %#v", review.Support)
	}
	if review.Payload["predicate_policy_version"] != domain.V2PredicatePolicyVersion {
		t.Fatalf("relationship review payload = %#v", review.Payload)
	}
}

func TestV2SemanticCommitRequiresPlacementRepository(t *testing.T) {
	svc := NewSemanticCommitService(SemanticCommitDependencies{})
	if _, err := svc.CommitSemantic(context.Background(), SemanticCommitJob{}); err == nil {
		t.Fatal("CommitSemantic returned nil error")
	}
	if _, err := svc.CompleteSemanticPlacement(context.Background(), SemanticCommitJob{}); err == nil {
		t.Fatal("CompleteSemanticPlacement returned nil error")
	}
}

type v2SemanticCommitRepoStub struct {
	called         bool
	terminalCalled bool
	retryCalled    bool
	input          repository.V2CommitPlacementSemanticInput
	terminalInput  repository.V2CompletePlacementReviewInput
	retryInput     repository.V2RequeuePlacementReviewInput
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

func (s *v2SemanticCommitRepoStub) RequeuePlacementReviewResult(
	_ context.Context,
	input repository.V2RequeuePlacementReviewInput,
) (*repository.V2RequeuePlacementReviewResult, error) {
	s.retryCalled = true
	s.retryInput = input
	return &repository.V2RequeuePlacementReviewResult{Status: string(domain.V2SemanticReviewRetryable)}, nil
}

func v2TerminalResponseHash(input repository.V2CompletePlacementReviewInput) string {
	value, _ := input.Payload["response_hash"].(string)
	return value
}
