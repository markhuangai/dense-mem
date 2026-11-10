package lifecycle

import (
	"context"
	"errors"

	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"

	"github.com/markhuangai/dense-mem/internal/httperr"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func authenticatedRememberContext(teamID, profileID, credentialID uuid.UUID) context.Context {
	ctx := correlation.WithID(context.Background(), "corr-canonical")
	return requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, TeamName: "team", IdentityID: credentialID, MembershipID: credentialID,
		OwnerID: profileID, OwnerName: "owner", CredentialID: &credentialID,
		AuthMethod: "api_key", Role: "member", Grants: []string{"read", "write"},
	})
}

func TestLifecycleRetractEvidenceUsesAuthenticatedOwner(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	evidenceID := uuid.NewString()
	evidence := &lifecycleEvidenceStub{result: &knowledgecontract.EvidenceLifecycleResult{
		DecisionID: "decision-canonical", ProcessingState: "completed", RetractedEvidenceIDs: []string{evidenceID},
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

type lifecycleEvidenceStub struct {
	input  knowledgecontract.RetractEvidenceInput
	result *knowledgecontract.EvidenceLifecycleResult
	err    error
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
