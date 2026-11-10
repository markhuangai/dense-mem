package recall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"

	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

func authenticatedRememberContext(teamID, profileID, credentialID uuid.UUID) context.Context {
	ctx := correlation.WithID(context.Background(), "corr-canonical")
	return requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, TeamName: "team", IdentityID: credentialID, MembershipID: credentialID,
		OwnerID: profileID, OwnerName: "owner", CredentialID: &credentialID,
		AuthMethod: "api_key", Role: "member", Grants: []string{"read", "write"},
	})
}

func TestRecallProviderFailureIsOptionalDegradation(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	search := &recallSearchStub{
		contract: &searchcontract.ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &recallcontract.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionPending),
			Results:     []recallcontract.RecallEvidenceHit{},
		},
	}
	provider := &recallProviderStub{available: false}
	svc := NewRecallService(RecallDependencies{Search: search, Provider: provider})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		Query: "PostgreSQL memory",
	})
	require.NoError(t, err)
	require.NotNil(t, result.Degradation)
	require.True(t, result.Degradation.Optional)
	require.Equal(t, string(domain.ErrorProviderUnavailable), result.Degradation.Code)
	require.Empty(t, search.input.QueryEmbedding)
	require.Equal(t, string(domain.SearchProjectionPending), result.SearchState)
}

func TestRecallProjectsEvidenceConflictPositionsAndJSONShape(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	positions := make([]recallcontract.EvidenceConflictPositionRecord, 0, 11)
	for index := 0; index < 11; index++ {
		positions = append(positions, recallcontract.EvidenceConflictPositionRecord{
			PositionID: fmt.Sprintf("position-%d", index), CanonicalEvidenceID: fmt.Sprintf("evidence-%d", index), OccurrenceID: fmt.Sprintf("occurrence-%d", index),
			Quote: fmt.Sprintf("quote-%d", index), SpanStart: index, SpanEnd: index + 2, Authority: "primary", Submitted: index == 0,
		})
	}
	summaries := recallEvidenceConflictSummaries([]recallcontract.EvidenceConflictCaseRecord{{
		ConflictID: "conflict-1", Status: "resolved", Version: 3, PreferredPositionID: "position-0", CreatedAt: now, UpdatedAt: now, Positions: positions,
	}})
	require.Len(t, summaries, 1)
	require.Equal(t, "evidence_conflict", summaries[0].Kind)
	require.Equal(t, "preferred", summaries[0].Positions[0].Disposition)
	require.True(t, summaries[0].PositionsTruncated)
	require.Len(t, summaries[0].Positions, recallConflictPositionLimit)
	require.Equal(t, "position-9", summaries[0].Positions[recallConflictPositionLimit-1].PositionID)
	encoded, err := json.Marshal(summaries[0])
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(encoded, &wire))
	require.Equal(t, "evidence_conflict", wire["kind"])
	require.NotContains(t, wire, "question")
	require.NotContains(t, wire, "review_due_at")
}

func TestRecallConflictTeamValidationRejectsCrossTeamEvidence(t *testing.T) {
	recalled := &recallcontract.RecallEvidenceResult{
		EvidenceConflicts: []recallcontract.EvidenceConflictCaseRecord{{TeamID: "team-b", Kind: "evidence_conflict"}},
	}
	require.ErrorIs(t, validateRecallConflictTeams(recalled, "team-a"), ErrRecallRepositoryTeamMismatch)
	recalled.EvidenceConflicts[0].TeamID = "team-a"
	recalled.EvidenceConflicts[0].Kind = "relationship"
	require.ErrorIs(t, validateRecallConflictTeams(recalled, "team-a"), ErrRecallRepositoryTeamMismatch)
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
				contract: &searchcontract.ActiveSearchContract{
					EmbeddingContractID: uuid.NewString(),
					EmbeddingDimensions: 3,
					EmbeddingModel:      "test-model",
				},
				result: &recallcontract.RecallEvidenceResult{
					SearchState: string(domain.SearchProjectionCurrent),
					Results:     []recallcontract.RecallEvidenceHit{},
				},
			}
			if tt.name == "returned model mismatch" {
				search.contract.EmbeddingModel = "configured-model"
			}
			svc := NewRecallService(RecallDependencies{Search: search, Provider: tt.provider})

			result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
				Query: "PostgreSQL memory",
			})
			require.NoError(t, err)
			require.NotNil(t, result.Degradation)
			require.True(t, result.Degradation.Optional)
			require.Equal(t, tt.wantCode, result.Degradation.Code)
			require.Empty(t, search.input.QueryEmbedding)
		})
	}
}

func TestRecallRestoresDefaultResultLimit(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	search := &recallSearchStub{
		contract: &searchcontract.ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &recallcontract.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results:     []recallcontract.RecallEvidenceHit{},
		},
	}
	svc := NewRecallService(RecallDependencies{Search: search})

	_, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		Query: "limits",
		Limit: 0,
	})
	require.NoError(t, err)
	require.Equal(t, defaultRecallResultLimit, search.input.Limit)
}

func TestAppendEvidenceVectorFailureDegradationIsBounded(t *testing.T) {
	result := &RecallResult{}
	appendEvidenceVectorFailureDegradation(result, string(domain.SearchProjectionCurrent))
	if len(result.Degradations) != 0 {
		t.Fatalf("current search state added degradations: %#v", result.Degradations)
	}
	appendEvidenceVectorFailureDegradation(result, string(domain.SearchProjectionFailed))
	if len(result.Degradations) != 1 || result.Degradations[0].Code != "evidence_vector_failed" || result.Degradation != &result.Degradations[0] {
		t.Fatalf("failed search state projection = %#v", result)
	}
	require.Equal(t, "Some evidence vectors are unavailable; lexical recall remains available. Check the control portal for recovery guidance.", result.Degradations[0].Message)
	appendEvidenceVectorFailureDegradation(nil, string(domain.SearchProjectionFailed))
}

func TestRecallRequiresAuthenticatedActor(t *testing.T) {
	svc := NewRecallService(RecallDependencies{Search: &recallSearchStub{}})
	_, err := svc.Recall(context.Background(), RecallRequest{
		Query: "PostgreSQL memory",
	})
	require.ErrorIs(t, err, ErrRecallAuthContext)
}

func TestRecallRequiresSearch(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	ctx := authenticatedRememberContext(teamID, profileID, keyID)

	_, err := NewRecallService(RecallDependencies{}).Recall(ctx, RecallRequest{Query: "PostgreSQL memory"})
	require.ErrorContains(t, err, "search repository is required")
}

func TestValidateRecallEmbeddingRejectsInvalidVectors(t *testing.T) {
	require.NoError(t, validateRecallEmbedding([]float32{1, 0, 0}, 3))
	require.Error(t, validateRecallEmbedding([]float32{1, 0}, 3))
	require.Error(t, validateRecallEmbedding([]float32{float32(math.Inf(1)), 0, 0}, 3))
}

type recallOntologyConfigStub struct {
	enabled bool
	err     error
}

func (s recallOntologyConfigStub) OntologyMaintenanceRuntimeConfig(context.Context) (domain.OntologyMaintenanceConfig, error) {
	if s.err != nil {
		return domain.OntologyMaintenanceConfig{}, s.err
	}
	return domain.OntologyMaintenanceConfig{Enabled: s.enabled}, nil
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
