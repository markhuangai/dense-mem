package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	semanticwritecontract "github.com/markhuangai/dense-mem/internal/semanticwrite/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	semanticwriteapp "github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func authenticatedRememberContext(teamID, profileID, credentialID uuid.UUID) context.Context {
	ctx := correlation.WithID(context.Background(), "corr-canonical")
	return requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, TeamName: "team", IdentityID: credentialID, MembershipID: credentialID,
		OwnerID: profileID, OwnerName: "owner", CredentialID: &credentialID,
		AuthMethod: "api_key", Role: "member", Grants: []string{"read", "write"},
	})
}

func TestLifecycleCorrectRelationshipUsesAuthenticatedOwner(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	relationshipID := uuid.NewString()
	evidenceID := uuid.NewString()
	semantic := &lifecycleSemanticStub{correctResult: &knowledgecontract.CorrectRelationshipResult{
		SubmissionID: uuid.NewString(), ProcessingState: "completed",
	}}
	svc := NewLifecycleService(LifecycleDependencies{Port: semantic})

	result, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{
		Action: "submit", RelationshipID: relationshipID, ExpectedVersion: 3,
		Patch:    knowledgecontract.RelationshipCorrectionPatch{Predicate: &knowledgecontract.RelationshipCorrectionPredicatePatch{Key: "works_with"}},
		Supports: []knowledgecontract.RelationshipCorrectionSupport{{EvidenceID: evidenceID, Start: 0, End: 8}},
		Reason:   "predicate was resolved incorrectly", IdempotencyKey: "relationship-correction-1",
	})
	require.NoError(t, err)
	require.Equal(t, semantic.correctResult.SubmissionID, result.SubmissionID)
	require.Equal(t, "relationship_correction", result.SubmissionKind)
	require.Equal(t, "completed", result.ProcessingState)
	require.Equal(t, string(domain.SearchProjectionNotRequired), result.SearchState)
	require.Equal(t, teamID.String(), semantic.correctInput.TeamID)
	require.Equal(t, profileID.String(), semantic.correctInput.OwnerProfileID)
	require.Equal(t, relationshipID, semantic.correctInput.RelationshipID)
	require.Equal(t, 3, semantic.correctInput.ExpectedVersion)
}

func TestLifecycleCorrectRelationshipNormalizesOversizedCorrelationID(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{correctResult: &knowledgecontract.CorrectRelationshipResult{
		SubmissionID: uuid.NewString(), ProcessingState: "completed",
	}}
	ctx := correlation.WithID(authenticatedRememberContext(teamID, profileID, uuid.New()), strings.Repeat("x", 129))

	result, err := NewLifecycleService(LifecycleDependencies{Port: semantic}).CorrectRelationship(ctx, CorrectRelationshipRequest{
		Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1,
		Patch:    knowledgecontract.RelationshipCorrectionPatch{Predicate: &knowledgecontract.RelationshipCorrectionPredicatePatch{Key: "works_with"}},
		Supports: []knowledgecontract.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 8}},
		Reason:   "predicate was resolved incorrectly", IdempotencyKey: "oversized-correlation",
	})

	require.NoError(t, err)
	require.NotEqual(t, strings.Repeat("x", 129), result.CorrelationID)
	require.LessOrEqual(t, len([]rune(result.CorrelationID)), 128)
	_, parseErr := uuid.Parse(result.CorrelationID)
	require.NoError(t, parseErr)
}

func TestLifecycleCorrectRelationshipExecutesOnePlannedBatchBeforeCommit(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{
		plan: &knowledgecontract.RelationshipCorrectionEmbeddingPlan{
			Documents:           []knowledgecontract.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
			EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
		},
		correctResult: &knowledgecontract.CorrectRelationshipResult{SubmissionID: uuid.NewString(), ProcessingState: "completed"},
	}
	executor := &lifecycleExecutorStub{result: semanticwritecontract.Result{Fence: semanticwritecontract.Fence{Model: semantic.plan.EmbeddingModel, Dimensions: 2, EmbeddingContractID: semantic.plan.EmbeddingContractID, SearchGenerationID: semantic.plan.SearchIndexGenerationID, SearchGenerationVersion: 1}, Embeddings: []semanticwritecontract.Embedding{{DocumentHash: "hash", Vector: []float32{1, 2}}}}}
	svc := NewLifecycleService(LifecycleDependencies{Port: semantic, CorrectionExecutor: executor})
	_, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: knowledgecontract.RelationshipCorrectionPatch{Predicate: &knowledgecontract.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []knowledgecontract.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "planned-correction"})
	require.NoError(t, err)
	require.Equal(t, 1, executor.calls)
	require.Len(t, semantic.embeddings, 1)
}

