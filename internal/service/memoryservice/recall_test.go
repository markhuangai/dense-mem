package memoryservice

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestRecallUsesAuthenticatedTeamAndVectorQuery(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	evidenceID := uuid.NewString()
	relationshipID := uuid.NewString()
	conflictID := uuid.NewString()
	positionID := uuid.NewString()
	evidenceCreatedAt := time.Date(2026, 7, 20, 10, 30, 0, 0, time.UTC)
	reviewDueAt := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	search := &recallSearchStub{
		contract: &repository.ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &repository.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results: []repository.RecallEvidenceHit{{
				EvidenceID:      evidenceID,
				RelationshipIDs: []string{relationshipID},
				Rank:            1,
				Score:           0.99,
				Context:         "Dense-Mem uses PostgreSQL for durable memory.",
				Source:          "wiki:target-architecture",
				SourceType:      "document",
				CreatedAt:       evidenceCreatedAt,
			}},
			Conflicts: []repository.RelationshipConflictCaseRecord{{
				ConflictID:          conflictID,
				Version:             1,
				Kind:                "cross_profile_current_state",
				Status:              "open",
				Question:            "Which database is current?",
				ReviewDueAt:         reviewDueAt,
				PolicyVersion:       domain.ConflictPolicyVersion,
				PreferredPositionID: "",
				Positions: []repository.RelationshipConflictPositionRecord{{
					PositionID:        positionID,
					Disposition:       "candidate",
					RelationshipIDs:   []string{relationshipID},
					OwnerProfileIDs:   []string{profileID.String()},
					EvidenceIDs:       []string{evidenceID},
					SupportGroupCount: 1,
				}},
			}},
		},
	}
	provider := &recallProviderStub{
		available: true,
		model:     "test-model",
		dims:      3,
		vector:    []float32{1, 0, 0},
	}
	svc := NewRecallService(RecallDependencies{Search: search, Provider: provider})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		ContractVersion: domain.ContractVersion,
		Query:           "PostgreSQL memory",
		Limit:           2,
	})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Nil(t, result.Degradation)
	require.Equal(t, evidenceID, result.Results[0].EvidenceID)
	require.Equal(t, "wiki:target-architecture", result.Results[0].Source)
	require.Equal(t, "document", result.Results[0].SourceType)
	require.Equal(t, &evidenceCreatedAt, result.Results[0].CreatedAt)
	require.Len(t, result.Conflicts, 1)
	require.Equal(t, conflictID, result.Conflicts[0].ConflictID)
	require.Equal(t, &reviewDueAt, result.Conflicts[0].ReviewDueAt)
	require.Equal(t, []string{relationshipID}, result.Conflicts[0].Positions[0].RelationshipIDs)
	require.NotEmpty(t, result.DiscoveryGuidance)
	require.Empty(t, result.DiscoveryPaths)
	require.Empty(t, result.RelatedHypotheses)
	require.Equal(t, []float32{1, 0, 0}, search.input.QueryEmbedding)
	require.Equal(t, teamID.String(), search.input.TeamID)
	require.Equal(t, "PostgreSQL memory", provider.query)
}

func TestRecallConflictSummariesEnforcePositionBounds(t *testing.T) {
	records := make([]repository.RelationshipConflictPositionRecord, 0, 11)
	for i := 0; i < 11; i++ {
		records = append(records, repository.RelationshipConflictPositionRecord{
			PositionID:      uuid.NewString(),
			Disposition:     "candidate",
			RelationshipIDs: make([]string, 21),
			OwnerProfileIDs: make([]string, 21),
			EvidenceIDs:     make([]string, 51),
		})
	}
	summaries := recallConflictSummaries([]repository.RelationshipConflictCaseRecord{{
		ConflictID:  uuid.NewString(),
		Version:     1,
		Kind:        "cross_profile_current_state",
		Status:      "open",
		Question:    "Which value is current?",
		ReviewDueAt: time.Now().UTC(),
		Positions:   records,
	}})

	require.Len(t, summaries, 1)
	require.True(t, summaries[0].PositionsTruncated)
	require.Len(t, summaries[0].Positions, 10)
	require.Len(t, summaries[0].Positions[0].RelationshipIDs, 20)
	require.Len(t, summaries[0].Positions[0].OwnerProfileIDs, 20)
	require.Len(t, summaries[0].Positions[0].ResultEvidenceIDs, 50)
}

