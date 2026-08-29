package memoryservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func TestLifecycleCorrectRelationshipUsesAuthenticatedOwner(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	relationshipID := uuid.NewString()
	evidenceID := uuid.NewString()
	semantic := &lifecycleSemanticStub{correctResult: &repository.CorrectRelationshipResult{
		SubmissionID: uuid.NewString(), ProcessingState: "completed",
	}}
	svc := NewLifecycleService(LifecycleDependencies{Semantic: semantic})

	result, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{
		Action: "submit", RelationshipID: relationshipID, ExpectedVersion: 3,
		Patch:    repository.RelationshipCorrectionPatch{Predicate: &repository.RelationshipCorrectionPredicatePatch{Key: "works_with"}},
		Supports: []repository.RelationshipCorrectionSupport{{EvidenceID: evidenceID, Start: 0, End: 8}},
		Reason:   "predicate was resolved incorrectly", IdempotencyKey: "relationship-correction-1",
	})
	require.NoError(t, err)
	require.Equal(t, semantic.correctResult.SubmissionID, result.SubmissionID)
	require.Equal(t, "relationship_correction", result.SubmissionKind)
	require.Equal(t, "completed", result.ProcessingState)
	require.Equal(t, rememberStatusTool, result.StatusTool)
	require.Equal(t, teamID.String(), semantic.correctInput.TeamID)
	require.Equal(t, profileID.String(), semantic.correctInput.OwnerProfileID)
	require.Equal(t, relationshipID, semantic.correctInput.RelationshipID)
	require.Equal(t, 3, semantic.correctInput.ExpectedVersion)
}

func TestLifecycleCorrectRelationshipExecutesOnePlannedBatchBeforeCommit(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{
		plan: &repository.RelationshipCorrectionEmbeddingPlan{
			Documents:           []repository.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
			EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
		},
		correctResult: &repository.CorrectRelationshipResult{SubmissionID: uuid.NewString(), ProcessingState: "completed"},
	}
	executor := &lifecycleExecutorStub{result: semanticwrite.Result{Fence: semanticwrite.Fence{Model: semantic.plan.EmbeddingModel, Dimensions: 2, EmbeddingContractID: semantic.plan.EmbeddingContractID, SearchGenerationID: semantic.plan.SearchIndexGenerationID, SearchGenerationVersion: 1}, Embeddings: []semanticwrite.Embedding{{DocumentHash: "hash", Vector: []float32{1, 2}}}}}
	svc := NewLifecycleService(LifecycleDependencies{Semantic: semantic, CorrectionExecutor: executor})
	_, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: repository.RelationshipCorrectionPatch{Predicate: &repository.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []repository.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "planned-correction"})
	require.NoError(t, err)
	require.Equal(t, 1, executor.calls)
	require.Len(t, semantic.embeddings, 1)
}

func TestLifecycleCorrectRelationshipDoesNotCommitWhenEmbeddingFails(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{plan: &repository.RelationshipCorrectionEmbeddingPlan{
		Documents:           []repository.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
		EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
	}, correctResult: &repository.CorrectRelationshipResult{SubmissionID: uuid.NewString(), ProcessingState: "completed"}}
	svc := NewLifecycleService(LifecycleDependencies{Semantic: semantic, CorrectionExecutor: &lifecycleExecutorStub{err: semanticwrite.ErrProviderUnavailable}})
	_, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: repository.RelationshipCorrectionPatch{Predicate: &repository.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []repository.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "failed-planned-correction"})
	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.ErrEmbeddingUnavailable, publicErr.Code)
	require.Zero(t, semantic.commitCalls)
}

func TestLifecycleCorrectRelationshipDoesNotCommitWhenEmbeddingTimesOut(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{plan: &repository.RelationshipCorrectionEmbeddingPlan{
		Documents:           []repository.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
		EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
	}, correctResult: &repository.CorrectRelationshipResult{SubmissionID: uuid.NewString(), ProcessingState: "completed"}}
	svc := NewLifecycleService(LifecycleDependencies{Semantic: semantic, CorrectionExecutor: &lifecycleExecutorStub{err: semanticwrite.ErrProviderTimeout}})

	_, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: repository.RelationshipCorrectionPatch{Predicate: &repository.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []repository.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "timed-out-planned-correction"})

	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.ErrEmbeddingTimeout, publicErr.Code)
	require.Zero(t, semantic.commitCalls)
}

