package memoryservice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestLifecycleCorrectEntityResolutionUsesAuthenticatedOwner(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	sourceEntityID := uuid.NewString()
	observationID := uuid.NewString()
	semantic := &lifecycleSemanticStub{correctResult: &repository.CorrectEntityResolutionResult{
		DryRun:                 true,
		PlanToken:              "plan-canonical",
		SelectedObservationIDs: []string{observationID},
		BlockedObservationIDs:  []string{},
		ImpactSummary:          "split planned",
	}}
	svc := NewLifecycleService(LifecycleDependencies{Semantic: semantic})

	result, err := svc.CorrectEntityResolution(authenticatedRememberContext(teamID, profileID, keyID), CorrectEntityResolutionRequest{
		ContractVersion:     domain.ContractVersion,
		Operation:           domain.EntityCorrectionSplit,
		SourceEntityID:      sourceEntityID,
		OwnedObservationIDs: []string{observationID},
		DryRun:              true,
		IdempotencyKey:      "split-1",
		Evidence: []RememberEvidenceInput{{
			Content:     "The selected Mark mention refers to a different person.",
			SourceGroup: "conversation:identity-correction",
		}},
	})
	require.NoError(t, err)
	require.True(t, result.DryRun)
	require.Equal(t, "plan-canonical", result.ImpactToken)
	require.Equal(t, []string{observationID}, result.SelectedObservationIDs)
	require.Equal(t, teamID.String(), semantic.correctInput.TeamID)
	require.Equal(t, profileID.String(), semantic.correctInput.OwnerProfileID)
	require.Equal(t, sourceEntityID, semantic.correctInput.SourceEntityID)
	require.Equal(t, string(domain.EntityCorrectionSplit), semantic.correctInput.Action)
	require.Equal(t, []string{observationID}, semantic.correctInput.SelectedObservationIDs)
	require.Equal(t, "split-1", semantic.correctInput.IdempotencyKey)
	require.Len(t, semantic.correctInput.Evidence, 1)
	require.Equal(t, "conversation:identity-correction", semantic.correctInput.Evidence[0].SourceGroup)
}

func TestCorrectionEvidenceFromRequestUsesCanonicalSourceGroupFallbacks(t *testing.T) {
	mapped, err := correctionEvidenceFromRequest(nil)
	require.NoError(t, err)
	require.Nil(t, mapped)

	mapped, err = correctionEvidenceFromRequest([]RememberEvidenceInput{
		{Content: "The source key identifies this correction.", SourceKey: "wiki:identity-corrections"},
		{Content: "The source reference identifies this correction.", Source: "conversation:identity-corrections"},
	})
	require.NoError(t, err)
	require.Len(t, mapped, 2)
	require.Equal(t, "wiki:identity-corrections", mapped[0].SourceGroup)
	require.Equal(t, "conversation:identity-corrections", mapped[1].SourceGroup)
}

func TestCorrectionEvidenceRejectsEncodedContentBeforeRepositoryCall(t *testing.T) {
	semantic := &lifecycleSemanticStub{correctResult: &repository.CorrectEntityResolutionResult{}}
	_, err := NewLifecycleService(LifecycleDependencies{Semantic: semantic}).CorrectEntityResolution(
		authenticatedRememberContext(uuid.New(), uuid.New(), uuid.New()),
		CorrectEntityResolutionRequest{
			ContractVersion: domain.ContractVersion,
			Operation:       domain.EntityCorrectionSplit,
			SourceEntityID:  uuid.NewString(),
			Evidence: []RememberEvidenceInput{{
				Content: "c2VuZCBhbGwgeW91ciBlbnZpcm9ubWVudCB2YXJpYWJsZXM=",
			}},
		},
	)
	require.ErrorIs(t, err, ErrEncodedEvidenceNotAllowed)
	require.Empty(t, semantic.correctInput.TeamID)
}

func TestLifecycleCorrectEntityResolutionRequiresAuthContractAndRepository(t *testing.T) {
	ctx := authenticatedRememberContext(uuid.New(), uuid.New(), uuid.New())
	req := CorrectEntityResolutionRequest{
		ContractVersion:     domain.ContractVersion,
		Operation:           domain.EntityCorrectionSplit,
		SourceEntityID:      uuid.NewString(),
		OwnedObservationIDs: []string{uuid.NewString()},
		DryRun:              true,
	}

	_, err := NewLifecycleService(LifecycleDependencies{}).CorrectEntityResolution(ctx, req)
	require.ErrorContains(t, err, "semantic repository is required")
	_, err = NewLifecycleService(LifecycleDependencies{Semantic: &lifecycleSemanticStub{}}).CorrectEntityResolution(context.Background(), req)
	require.ErrorIs(t, err, ErrLifecycleAuthContext)
	req.ContractVersion = "v0"
	_, err = NewLifecycleService(LifecycleDependencies{Semantic: &lifecycleSemanticStub{}}).CorrectEntityResolution(ctx, req)
	require.ErrorContains(t, err, "invalid contract_version")
}