func TestRecallReturnsRelatedHypothesesOutsidePrimaryResults(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	evidenceID := uuid.NewString()
	hypothesisID := uuid.NewString()
	sourceRelationshipID := uuid.NewString()
	search := &recallSearchStub{
		contract: &repository.ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &repository.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results: []repository.RecallEvidenceHit{{
				EvidenceID: evidenceID,
				Rank:       1,
				Context:    "Dense-Mem uses PostgreSQL for durable memory.",
			}},
		},
	}
	hypotheses := &recallHypothesisStub{
		records: []repository.HypothesisRecord{{
			HypothesisID:    hypothesisID,
			SubjectEntityID: uuid.NewString(),
			PredicateKey:    "benefits_from",
			Statement:       "Dense-Mem may benefit from explicit search freshness.",
			Status:          string(domain.DreamStatusProposed),
			SourceRefs: []map[string]any{{
				"type": "relationship",
				"id":   sourceRelationshipID,
			}},
			GeneratorKind:    "server",
			GeneratorVersion: "dream-v2.candidate-safe",
			CreatedAt:        time.Now().UTC(),
		}},
	}
	svc := NewRecallService(RecallDependencies{Search: search, Hypotheses: hypotheses})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		ContractVersion: domain.ContractVersion,
		Query:           "PostgreSQL memory",
	})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Equal(t, evidenceID, result.Results[0].EvidenceID)
	require.Len(t, result.RelatedHypotheses, 1)
	require.Equal(t, hypothesisID, result.RelatedHypotheses[0].HypothesisID)
	require.Equal(t, "deterministic", result.RelatedHypotheses[0].GeneratorKind)
	require.Equal(t, []string{sourceRelationshipID}, result.RelatedHypotheses[0].SourceRelationshipIDs)
	require.Equal(t, teamID.String(), hypotheses.refreshInput.TeamID)
	require.Equal(t, profileID.String(), hypotheses.refreshInput.OwnerProfileID)
	require.Equal(t, defaultRelatedHypothesisLimit, hypotheses.recallInput.Limit)
	require.Equal(t, "PostgreSQL memory", hypotheses.recallInput.Query)
}

func TestRecallProviderFailureIsOptionalDegradation(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	search := &recallSearchStub{
		contract: &repository.ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &repository.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionPending),
			Results:     []repository.RecallEvidenceHit{},
		},
	}
	provider := &recallProviderStub{available: false}
	svc := NewRecallService(RecallDependencies{Search: search, Provider: provider})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		ContractVersion: domain.ContractVersion,
		Query:           "PostgreSQL memory",
	})
	require.NoError(t, err)
	require.NotNil(t, result.Degradation)
	require.True(t, result.Degradation.Optional)
	require.Equal(t, string(domain.ErrorProviderUnavailable), result.Degradation.Code)
	require.Empty(t, search.input.QueryEmbedding)
	require.Equal(t, string(domain.SearchProjectionPending), result.SearchState)
}