func TestLifecycleCorrectRelationshipClassifiesConfiguredEmbeddingDeadline(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{plan: &repository.RelationshipCorrectionEmbeddingPlan{
		Documents:           []repository.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
		EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
	}}
	svc := NewLifecycleService(LifecycleDependencies{Semantic: semantic, CorrectionExecutor: &lifecycleExecutorStub{waitForContext: true}, CorrectionEmbeddingTimeout: 10 * time.Millisecond})

	_, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: repository.RelationshipCorrectionPatch{Predicate: &repository.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []repository.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "configured-timeout-correction"})

	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.ErrEmbeddingTimeout, publicErr.Code)
	require.Zero(t, semantic.commitCalls)
}

func TestLifecycleCorrectRelationshipPreservesCallerDeadline(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{plan: &repository.RelationshipCorrectionEmbeddingPlan{
		Documents:           []repository.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
		EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
	}}
	executor := &lifecycleExecutorStub{waitForContext: true}
	svc := NewLifecycleService(LifecycleDependencies{Semantic: semantic, CorrectionExecutor: executor, CorrectionEmbeddingTimeout: time.Second})
	callerCtx, cancel := context.WithTimeout(authenticatedRememberContext(teamID, profileID, uuid.New()), 10*time.Millisecond)
	defer cancel()

	_, err := svc.CorrectRelationship(callerCtx, CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: repository.RelationshipCorrectionPatch{Predicate: &repository.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []repository.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "caller-deadline-correction"})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	var publicErr *httperr.APIError
	require.NotErrorAs(t, err, &publicErr)
	require.Zero(t, semantic.commitCalls)
}

func TestLifecycleRelationshipCorrectionStatusPreservesConfirmationWorkflow(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	submissionID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(time.Hour)
	semantic := &lifecycleSemanticStub{statusResult: &repository.RelationshipCorrectionStatus{
		SubmissionID: submissionID, ProcessingState: "awaiting_confirmation", SearchState: string(domain.SearchProjectionPending),
		Confirmation: &repository.RelationshipCorrectionConfirmation{Token: uuid.NewString(), ExpiresAt: expiresAt},
	}}
	svc := NewLifecycleService(LifecycleDependencies{Semantic: semantic})

	result, err := svc.GetRelationshipCorrectionStatus(authenticatedRememberContext(teamID, profileID, uuid.New()), GetSubmissionStatusRequest{SubmissionID: submissionID})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", result.ProcessingState)
	require.Equal(t, string(domain.SearchProjectionPending), result.SearchState)
	require.NotNil(t, result.AwaitingConfirmation)
	require.Equal(t, teamID.String(), semantic.statusInput.TeamID)
	require.Equal(t, profileID.String(), semantic.statusInput.OwnerProfileID)

	_, err = svc.GetRelationshipCorrectionStatus(authenticatedRememberContext(teamID, profileID, uuid.New()), GetSubmissionStatusRequest{SubmissionID: "not-a-uuid"})
	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.NOT_FOUND, publicErr.Code)
}

func TestLifecycleRelationshipCorrectionErrorsAreBounded(t *testing.T) {
	ctx := authenticatedRememberContext(uuid.New(), uuid.New(), uuid.New())
	repositoryFailure := errors.New("database host and query details")
	svc := NewLifecycleService(LifecycleDependencies{Semantic: &lifecycleSemanticStub{err: repositoryFailure}})
	_, err := svc.CorrectRelationship(ctx, CorrectRelationshipRequest{Action: "submit"})
	require.ErrorIs(t, err, ErrLifecyclePersistence)
	require.NotContains(t, err.Error(), repositoryFailure.Error())

	unsafeAction := strings.Repeat("client-controlled-", 100)
	_, err = svc.CorrectRelationship(ctx, CorrectRelationshipRequest{Action: unsafeAction})
	require.ErrorContains(t, err, "action must be submit or confirm")
	require.NotContains(t, err.Error(), unsafeAction)
}

func TestTranslateRelationshipCorrectionErrorMapsSearchFencesToConflict(t *testing.T) {
	for _, cause := range []error{repository.ErrSearchEmbeddingRequired, repository.ErrSearchContractMismatch, repository.ErrSearchStaleVersion} {
		err := translateRelationshipCorrectionError(cause)
		var publicErr *httperr.APIError
		require.ErrorAs(t, err, &publicErr)
		require.Equal(t, httperr.CONFLICT, publicErr.Code)
	}
}

