package topics

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

type communityTestTransport struct {
	bodies []string
	calls  int
}

func (p *communityTestTransport) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	response := p.bodies[p.calls]
	p.calls++
	return modelprovider.StructuredResult{Content: response}, nil
}

func TestCommunityPlacementAcceptsOnlySuppliedParent(t *testing.T) {
	parent := "1590f5eb-d4de-4794-8042-dfd4207b84f1"
	transport := &communityTestTransport{bodies: []string{
		`{"parent_id":"0feb8b44-b851-4535-9d7e-40d014743fee"}`,
		`{"parent_id":"` + parent + `"}`,
	}}
	service := CommunityAssessor{Transport: transport, Limits: assessor.SemanticAssessmentLimits{}}
	outcome, err := service.Assess(context.Background(), uuid.NewString(), contract.CommunityInput{
		Topic:      contract.CommunityCandidate{ID: uuid.NewString(), Version: 1, Name: "Rollback drills"},
		Candidates: []contract.CommunityCandidate{{ID: parent, Version: 1, Name: "Release readiness"}},
	}, "fixture")
	require.NoError(t, err)
	require.Equal(t, "placed", outcome.Status)
	require.Equal(t, parent, outcome.ParentID)
	require.Equal(t, 2, transport.calls)
}

func TestCommunityPlacementWithoutCandidatesIsStandalone(t *testing.T) {
	service := CommunityAssessor{}
	outcome, err := service.Assess(context.Background(), uuid.NewString(), contract.CommunityInput{
		Topic: contract.CommunityCandidate{ID: uuid.NewString(), Version: 1, Name: "Rollback drills"},
	}, "fixture")
	require.NoError(t, err)
	require.Equal(t, "standalone", outcome.Status)
	require.NotEmpty(t, outcome.InputHash)
}