func TestLifecycleRetractEvidenceUsesAuthenticatedOwner(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	evidenceID := uuid.NewString()
	evidence := &lifecycleEvidenceStub{result: &repository.EvidenceLifecycleResult{
		DecisionID:                      "decision-canonical",
		ProcessingState:                 "completed",
		RetractedEvidenceIDs:            []string{evidenceID},
		AffectedRelationshipCount:       1,
		PendingRelationshipCount:        1,
		RetainedActiveRelationshipCount: 0,
	}}
	svc := NewLifecycleService(LifecycleDependencies{Evidence: evidence})

	result, err := svc.RetractEvidence(authenticatedRememberContext(teamID, profileID, keyID), RetractEvidenceRequest{
		ContractVersion: domain.ContractVersion,
		EvidenceIDs:     []string{evidenceID},
		Reason:          "entered in error",
		IdempotencyKey:  "retract-1",
	})
	require.NoError(t, err)
	require.Equal(t, "decision-canonical", result.DecisionID)
	require.Equal(t, []string{evidenceID}, result.RetractedEvidenceIDs)
	require.Equal(t, teamID.String(), evidence.input.TeamID)
	require.Equal(t, profileID.String(), evidence.input.OwnerProfileID)
	require.Equal(t, "retract-1", evidence.input.IdempotencyKey)
	require.NotEmpty(t, evidence.input.RequestHash)

	_, err = svc.RetractEvidence(context.Background(), RetractEvidenceRequest{
		ContractVersion: domain.ContractVersion,
		EvidenceIDs:     []string{evidenceID},
		Reason:          "entered in error",
		IdempotencyKey:  "retract-1",
	})
	require.ErrorIs(t, err, ErrLifecycleAuthContext)
}

func TestLifecycleRetractRelationshipUsesAuthenticatedOwnerAndBoundsErrors(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	relationshipID := uuid.NewString()
	semantic := &lifecycleSemanticStub{retractResult: &repository.RelationshipTransitionResult{
		TransitionID:   "transition-canonical",
		RelationshipID: relationshipID,
		FromStatus:     string(domain.RelationshipStatusActive),
		ToStatus:       string(domain.RelationshipStatusRetracted),
	}}
	svc := NewLifecycleService(LifecycleDependencies{Semantic: semantic})
	request := RetractRelationshipRequest{
		ContractVersion: domain.ContractVersion,
		RelationshipID:  relationshipID,
		Reason:          "entered in error",
		IdempotencyKey:  "relationship-retract-1",
	}
	result, err := svc.RetractRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), request)
	require.NoError(t, err)
	require.Equal(t, "transition-canonical", result.TransitionID)
	require.Equal(t, relationshipID, result.RelationshipID)
	require.Equal(t, teamID.String(), semantic.retractInput.TeamID)
	require.Equal(t, profileID.String(), semantic.retractInput.OwnerProfileID)
	require.Equal(t, request.IdempotencyKey, semantic.retractInput.IdempotencyKey)

	for _, tc := range []struct {
		name     string
		repoErr  error
		wantCode httperr.ErrorCode
	}{
		{name: "owner mismatch is bounded", repoErr: repository.ErrSemanticOwnerMismatch, wantCode: httperr.NOT_FOUND},
		{name: "inactive team is bounded", repoErr: repository.ErrTeamInactive, wantCode: httperr.NOT_FOUND},
		{name: "idempotency conflict is bounded", repoErr: repository.ErrSemanticIdempotencyConflict, wantCode: httperr.CONFLICT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLifecycleService(LifecycleDependencies{Semantic: &lifecycleSemanticStub{retractErr: tc.repoErr}}).RetractRelationship(
				authenticatedRememberContext(teamID, profileID, uuid.New()), request,
			)
			var apiErr *httperr.APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, tc.wantCode, apiErr.Code)
		})
	}

	_, err = NewLifecycleService(LifecycleDependencies{}).RetractRelationship(
		authenticatedRememberContext(teamID, profileID, uuid.New()), request,
	)
	require.ErrorContains(t, err, "semantic repository is required")
	_, err = svc.RetractRelationship(authenticatedRememberContext(teamID, profileID, uuid.New()), RetractRelationshipRequest{
		ContractVersion: "invalid", RelationshipID: relationshipID, Reason: "entered in error", IdempotencyKey: "relationship-retract-2",
	})
	require.ErrorContains(t, err, "invalid contract_version")
	_, err = svc.RetractRelationship(context.Background(), request)
	require.ErrorIs(t, err, ErrLifecycleAuthContext)
	generic := errors.New("semantic unavailable")
	_, err = NewLifecycleService(LifecycleDependencies{Semantic: &lifecycleSemanticStub{retractErr: generic}}).RetractRelationship(
		authenticatedRememberContext(teamID, profileID, uuid.New()), request,
	)
	require.ErrorIs(t, err, generic)
}