func TestTranslateRelationshipCorrectionIdempotencyConflictPreservesTerminalClassification(t *testing.T) {
	err := translateRelationshipCorrectionError(repository.ErrSemanticIdempotencyConflict)
	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.CONFLICT, publicErr.Code)
	require.Contains(t, publicErr.Details, httperr.ErrorDetail{Field: "reason", Message: "idempotency_conflict"})
}

func TestTranslateRelationshipCorrectionConfirmationExpiryPreservesTerminalClassification(t *testing.T) {
	err := translateRelationshipCorrectionError(repository.ErrRelationshipCorrectionConfirmationExpired)
	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.CONFLICT, publicErr.Code)
	require.Contains(t, publicErr.Details, httperr.ErrorDetail{Field: "reason", Message: string(SubmissionErrorConfirmationExpired)})
}

func TestTranslateRelationshipCorrectionErrorMapsEmbeddingFailures(t *testing.T) {
	for _, test := range []struct {
		err  error
		code httperr.ErrorCode
	}{
		{ErrLifecycleEmbeddingUnavailable, httperr.ErrEmbeddingUnavailable},
		{ErrLifecycleEmbeddingInvalid, httperr.ErrEmbeddingResponseInvalid},
		{ErrLifecycleEmbeddingTimeout, httperr.ErrEmbeddingTimeout},
	} {
		err := translateRelationshipCorrectionError(test.err)
		var publicErr *httperr.APIError
		require.ErrorAs(t, err, &publicErr)
		require.Equal(t, test.code, publicErr.Code)
	}
}

func TestLifecycleCorrectRelationshipRequiresAuthAndRepository(t *testing.T) {
	ctx := authenticatedRememberContext(uuid.New(), uuid.New(), uuid.New())
	req := CorrectRelationshipRequest{
		Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1,
		Patch:    repository.RelationshipCorrectionPatch{Predicate: &repository.RelationshipCorrectionPredicatePatch{Key: "works_on"}},
		Supports: []repository.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}},
		Reason:   "incorrect predicate", IdempotencyKey: "correction-1",
	}
	_, err := NewLifecycleService(LifecycleDependencies{}).CorrectRelationship(ctx, req)
	require.ErrorContains(t, err, "semantic repository is required")
	_, err = NewLifecycleService(LifecycleDependencies{Semantic: &lifecycleSemanticStub{}}).CorrectRelationship(context.Background(), req)
	require.ErrorIs(t, err, ErrLifecycleAuthContext)
}

func TestLifecycleRetractEvidenceUsesAuthenticatedOwner(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	evidenceID := uuid.NewString()
	evidence := &lifecycleEvidenceStub{result: &repository.EvidenceLifecycleResult{
		DecisionID: "decision-canonical", ProcessingState: "completed", RetractedEvidenceIDs: []string{evidenceID},
		AffectedRelationshipCount: 1, PendingRelationshipCount: 1,
	}}
	svc := NewLifecycleService(LifecycleDependencies{Evidence: evidence})
	result, err := svc.RetractEvidence(authenticatedRememberContext(teamID, profileID, uuid.New()), RetractEvidenceRequest{
		EvidenceIDs: []string{evidenceID}, Reason: "entered in error", IdempotencyKey: "retract-1",
	})
	require.NoError(t, err)
	require.Equal(t, "decision-canonical", result.DecisionID)
	require.Equal(t, teamID.String(), evidence.input.TeamID)
	require.Equal(t, profileID.String(), evidence.input.OwnerProfileID)
	require.NotEmpty(t, evidence.input.RequestHash)

	_, err = svc.RetractEvidence(context.Background(), RetractEvidenceRequest{
		EvidenceIDs: []string{evidenceID}, Reason: "entered in error", IdempotencyKey: "retract-1",
	})
	require.ErrorIs(t, err, ErrLifecycleAuthContext)
}

func TestRetractEvidenceRequestHashCanonicalizesEvidenceIDsAndKeepsContractMarker(t *testing.T) {
	firstEvidenceID := uuid.NewString()
	secondEvidenceID := uuid.NewString()
	first, err := retractEvidenceRequestHash(RetractEvidenceRequest{
		EvidenceIDs: []string{" " + firstEvidenceID + " ", secondEvidenceID}, Reason: "entered in error", IdempotencyKey: "retract-canonical-hash",
	})
	require.NoError(t, err)
	second, err := retractEvidenceRequestHash(RetractEvidenceRequest{
		EvidenceIDs: []string{secondEvidenceID, firstEvidenceID}, Reason: "entered in error", IdempotencyKey: "retract-canonical-hash",
	})
	require.NoError(t, err)
	require.Equal(t, first, second)

	hash, err := retractEvidenceRequestHash(RetractEvidenceRequest{
		EvidenceIDs: []string{"b", "a"}, Reason: "entered in error", IdempotencyKey: "retract-compat",
	})
	require.NoError(t, err)
	require.Equal(t, "sha256:72fbf75d4468d6232c78c592ea5331bd639dbe29aefb2926cf4f8776ce098ceb", hash)
}