func TestRecallProviderMalformedBranchesAreOptionalDegradation(t *testing.T) {
	tests := []struct {
		name     string
		provider *recallProviderStub
		wantCode string
	}{
		{
			name:     "configured dimensions mismatch",
			provider: &recallProviderStub{available: true, model: "test-model", dims: 4, vector: []float32{1, 0, 0}},
			wantCode: string(domain.ErrorProviderMalformed),
		},
		{
			name:     "configured model mismatch",
			provider: &recallProviderStub{available: true, model: "other-model", dims: 3, vector: []float32{1, 0, 0}},
			wantCode: string(domain.ErrorProviderMalformed),
		},
		{
			name:     "embed failure",
			provider: &recallProviderStub{available: true, model: "test-model", dims: 3, err: errors.New("provider failed")},
			wantCode: string(domain.ErrorProviderUnavailable),
		},
		{
			name:     "returned vector length mismatch",
			provider: &recallProviderStub{available: true, model: "test-model", dims: 3, vector: []float32{1, 0}},
			wantCode: string(domain.ErrorProviderMalformed),
		},
		{
			name:     "returned non finite vector",
			provider: &recallProviderStub{available: true, model: "test-model", dims: 3, vector: []float32{float32(math.NaN()), 0, 0}},
			wantCode: string(domain.ErrorProviderMalformed),
		},
		{
			name:     "returned model mismatch",
			provider: &recallProviderStub{available: true, model: "configured-model", dims: 3, vector: []float32{1, 0, 0}, returnedModel: "other-model"},
			wantCode: string(domain.ErrorProviderMalformed),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			teamID := uuid.New()
			profileID := uuid.New()
			keyID := uuid.New()
			search := &recallSearchStub{
				contract: &repository.ActiveSearchContract{
					EmbeddingContractID: uuid.NewString(),
					EmbeddingDimensions: 3,
					EmbeddingModel:      "test-model",
				},
				result: &repository.RecallEvidenceResult{
					SearchState: string(domain.SearchProjectionCurrent),
					Results:     []repository.RecallEvidenceHit{},
				},
			}
			if tt.name == "returned model mismatch" {
				search.contract.EmbeddingModel = "configured-model"
			}
			svc := NewRecallService(RecallDependencies{Search: search, Provider: tt.provider})

			result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
				ContractVersion: domain.ContractVersion,
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

func TestRecallNormalizesIDs(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	entityID := uuid.NewString()
	evidenceID := uuid.NewString()
	relationshipID := uuid.NewString()
	search := &recallSearchStub{
		contract: &repository.ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &repository.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results:     []repository.RecallEvidenceHit{},
		},
	}
	svc := NewRecallService(RecallDependencies{Search: search})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		ContractVersion:      domain.ContractVersion,
		Query:                " ",
		KnownEvidenceIDs:     []string{" " + evidenceID + " ", evidenceID},
		KnownRelationshipIDs: []string{relationshipID, relationshipID},
		ExpandFromEntityIDs:  []string{entityID, entityID},
	})
	require.NoError(t, err)
	require.Nil(t, result.Degradation)
	require.Equal(t, []string{evidenceID}, search.input.KnownEvidenceIDs)
	require.Equal(t, []string{relationshipID}, search.input.KnownRelationshipIDs)
	require.Equal(t, []string{entityID}, search.input.ExpandFromEntityIDs)
	require.Empty(t, search.input.Query)
}

func TestRecallAddsCommunityDiscoveryWhenEnabledAndPrimaryHasRoom(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	relationshipID := uuid.NewString()
	evidenceID := uuid.NewString()
	subjectID := uuid.NewString()
	objectID := uuid.NewString()
	search := &recallSearchStub{
		contract: &repository.ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &repository.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results:     []repository.RecallEvidenceHit{},
		},
	}
	communities := &recallCommunityStub{
		paths: []repository.CommunityDiscoveryPath{{
			CommunityID: uuid.NewString(),
			Relationship: repository.CommunityDiscoveryRelationship{
				RelationshipID:  relationshipID,
				SubjectEntityID: subjectID,
				SubjectName:     "Dense-Mem",
				PredicateKey:    "uses",
				ObjectEntityID:  objectID,
				ObjectName:      "PostgreSQL",
				Polarity:        "+",
			},
			EvidenceIDs: []string{evidenceID},
		}},
	}
	svc := NewRecallService(RecallDependencies{
		Search:          search,
		Provider:        &recallProviderStub{available: true, model: "test-model", dims: 3, vector: []float32{1, 0, 0}},
		Communities:     communities,
		CommunityConfig: recallCommunityConfigStub{enabled: true},
	})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		ContractVersion: domain.ContractVersion,
		Query:           "PostgreSQL",
		Limit:           3,
	})
	require.NoError(t, err)
	require.Nil(t, result.Degradation)
	require.Empty(t, result.Results)
	require.Len(t, result.DiscoveryPaths, 1)
	require.Equal(t, relationshipID, result.DiscoveryPaths[0].Relationships[0].RelationshipID)
	require.Equal(t, objectID, result.DiscoveryPaths[0].Relationships[0].Object.EntityID)
	require.Equal(t, "PostgreSQL", result.DiscoveryPaths[0].Relationships[0].Object.Name)
	require.Equal(t, []string{evidenceID}, result.DiscoveryPaths[0].EvidenceIDs)
	require.Equal(t, teamID.String(), communities.refreshInput.TeamID)
	require.Equal(t, "PostgreSQL", communities.recallInput.Query)
	require.Equal(t, 3, communities.recallInput.Limit)
}