func TestLifecycleCorrectRelationshipDoesNotCommitWhenEmbeddingFails(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{plan: &knowledgecontract.RelationshipCorrectionEmbeddingPlan{
		Documents:           []knowledgecontract.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
		EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
	}, correctResult: &knowledgecontract.CorrectRelationshipResult{SubmissionID: uuid.NewString(), ProcessingState: "completed"}}
	svc := NewLifecycleService(LifecycleDependencies{Port: semantic, CorrectionExecutor: &lifecycleExecutorStub{err: semanticwriteapp.ErrProviderUnavailable}})
	_, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: knowledgecontract.RelationshipCorrectionPatch{Predicate: &knowledgecontract.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []knowledgecontract.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "failed-planned-correction"})
	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.ErrEmbeddingUnavailable, publicErr.Code)
	require.Zero(t, semantic.commitCalls)
}

func TestLifecycleCorrectRelationshipDoesNotCommitWhenEmbeddingTimesOut(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{plan: &knowledgecontract.RelationshipCorrectionEmbeddingPlan{
		Documents:           []knowledgecontract.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
		EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
	}, correctResult: &knowledgecontract.CorrectRelationshipResult{SubmissionID: uuid.NewString(), ProcessingState: "completed"}}
	svc := NewLifecycleService(LifecycleDependencies{Port: semantic, CorrectionExecutor: &lifecycleExecutorStub{err: semanticwriteapp.ErrProviderTimeout}})

	_, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: knowledgecontract.RelationshipCorrectionPatch{Predicate: &knowledgecontract.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []knowledgecontract.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "timed-out-planned-correction"})

	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.ErrEmbeddingTimeout, publicErr.Code)
	require.Zero(t, semantic.commitCalls)
}

func TestLifecycleCorrectRelationshipPreservesCommitFenceClassification(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	for _, cause := range []error{knowledgecontract.ErrSearchEmbeddingRequired, knowledgecontract.ErrSearchContractMismatch, knowledgecontract.ErrSearchStaleVersion} {
		t.Run(cause.Error(), func(t *testing.T) {
			semantic := &lifecycleSemanticStub{plan: &knowledgecontract.RelationshipCorrectionEmbeddingPlan{
				Documents:           []knowledgecontract.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
				EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
			}, correctResult: &knowledgecontract.CorrectRelationshipResult{SubmissionID: uuid.NewString(), ProcessingState: "completed"}, commitErr: cause}
			executor := &lifecycleExecutorStub{result: semanticwritecontract.Result{Fence: semanticwritecontract.Fence{Model: semantic.plan.EmbeddingModel, Dimensions: 2, EmbeddingContractID: semantic.plan.EmbeddingContractID, SearchGenerationID: semantic.plan.SearchIndexGenerationID, SearchGenerationVersion: 1}, Embeddings: []semanticwritecontract.Embedding{{DocumentHash: "hash", Vector: []float32{1, 2}}}}}
			svc := NewLifecycleService(LifecycleDependencies{Port: semantic, CorrectionExecutor: executor})

			_, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: knowledgecontract.RelationshipCorrectionPatch{Predicate: &knowledgecontract.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []knowledgecontract.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "commit-fence-correction"})
			var publicErr *httperr.APIError
			require.ErrorAs(t, err, &publicErr)
			require.Equal(t, httperr.CONFLICT, publicErr.Code)
			require.Contains(t, publicErr.Details, httperr.ErrorDetail{Field: "reason", Message: string(rememberapp.TerminalErrorCommitConflict)})
		})
	}
}

func TestLifecycleCorrectRelationshipClassifiesConfiguredEmbeddingDeadline(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{plan: &knowledgecontract.RelationshipCorrectionEmbeddingPlan{
		Documents:           []knowledgecontract.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
		EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
	}}
	svc := NewLifecycleService(LifecycleDependencies{Port: semantic, CorrectionExecutor: &lifecycleExecutorStub{waitForContext: true}, CorrectionEmbeddingTimeout: 10 * time.Millisecond})

	_, err := svc.CorrectRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: knowledgecontract.RelationshipCorrectionPatch{Predicate: &knowledgecontract.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []knowledgecontract.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "configured-timeout-correction"})

	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.ErrEmbeddingTimeout, publicErr.Code)
	require.Zero(t, semantic.commitCalls)
}