func TestLifecycleRetractEvidenceMapsRepositoryErrors(t *testing.T) {
	ctx := authenticatedRememberContext(uuid.New(), uuid.New(), uuid.New())
	req := RetractEvidenceRequest{EvidenceIDs: []string{uuid.NewString()}, Reason: "entered in error", IdempotencyKey: "retract-errors-1"}
	_, err := NewLifecycleService(LifecycleDependencies{}).RetractEvidence(ctx, req)
	require.ErrorContains(t, err, "evidence repository is required")

	for _, test := range []struct {
		err  error
		code httperr.ErrorCode
	}{
		{repository.ErrEvidenceLifecycleNotFound, httperr.NOT_FOUND},
		{repository.ErrTeamInactive, httperr.NOT_FOUND},
		{repository.ErrEvidenceLifecycleConflict, httperr.CONFLICT},
		{repository.ErrIdempotencyConflict, httperr.CONFLICT},
	} {
		_, err := NewLifecycleService(LifecycleDependencies{Evidence: &lifecycleEvidenceStub{err: test.err}}).RetractEvidence(ctx, req)
		var apiErr *httperr.APIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, test.code, apiErr.Code)
	}
}

type lifecycleSemanticStub struct {
	correctInput  repository.CorrectRelationshipInput
	statusInput   repository.GetRelationshipCorrectionInput
	correctResult *repository.CorrectRelationshipResult
	statusResult  *repository.RelationshipCorrectionStatus
	err           error
	plan          *repository.RelationshipCorrectionEmbeddingPlan
	embeddings    []repository.RelationshipCorrectionEmbedding
	commitCalls   int
}

func (s *lifecycleSemanticStub) CorrectRelationship(_ context.Context, input repository.CorrectRelationshipInput) (*repository.CorrectRelationshipResult, error) {
	s.correctInput = input
	if s.err != nil {
		return nil, s.err
	}
	if s.correctResult == nil {
		return nil, errors.New("missing correct result")
	}
	return s.correctResult, nil
}

func (s *lifecycleSemanticStub) PlanRelationshipCorrectionEmbeddings(_ context.Context, input repository.CorrectRelationshipInput) (*repository.RelationshipCorrectionEmbeddingPlan, error) {
	s.correctInput = input
	if s.err != nil {
		return nil, s.err
	}
	if s.plan != nil {
		return s.plan, nil
	}
	return &repository.RelationshipCorrectionEmbeddingPlan{}, nil
}

func (s *lifecycleSemanticStub) CorrectRelationshipWithEmbeddings(ctx context.Context, input repository.CorrectRelationshipInput, embeddings []repository.RelationshipCorrectionEmbedding) (*repository.CorrectRelationshipResult, error) {
	s.commitCalls++
	s.embeddings = append([]repository.RelationshipCorrectionEmbedding(nil), embeddings...)
	return s.CorrectRelationship(ctx, input)
}

func (s *lifecycleSemanticStub) GetRelationshipCorrection(_ context.Context, input repository.GetRelationshipCorrectionInput) (*repository.RelationshipCorrectionStatus, error) {
	s.statusInput = input
	if s.err != nil {
		return nil, s.err
	}
	if s.statusResult == nil {
		return nil, repository.ErrRelationshipCorrectionNotFound
	}
	return s.statusResult, nil
}

type lifecycleEvidenceStub struct {
	input  repository.RetractEvidenceInput
	result *repository.EvidenceLifecycleResult
	err    error
}

type lifecycleExecutorStub struct {
	result         semanticwrite.Result
	err            error
	calls          int
	waitForContext bool
}

func (s *lifecycleExecutorStub) Execute(ctx context.Context, _ semanticwrite.Plan) (semanticwrite.Result, error) {
	s.calls++
	if s.waitForContext {
		<-ctx.Done()
		return semanticwrite.Result{}, ctx.Err()
	}
	return s.result, s.err
}

func (s *lifecycleEvidenceStub) RetractEvidence(_ context.Context, input repository.RetractEvidenceInput) (*repository.EvidenceLifecycleResult, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	if s.result == nil {
		return nil, errors.New("missing evidence lifecycle result")
	}
	return s.result, nil
}
