package memoryservice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
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
	require.Equal(t, string(domain.SearchProjectionNotRequired), result.SearchState)
	require.Equal(t, teamID.String(), semantic.correctInput.TeamID)
	require.Equal(t, profileID.String(), semantic.correctInput.OwnerProfileID)
	require.Equal(t, relationshipID, semantic.correctInput.RelationshipID)
	require.Equal(t, 3, semantic.correctInput.ExpectedVersion)
}

func TestLifecycleInlineRelationshipEmbeddingBatchValidatesProviderOutput(t *testing.T) {
	document := repository.SearchDocumentForEmbedding{
		SearchDocumentResult: repository.SearchDocumentResult{
			SearchDocumentID: uuid.NewString(), SourceVersion: 1, ProjectionFormat: 2,
			DocumentVersion: 1, EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 3,
			SpaceID: uuid.NewString(), SpaceGeneration: 1,
		},
		DocumentText: "relationship text",
	}
	provider := &lifecycleEmbeddingStub{available: true, model: "embed-model", vectors: [][]float32{{1, 2, 3}}}
	svc := &lifecycleService{embedder: provider}
	embeddings, err := svc.embedRelationshipDocumentBatch(context.Background(), []repository.SearchDocumentForEmbedding{document})
	require.NoError(t, err)
	require.Len(t, embeddings, 1)
	require.Equal(t, document.SearchDocumentID, embeddings[0].SearchDocumentID)
	require.Equal(t, []float32{1, 2, 3}, embeddings[0].Embedding)

	provider.available = false
	_, err = svc.embedRelationshipDocumentBatch(context.Background(), []repository.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, ErrLifecycleEmbeddingUnavailable)
	provider.available = true
	provider.vectors = [][]float32{{1, 2}}
	_, err = svc.embedRelationshipDocumentBatch(context.Background(), []repository.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, ErrLifecycleEmbeddingInvalid)
	provider.vectors = [][]float32{{1, 2, 3}}
	provider.err = context.DeadlineExceeded
	_, err = svc.embedRelationshipDocumentBatch(context.Background(), []repository.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, ErrLifecycleEmbeddingTimeout)
	provider.err = context.Canceled
	_, err = svc.embedRelationshipDocumentBatch(context.Background(), []repository.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, ErrLifecycleEmbeddingCancelled)
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

func TestLifecycleCancellationErrorRemainsTyped(t *testing.T) {
	require.ErrorIs(t, translateRelationshipCorrectionError(ErrLifecycleEmbeddingCancelled), ErrLifecycleEmbeddingCancelled)
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

type lifecycleEmbeddingStub struct {
	available bool
	model     string
	vectors   [][]float32
	err       error
}

func (s *lifecycleEmbeddingStub) Embed(context.Context, string) ([]float32, string, error) {
	return nil, s.model, nil
}

func (s *lifecycleEmbeddingStub) EmbedBatch(context.Context, []string) ([][]float32, string, error) {
	return s.vectors, s.model, s.err
}

func (s *lifecycleEmbeddingStub) ModelName() string { return s.model }

func (s *lifecycleEmbeddingStub) Dimensions() int { return 3 }

func (s *lifecycleEmbeddingStub) IsAvailable() bool { return s.available }

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