func TestLifecycleCorrectRelationshipPreservesCallerDeadline(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	semantic := &lifecycleSemanticStub{plan: &knowledgecontract.RelationshipCorrectionEmbeddingPlan{
		Documents:           []knowledgecontract.RelationshipCorrectionEmbeddingDocument{{DocumentHash: "hash", DocumentText: "relationship"}},
		EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: uuid.NewString(), IndexGeneration: 1,
	}}
	executor := &lifecycleExecutorStub{waitForContext: true}
	svc := NewLifecycleService(LifecycleDependencies{Port: semantic, CorrectionExecutor: executor, CorrectionEmbeddingTimeout: time.Second})
	callerCtx, cancel := context.WithTimeout(authenticatedRememberContext(teamID, profileID, uuid.New()), 10*time.Millisecond)
	defer cancel()

	_, err := svc.CorrectRelationship(callerCtx, CorrectRelationshipRequest{Action: "submit", RelationshipID: uuid.NewString(), ExpectedVersion: 1, Patch: knowledgecontract.RelationshipCorrectionPatch{Predicate: &knowledgecontract.RelationshipCorrectionPredicatePatch{Key: "works_on"}}, Supports: []knowledgecontract.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}}, Reason: "incorrect predicate", IdempotencyKey: "caller-deadline-correction"})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	var publicErr *httperr.APIError
	require.NotErrorAs(t, err, &publicErr)
	require.Zero(t, semantic.commitCalls)
}

func TestLifecycleRelationshipCorrectionErrorsAreBounded(t *testing.T) {
	ctx := authenticatedRememberContext(uuid.New(), uuid.New(), uuid.New())
	repositoryFailure := errors.New("database host and query details")
	svc := NewLifecycleService(LifecycleDependencies{Port: &lifecycleSemanticStub{err: repositoryFailure}})
	_, err := svc.CorrectRelationship(ctx, CorrectRelationshipRequest{Action: "submit"})
	require.ErrorIs(t, err, ErrLifecyclePersistence)
	require.NotContains(t, err.Error(), repositoryFailure.Error())

	unsafeAction := strings.Repeat("client-controlled-", 100)
	_, err = svc.CorrectRelationship(ctx, CorrectRelationshipRequest{Action: unsafeAction})
	require.ErrorContains(t, err, "action must be submit or confirm")
	require.NotContains(t, err.Error(), unsafeAction)
}

func TestTranslateRelationshipCorrectionErrorMapsSearchFencesToConflict(t *testing.T) {
	for _, cause := range []error{knowledgecontract.ErrSearchEmbeddingRequired, knowledgecontract.ErrSearchContractMismatch, knowledgecontract.ErrSearchStaleVersion} {
		err := translateRelationshipCorrectionError(cause)
		var publicErr *httperr.APIError
		require.ErrorAs(t, err, &publicErr)
		require.Equal(t, httperr.CONFLICT, publicErr.Code)
	}
}

func TestTranslateRelationshipCorrectionIdempotencyConflictPreservesTerminalClassification(t *testing.T) {
	err := translateRelationshipCorrectionError(knowledgecontract.ErrSemanticIdempotencyConflict)
	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.CONFLICT, publicErr.Code)
	require.Contains(t, publicErr.Details, httperr.ErrorDetail{Field: "reason", Message: "idempotency_conflict"})
}

func TestTranslateRelationshipCorrectionConfirmationPreservesTypedConflict(t *testing.T) {
	err := translateRelationshipCorrectionError(knowledgecontract.ErrRelationshipCorrectionConfirmation)
	var publicErr *httperr.APIError
	require.ErrorAs(t, err, &publicErr)
	require.Equal(t, httperr.CONFLICT, publicErr.Code)
	require.Contains(t, publicErr.Details, httperr.ErrorDetail{Field: "reason", Message: CorrectionConfirmationInvalidReason})
}

func TestTranslateRelationshipCorrectionConfirmationExpiryPreservesTerminalClassification(t *testing.T) {
	err := translateRelationshipCorrectionError(knowledgecontract.ErrRelationshipCorrectionConfirmationExpired)
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
		Patch:    knowledgecontract.RelationshipCorrectionPatch{Predicate: &knowledgecontract.RelationshipCorrectionPredicatePatch{Key: "works_on"}},
		Supports: []knowledgecontract.RelationshipCorrectionSupport{{EvidenceID: uuid.NewString(), Start: 0, End: 1}},
		Reason:   "incorrect predicate", IdempotencyKey: "correction-1",
	}
	_, err := NewLifecycleService(LifecycleDependencies{}).CorrectRelationship(ctx, req)
	require.ErrorContains(t, err, "semantic repository is required")
	_, err = NewLifecycleService(LifecycleDependencies{Port: &lifecycleSemanticStub{}}).CorrectRelationship(context.Background(), req)
	require.ErrorIs(t, err, ErrLifecycleAuthContext)
}

