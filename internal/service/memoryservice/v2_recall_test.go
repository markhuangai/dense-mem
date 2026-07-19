package memoryservice

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestV2RecallUsesAuthenticatedTeamAndVectorQuery(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	evidenceID := uuid.NewString()
	relationshipID := uuid.NewString()
	search := &v2RecallSearchStub{
		contract: &repository.V2ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &repository.V2RecallEvidenceResult{
			SearchState: string(domain.V2SearchProjectionCurrent),
			Results: []repository.V2RecallEvidenceHit{{
				EvidenceID:      evidenceID,
				RelationshipIDs: []string{relationshipID},
				Rank:            1,
				Score:           0.99,
				Context:         "Dense-Mem uses PostgreSQL for durable memory.",
			}},
		},
	}
	provider := &v2RecallProviderStub{
		available: true,
		model:     "test-model",
		dims:      3,
		vector:    []float32{1, 0, 0},
	}
	svc := NewV2RecallService(V2RecallDependencies{Search: search, Provider: provider})

	result, err := svc.RecallV2(authenticatedV2RememberContext(teamID, profileID, keyID), V2RecallRequest{
		ContractVersion: domain.V2ContractVersion,
		Query:           "PostgreSQL memory",
		Limit:           2,
	})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Nil(t, result.Degradation)
	require.Equal(t, evidenceID, result.Results[0].EvidenceID)
	require.Equal(t, []float32{1, 0, 0}, search.input.QueryEmbedding)
	require.Equal(t, teamID.String(), search.input.TeamID)
	require.Equal(t, "PostgreSQL memory", provider.query)
}

func TestV2RecallProviderFailureIsOptionalDegradation(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	search := &v2RecallSearchStub{
		contract: &repository.V2ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &repository.V2RecallEvidenceResult{
			SearchState: string(domain.V2SearchProjectionPending),
			Results:     []repository.V2RecallEvidenceHit{},
		},
	}
	provider := &v2RecallProviderStub{available: false}
	svc := NewV2RecallService(V2RecallDependencies{Search: search, Provider: provider})

	result, err := svc.RecallV2(authenticatedV2RememberContext(teamID, profileID, keyID), V2RecallRequest{
		ContractVersion: domain.V2ContractVersion,
		Query:           "PostgreSQL memory",
	})
	require.NoError(t, err)
	require.NotNil(t, result.Degradation)
	require.True(t, result.Degradation.Optional)
	require.Equal(t, string(domain.V2ErrorProviderUnavailable), result.Degradation.Code)
	require.Empty(t, search.input.QueryEmbedding)
	require.Equal(t, string(domain.V2SearchProjectionPending), result.SearchState)
}

func TestV2RecallProviderMalformedBranchesAreOptionalDegradation(t *testing.T) {
	tests := []struct {
		name     string
		provider *v2RecallProviderStub
		wantCode string
	}{
		{
			name:     "configured dimensions mismatch",
			provider: &v2RecallProviderStub{available: true, model: "test-model", dims: 4, vector: []float32{1, 0, 0}},
			wantCode: string(domain.V2ErrorProviderMalformed),
		},
		{
			name:     "configured model mismatch",
			provider: &v2RecallProviderStub{available: true, model: "other-model", dims: 3, vector: []float32{1, 0, 0}},
			wantCode: string(domain.V2ErrorProviderMalformed),
		},
		{
			name:     "embed failure",
			provider: &v2RecallProviderStub{available: true, model: "test-model", dims: 3, err: errors.New("provider failed")},
			wantCode: string(domain.V2ErrorProviderUnavailable),
		},
		{
			name:     "returned vector length mismatch",
			provider: &v2RecallProviderStub{available: true, model: "test-model", dims: 3, vector: []float32{1, 0}},
			wantCode: string(domain.V2ErrorProviderMalformed),
		},
		{
			name:     "returned non finite vector",
			provider: &v2RecallProviderStub{available: true, model: "test-model", dims: 3, vector: []float32{float32(math.NaN()), 0, 0}},
			wantCode: string(domain.V2ErrorProviderMalformed),
		},
		{
			name:     "returned model mismatch",
			provider: &v2RecallProviderStub{available: true, model: "configured-model", dims: 3, vector: []float32{1, 0, 0}, returnedModel: "other-model"},
			wantCode: string(domain.V2ErrorProviderMalformed),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			teamID := uuid.New()
			profileID := uuid.New()
			keyID := uuid.New()
			search := &v2RecallSearchStub{
				contract: &repository.V2ActiveSearchContract{
					EmbeddingContractID: uuid.NewString(),
					EmbeddingDimensions: 3,
					EmbeddingModel:      "test-model",
				},
				result: &repository.V2RecallEvidenceResult{
					SearchState: string(domain.V2SearchProjectionCurrent),
					Results:     []repository.V2RecallEvidenceHit{},
				},
			}
			if tt.name == "returned model mismatch" {
				search.contract.EmbeddingModel = "configured-model"
			}
			svc := NewV2RecallService(V2RecallDependencies{Search: search, Provider: tt.provider})

			result, err := svc.RecallV2(authenticatedV2RememberContext(teamID, profileID, keyID), V2RecallRequest{
				ContractVersion: domain.V2ContractVersion,
				Query:           "PostgreSQL memory",
			})
			require.NoError(t, err)
			require.NotNil(t, result.Degradation)
			require.True(t, result.Degradation.Optional)
			require.Equal(t, tt.wantCode, result.Degradation.Code)
			require.Empty(t, search.input.QueryEmbedding)
		})
	}
}