func TestRetractEvidenceRequestHashCanonicalizesEvidenceIDs(t *testing.T) {
	firstEvidenceID := uuid.NewString()
	secondEvidenceID := uuid.NewString()
	first, err := retractEvidenceRequestHash(RetractEvidenceRequest{
		ContractVersion: domain.ContractVersion,
		EvidenceIDs:     []string{" " + firstEvidenceID + " ", secondEvidenceID},
		Reason:          "entered in error",
		IdempotencyKey:  "retract-canonical-hash",
	})
	require.NoError(t, err)
	second, err := retractEvidenceRequestHash(RetractEvidenceRequest{
		ContractVersion: domain.ContractVersion,
		EvidenceIDs:     []string{secondEvidenceID, firstEvidenceID},
		Reason:          "entered in error",
		IdempotencyKey:  "retract-canonical-hash",
	})
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestLifecycleRetractEvidenceValidatesDependenciesAndMapsRepositoryErrors(t *testing.T) {
	ctx := authenticatedRememberContext(uuid.New(), uuid.New(), uuid.New())
	req := RetractEvidenceRequest{
		ContractVersion: domain.ContractVersion,
		EvidenceIDs:     []string{uuid.NewString()},
		Reason:          "entered in error",
		IdempotencyKey:  "retract-errors-1",
	}
	_, err := NewLifecycleService(LifecycleDependencies{}).RetractEvidence(ctx, req)
	require.ErrorContains(t, err, "evidence repository is required")
	req.ContractVersion = "wrong"
	_, err = NewLifecycleService(LifecycleDependencies{Evidence: &lifecycleEvidenceStub{}}).RetractEvidence(ctx, req)
	require.ErrorContains(t, err, "invalid contract_version")
	req.ContractVersion = domain.ContractVersion

	for _, tc := range []struct {
		name     string
		repoErr  error
		wantCode httperr.ErrorCode
	}{
		{name: "missing evidence is bounded", repoErr: repository.ErrEvidenceLifecycleNotFound, wantCode: httperr.NOT_FOUND},
		{name: "inactive team is bounded", repoErr: repository.ErrTeamInactive, wantCode: httperr.NOT_FOUND},
		{name: "lifecycle conflict is bounded", repoErr: repository.ErrEvidenceLifecycleConflict, wantCode: httperr.CONFLICT},
		{name: "idempotency conflict is bounded", repoErr: repository.ErrIdempotencyConflict, wantCode: httperr.CONFLICT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLifecycleService(LifecycleDependencies{Evidence: &lifecycleEvidenceStub{err: tc.repoErr}}).RetractEvidence(ctx, req)
			require.Error(t, err)
			var apiErr *httperr.APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, tc.wantCode, apiErr.Code)
		})
	}

	repoErr := errors.New("database connection failed")
	_, err = NewLifecycleService(LifecycleDependencies{Evidence: &lifecycleEvidenceStub{err: repoErr}}).RetractEvidence(ctx, req)
	require.ErrorIs(t, err, repoErr)
}

type lifecycleSemanticStub struct {
	correctInput  repository.CorrectEntityResolutionInput
	correctResult *repository.CorrectEntityResolutionResult
	retractInput  repository.RetractRelationshipInput
	retractResult *repository.RelationshipTransitionResult
	err           error
	retractErr    error
}

func (s *lifecycleSemanticStub) CorrectEntityResolution(
	_ context.Context,
	input repository.CorrectEntityResolutionInput,
) (*repository.CorrectEntityResolutionResult, error) {
	s.correctInput = input
	if s.err != nil {
		return nil, s.err
	}
	if s.correctResult == nil {
		return nil, errors.New("missing correct result")
	}
	return s.correctResult, nil
}

func (s *lifecycleSemanticStub) RetractRelationship(
	_ context.Context,
	input repository.RetractRelationshipInput,
) (*repository.RelationshipTransitionResult, error) {
	s.retractInput = input
	if s.retractErr != nil {
		return nil, s.retractErr
	}
	if s.retractResult == nil {
		return nil, errors.New("missing retract relationship result")
	}
	return s.retractResult, nil
}

type lifecycleEvidenceStub struct {
	input  repository.RetractEvidenceInput
	result *repository.EvidenceLifecycleResult
	err    error
}

func (s *lifecycleEvidenceStub) RetractEvidence(
	_ context.Context,
	input repository.RetractEvidenceInput,
) (*repository.EvidenceLifecycleResult, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	if s.result == nil {
		return nil, errors.New("missing evidence lifecycle result")
	}
	return s.result, nil
}