func TestLifecycleRetractEvidenceUsesAuthenticatedOwner(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	evidenceID := uuid.NewString()
	evidence := &lifecycleEvidenceStub{result: &knowledgecontract.EvidenceLifecycleResult{
		DecisionID: "decision-canonical", ProcessingState: "completed", RetractedEvidenceIDs: []string{evidenceID},
		AffectedRelationshipCount: 1, PendingRelationshipCount: 1,
	}}
	svc := NewLifecycleService(LifecycleDependencies{Port: evidence})
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
		{knowledgecontract.ErrEvidenceLifecycleNotFound, httperr.NOT_FOUND},
		{knowledgecontract.ErrTeamInactive, httperr.NOT_FOUND},
		{knowledgecontract.ErrEvidenceLifecycleConflict, httperr.CONFLICT},
		{knowledgecontract.ErrIdempotencyConflict, httperr.CONFLICT},
	} {
		_, err := NewLifecycleService(LifecycleDependencies{Port: &lifecycleEvidenceStub{err: test.err}}).RetractEvidence(ctx, req)
		var apiErr *httperr.APIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, test.code, apiErr.Code)
	}
}

type lifecycleSemanticStub struct {
	correctInput  knowledgecontract.CorrectRelationshipInput
	correctResult *knowledgecontract.CorrectRelationshipResult
	err           error
	plan          *knowledgecontract.RelationshipCorrectionEmbeddingPlan
	embeddings    []knowledgecontract.RelationshipCorrectionEmbedding
	commitErr     error
	commitCalls   int
}

func (s *lifecycleSemanticStub) CorrectRelationship(_ context.Context, input knowledgecontract.CorrectRelationshipInput) (*knowledgecontract.CorrectRelationshipResult, error) {
	s.correctInput = input
	if s.err != nil {
		return nil, s.err
	}
	if s.correctResult == nil {
		return nil, errors.New("missing correct result")
	}
	return s.correctResult, nil
}

func (s *lifecycleSemanticStub) PlanRelationshipCorrectionEmbeddings(_ context.Context, input knowledgecontract.CorrectRelationshipInput) (*knowledgecontract.RelationshipCorrectionEmbeddingPlan, error) {
	s.correctInput = input
	if s.err != nil {
		return nil, s.err
	}
	if s.plan != nil {
		return s.plan, nil
	}
	return &knowledgecontract.RelationshipCorrectionEmbeddingPlan{}, nil
}

func (s *lifecycleSemanticStub) RetractEvidence(_ context.Context, _ knowledgecontract.RetractEvidenceInput) (*knowledgecontract.EvidenceLifecycleResult, error) {
	return nil, errors.New("retraction not configured")
}

func (s *lifecycleSemanticStub) CorrectRelationshipWithEmbeddings(ctx context.Context, input knowledgecontract.CorrectRelationshipInput, embeddings []knowledgecontract.RelationshipCorrectionEmbedding) (*knowledgecontract.CorrectRelationshipResult, error) {
	s.commitCalls++
	s.embeddings = append([]knowledgecontract.RelationshipCorrectionEmbedding(nil), embeddings...)
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	return s.CorrectRelationship(ctx, input)
}

type lifecycleEvidenceStub struct {
	input  knowledgecontract.RetractEvidenceInput
	result *knowledgecontract.EvidenceLifecycleResult
	err    error
}

type lifecycleExecutorStub struct {
	result         semanticwritecontract.Result
	err            error
	calls          int
	waitForContext bool
}

func (s *lifecycleExecutorStub) Execute(ctx context.Context, _ semanticwritecontract.Plan) (semanticwritecontract.Result, error) {
	s.calls++
	if s.waitForContext {
		<-ctx.Done()
		return semanticwritecontract.Result{}, ctx.Err()
	}
	return s.result, s.err
}

func (s *lifecycleEvidenceStub) PlanRelationshipCorrectionEmbeddings(_ context.Context, _ knowledgecontract.CorrectRelationshipInput) (*knowledgecontract.RelationshipCorrectionEmbeddingPlan, error) {
	return nil, errors.New("correction not configured")
}

func (s *lifecycleEvidenceStub) CorrectRelationshipWithEmbeddings(_ context.Context, _ knowledgecontract.CorrectRelationshipInput, _ []knowledgecontract.RelationshipCorrectionEmbedding) (*knowledgecontract.CorrectRelationshipResult, error) {
	return nil, errors.New("correction not configured")
}

func (s *lifecycleEvidenceStub) RetractEvidence(_ context.Context, input knowledgecontract.RetractEvidenceInput) (*knowledgecontract.EvidenceLifecycleResult, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	if s.result == nil {
		return nil, errors.New("missing evidence lifecycle result")
	}
	return s.result, nil
}