func TestV2RecallCommunityDegradationAndIDNormalization(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	entityID := uuid.NewString()
	evidenceID := uuid.NewString()
	relationshipID := uuid.NewString()
	search := &v2RecallSearchStub{
		contract: &repository.V2ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &repository.V2RecallEvidenceResult{
			SearchState: string(domain.V2SearchProjectionCurrent),
			Results:     []repository.V2RecallEvidenceHit{},
		},
	}
	svc := NewV2RecallService(V2RecallDependencies{Search: search})

	result, err := svc.RecallV2(authenticatedV2RememberContext(teamID, profileID, keyID), V2RecallRequest{
		ContractVersion:      domain.V2ContractVersion,
		Query:                " ",
		UseCommunities:       true,
		KnownEvidenceIDs:     []string{" " + evidenceID + " ", evidenceID},
		KnownRelationshipIDs: []string{relationshipID, relationshipID},
		ExpandFromEntityIDs:  []string{entityID, entityID},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Degradation)
	require.Equal(t, "community_recall_unavailable", result.Degradation.Code)
	require.Equal(t, []string{evidenceID}, search.input.KnownEvidenceIDs)
	require.Equal(t, []string{relationshipID}, search.input.KnownRelationshipIDs)
	require.Equal(t, []string{entityID}, search.input.ExpandFromEntityIDs)
	require.Empty(t, search.input.Query)
}

func TestV2RecallRequiresAuthenticatedActor(t *testing.T) {
	svc := NewV2RecallService(V2RecallDependencies{Search: &v2RecallSearchStub{}})
	_, err := svc.RecallV2(context.Background(), V2RecallRequest{
		ContractVersion: domain.V2ContractVersion,
		Query:           "PostgreSQL memory",
	})
	require.ErrorIs(t, err, ErrV2RecallAuthContext)
}

func TestV2RecallRejectsInvalidContractAndMissingSearch(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	ctx := authenticatedV2RememberContext(teamID, profileID, keyID)

	_, err := NewV2RecallService(V2RecallDependencies{}).RecallV2(ctx, V2RecallRequest{
		ContractVersion: domain.V2ContractVersion,
		Query:           "PostgreSQL memory",
	})
	require.ErrorContains(t, err, "search repository is required")

	_, err = NewV2RecallService(V2RecallDependencies{Search: &v2RecallSearchStub{}}).RecallV2(ctx, V2RecallRequest{
		ContractVersion: "wrong",
		Query:           "PostgreSQL memory",
	})
	require.ErrorContains(t, err, "invalid contract_version")
}

func TestValidateV2RecallEmbeddingRejectsInvalidVectors(t *testing.T) {
	require.NoError(t, validateV2RecallEmbedding([]float32{1, 0, 0}, 3))
	require.Error(t, validateV2RecallEmbedding([]float32{1, 0}, 3))
	require.Error(t, validateV2RecallEmbedding([]float32{float32(math.Inf(1)), 0, 0}, 3))
}

type v2RecallSearchStub struct {
	contract *repository.V2ActiveSearchContract
	input    repository.V2RecallEvidenceInput
	result   *repository.V2RecallEvidenceResult
	err      error
}

func (s *v2RecallSearchStub) GetActiveSearchContract(context.Context) (*repository.V2ActiveSearchContract, error) {
	if s.contract == nil {
		return nil, errors.New("missing contract")
	}
	return s.contract, nil
}

func (s *v2RecallSearchStub) RecallEvidence(_ context.Context, input repository.V2RecallEvidenceInput) (*repository.V2RecallEvidenceResult, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

type v2RecallProviderStub struct {
	available     bool
	model         string
	dims          int
	vector        []float32
	query         string
	err           error
	returnedModel string
}

func (s *v2RecallProviderStub) Embed(_ context.Context, text string) ([]float32, string, error) {
	s.query = text
	if s.err != nil {
		return nil, "", s.err
	}
	model := s.model
	if s.returnedModel != "" {
		model = s.returnedModel
	}
	return append([]float32(nil), s.vector...), model, nil
}

func (s *v2RecallProviderStub) EmbedBatch(context.Context, []string) ([][]float32, string, error) {
	return nil, "", errors.New("unexpected EmbedBatch")
}

func (s *v2RecallProviderStub) ModelName() string { return s.model }

func (s *v2RecallProviderStub) Dimensions() int { return s.dims }

func (s *v2RecallProviderStub) IsAvailable() bool { return s.available }