func TestRecallRequiresAuthenticatedActor(t *testing.T) {
	svc := NewRecallService(RecallDependencies{Search: &recallSearchStub{}})
	_, err := svc.Recall(context.Background(), RecallRequest{
		ContractVersion: domain.ContractVersion,
		Query:           "PostgreSQL memory",
	})
	require.ErrorIs(t, err, ErrRecallAuthContext)
}

func TestRecallRejectsInvalidContractAndMissingSearch(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	ctx := authenticatedRememberContext(teamID, profileID, keyID)

	_, err := NewRecallService(RecallDependencies{}).Recall(ctx, RecallRequest{
		ContractVersion: domain.ContractVersion,
		Query:           "PostgreSQL memory",
	})
	require.ErrorContains(t, err, "search repository is required")

	_, err = NewRecallService(RecallDependencies{Search: &recallSearchStub{}}).Recall(ctx, RecallRequest{
		ContractVersion: "wrong",
		Query:           "PostgreSQL memory",
	})
	require.ErrorContains(t, err, "invalid contract_version")
}

func TestValidateRecallEmbeddingRejectsInvalidVectors(t *testing.T) {
	require.NoError(t, validateRecallEmbedding([]float32{1, 0, 0}, 3))
	require.Error(t, validateRecallEmbedding([]float32{1, 0}, 3))
	require.Error(t, validateRecallEmbedding([]float32{float32(math.Inf(1)), 0, 0}, 3))
}

type recallSearchStub struct {
	contract *repository.ActiveSearchContract
	input    repository.RecallEvidenceInput
	result   *repository.RecallEvidenceResult
	err      error
}

func (s *recallSearchStub) GetActiveSearchContract(context.Context) (*repository.ActiveSearchContract, error) {
	if s.contract == nil {
		return nil, errors.New("missing contract")
	}
	return s.contract, nil
}

func (s *recallSearchStub) RecallEvidence(_ context.Context, input repository.RecallEvidenceInput) (*repository.RecallEvidenceResult, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

type recallHypothesisStub struct {
	refreshInput repository.RefreshHypothesisStalenessInput
	recallInput  repository.RecallHypothesesInput
	records      []repository.HypothesisRecord
	err          error
}

func (s *recallHypothesisStub) RefreshHypothesisStaleness(_ context.Context, input repository.RefreshHypothesisStalenessInput) (int, error) {
	s.refreshInput = input
	if s.err != nil {
		return 0, s.err
	}
	return 0, nil
}

func (s *recallHypothesisStub) RecallHypotheses(_ context.Context, input repository.RecallHypothesesInput) ([]repository.HypothesisRecord, error) {
	s.recallInput = input
	if s.err != nil {
		return nil, s.err
	}
	return s.records, nil
}

type recallCommunityStub struct {
	refreshInput repository.CommunityStalenessInput
	recallInput  repository.CommunityDiscoveryInput
	paths        []repository.CommunityDiscoveryPath
	err          error
}

func (s *recallCommunityStub) RefreshCommunityStaleness(_ context.Context, input repository.CommunityStalenessInput) (int, error) {
	s.refreshInput = input
	if s.err != nil {
		return 0, s.err
	}
	return 0, nil
}

func (s *recallCommunityStub) RecallCommunityDiscovery(_ context.Context, input repository.CommunityDiscoveryInput) ([]repository.CommunityDiscoveryPath, error) {
	s.recallInput = input
	if s.err != nil {
		return nil, s.err
	}
	return s.paths, nil
}

type recallCommunityConfigStub struct {
	enabled bool
	err     error
}

func (s recallCommunityConfigStub) CommunityDetectionRuntimeConfig(context.Context) (domain.CommunityDetectionRuntimeConfig, error) {
	if s.err != nil {
		return domain.CommunityDetectionRuntimeConfig{}, s.err
	}
	return domain.CommunityDetectionRuntimeConfig{Enabled: s.enabled}, nil
}

type recallProviderStub struct {
	available     bool
	model         string
	dims          int
	vector        []float32
	query         string
	err           error
	returnedModel string
}

func (s *recallProviderStub) Embed(_ context.Context, text string) ([]float32, string, error) {
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

func (s *recallProviderStub) EmbedBatch(context.Context, []string) ([][]float32, string, error) {
	return nil, "", errors.New("unexpected EmbedBatch")
}

func (s *recallProviderStub) ModelName() string { return s.model }

func (s *recallProviderStub) Dimensions() int { return s.dims }

func (s *recallProviderStub) IsAvailable() bool { return s.available }
