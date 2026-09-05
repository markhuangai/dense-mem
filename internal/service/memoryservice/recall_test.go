package memoryservice

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
	supportAcceptedAt := time.Date(2026, 7, 20, 10, 31, 0, 0, time.UTC)
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
				TeamID:              teamID.String(),
				ConflictID:          conflictID,
				Version:             1,
				Kind:                "cross_profile_current_state",
				Status:              "open",
				Question:            "Which database is current?",
				ReviewDueAt:         reviewDueAt,
				PolicyVersion:       domain.ConflictPolicyVersion,
				PreferredPositionID: "",
				Positions: []repository.RelationshipConflictPositionRecord{{
					PositionID:     positionID,
					Disposition:    "candidate",
					SupporterCount: 1,
					Supporters: []repository.RelationshipConflictSupporterRecord{{
						ProfileID:          profileID.String(),
						ProfileName:        "Profile A",
						StrongestAuthority: "authoritative",
						EvidenceID:         evidenceID,
						AcceptedAt:         supportAcceptedAt,
					}},
					RelationshipIDs: []string{relationshipID},
					OwnerProfileIDs: []string{profileID.String()},
					EvidenceIDs:     []string{evidenceID},
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
		Query: "PostgreSQL memory",
		Limit: 2,
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
	require.Equal(t, 1, result.Conflicts[0].Positions[0].SupporterCount)
	require.False(t, result.Conflicts[0].Positions[0].SupportersTruncated)
	require.Equal(t, []RecallConflictSupporter{{
		ProfileID:          profileID.String(),
		ProfileName:        "Profile A",
		StrongestAuthority: "authoritative",
		EvidenceID:         evidenceID,
		AcceptedAt:         supportAcceptedAt,
	}}, result.Conflicts[0].Positions[0].Supporters)
	require.NotEmpty(t, result.DiscoveryGuidance)
	require.Empty(t, result.DiscoveryPaths)
	require.Empty(t, result.RelatedHypotheses)
	require.Equal(t, []float32{1, 0, 0}, search.input.QueryEmbedding)
	require.Equal(t, teamID.String(), search.input.TeamID)
	require.Equal(t, "PostgreSQL memory", provider.query)
}

func TestRelatedHypothesisSourceIDsExcludeEvidenceReferences(t *testing.T) {
	got := relatedHypothesisSourceIDs([]map[string]any{
		{"type": "relationship", "id": "relationship-1"},
		{"type": "candidate_relationship", "id": "candidate-1"},
		{"type": "evidence", "id": "evidence-1"},
	})
	require.Equal(t, []string{"relationship-1", "candidate-1"}, got)
}

func TestRecallRejectsMismatchedConflictTeam(t *testing.T) {
	teamID := uuid.New()
	search := &recallSearchStub{
		contract: &repository.ActiveSearchContract{},
		result: &repository.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Conflicts: []repository.RelationshipConflictCaseRecord{{
				TeamID:     uuid.NewString(),
				ConflictID: uuid.NewString(),
			}},
		},
	}
	svc := NewRecallService(RecallDependencies{Search: search})

	_, err := svc.Recall(authenticatedRememberContext(teamID, uuid.New(), uuid.New()), RecallRequest{})
	require.ErrorIs(t, err, ErrRecallRepositoryTeamMismatch)
}

func TestRecallConflictSummariesEnforcePositionBounds(t *testing.T) {
	records := make([]repository.RelationshipConflictPositionRecord, 0, 11)
	for i := 0; i < 11; i++ {
		supporters := make([]repository.RelationshipConflictSupporterRecord, 21)
		records = append(records, repository.RelationshipConflictPositionRecord{
			PositionID:      uuid.NewString(),
			Disposition:     "candidate",
			Supporters:      supporters,
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
	require.Len(t, summaries[0].Positions[0].Supporters, 20)
	require.True(t, summaries[0].Positions[0].SupportersTruncated)
}

func TestRecallConflictJSONKeepsRelationshipShapeAndAddsEvidenceBranch(t *testing.T) {
	relationship := RecallConflictSummary{
		ConflictID: "relationship-1", Version: 1, Kind: "cross_profile_current_state", Status: "open",
		Question: "Which value is current?", Positions: []RecallConflictPosition{{
			PositionID: "position-1", Disposition: "candidate", SpanStart: 4, SpanEnd: 9,
			RelationshipIDs: []string{"relationship-1"}, Supporters: []RecallConflictSupporter{},
		}},
	}
	evidence := RecallConflictSummary{
		ConflictID: "evidence-1", Version: 2, Kind: "evidence_conflict", Status: "resolved", PreferredPositionID: "position-2",
		Positions: []RecallConflictPosition{{
			PositionID: "position-2", Disposition: "preferred", EvidenceID: "evidence-1", OccurrenceID: "occurrence-1",
			Quote: "exact", SpanStart: 0, SpanEnd: 5, Authority: "primary", Submitted: true,
		}},
	}
	relationshipJSON, err := json.Marshal(relationship)
	require.NoError(t, err)
	evidenceJSON, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NotContains(t, string(relationshipJSON), "span_start")
	require.NotContains(t, string(relationshipJSON), "occurrence_id")
	require.Contains(t, string(evidenceJSON), `"span_start":0`)
	require.Contains(t, string(evidenceJSON), `"occurrence_id":"occurrence-1"`)
	require.NotContains(t, string(evidenceJSON), `"question"`)
}

func TestPublicHypothesisGeneratorKindPreservesProvider(t *testing.T) {
	require.Equal(t, "provider", publicHypothesisGeneratorKind("provider"))
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
		Query:             "PostgreSQL memory",
		IncludeHypotheses: true,
	})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Equal(t, evidenceID, result.Results[0].EvidenceID)
	require.Len(t, result.RelatedHypotheses, 1)
	require.Equal(t, hypothesisID, result.RelatedHypotheses[0].HypothesisID)
	require.Equal(t, "deterministic", result.RelatedHypotheses[0].GeneratorKind)
	require.Equal(t, []string{sourceRelationshipID}, result.RelatedHypotheses[0].SourceRelationshipIDs)
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
	positions := make([]repository.EvidenceConflictPositionRecord, 0, 11)
	for index := 0; index < 11; index++ {
		positions = append(positions, repository.EvidenceConflictPositionRecord{
			PositionID: fmt.Sprintf("position-%d", index), CanonicalEvidenceID: fmt.Sprintf("evidence-%d", index), OccurrenceID: fmt.Sprintf("occurrence-%d", index),
			Quote: fmt.Sprintf("quote-%d", index), SpanStart: index, SpanEnd: index + 2, Authority: "primary", Submitted: index == 0,
		})
	}
	summaries := recallEvidenceConflictSummaries([]repository.EvidenceConflictCaseRecord{{
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

func TestRecallConflictLimitPreservesRelationshipOrderAndDeduplicates(t *testing.T) {
	values := []RecallConflictSummary{
		{ConflictID: "same", Kind: "relationship"},
		{ConflictID: "same", Kind: "evidence_conflict"},
		{ConflictID: "evidence-2", Kind: "evidence_conflict"},
		{ConflictID: "relationship-2", Kind: "relationship"},
	}
	require.Empty(t, limitRecallConflictSummaries(values, 0))
	limited := limitRecallConflictSummaries(values, 3)
	require.Equal(t, []string{"same", "relationship-2", "evidence-2"}, []string{limited[0].ConflictID, limited[1].ConflictID, limited[2].ConflictID})
}

func TestRecallConflictTeamValidationRejectsCrossTeamEvidence(t *testing.T) {
	recalled := &repository.RecallEvidenceResult{
		Conflicts:         []repository.RelationshipConflictCaseRecord{{TeamID: "team-a"}},
		EvidenceConflicts: []repository.EvidenceConflictCaseRecord{{TeamID: "team-b", Kind: "evidence_conflict"}},
	}
	require.ErrorIs(t, validateRecallConflictTeams(recalled, "team-a"), ErrRecallRepositoryTeamMismatch)
	recalled.Conflicts = nil
	recalled.EvidenceConflicts[0].TeamID = "team-a"
	recalled.EvidenceConflicts[0].Kind = "relationship"
	require.ErrorIs(t, validateRecallConflictTeams(recalled, "team-a"), ErrRecallRepositoryTeamMismatch)
}

func TestRecallConflictHelpersHandleNilAndExcludedRelationshipGroups(t *testing.T) {
	require.NoError(t, validateRecallConflictTeams(nil, "team-a"))
	values := []RelatedRelationshipSummary{
		{RelationshipID: "kept", SemanticGroupKey: "keep"},
		{RelationshipID: "removed", SemanticGroupKey: "remove"},
	}
	filtered := filterRelatedRelationshipsByGroups(values, map[string]struct{}{"remove": {}})
	require.Len(t, filtered, 1)
	require.Equal(t, "kept", filtered[0].RelationshipID)
}

func TestRecallProviderFailureReportsFailedRelationshipProjection(t *testing.T) {
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
		relationshipResult: &repository.RecallRelationshipsResult{
			TeamID:      teamID.String(),
			SearchState: string(domain.SearchProjectionFailed),
			Results:     []repository.RecallRelationshipHit{},
		},
	}
	svc := NewRecallService(RecallDependencies{
		Search:   search,
		Provider: &recallProviderStub{available: false},
	})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		Query: "PostgreSQL memory",
	})
	require.NoError(t, err)
	require.Equal(t, string(domain.SearchProjectionFailed), result.SearchStates.Relationships)
	require.Len(t, result.Degradations, 2)
	require.Equal(t, string(domain.ErrorProviderUnavailable), result.Degradations[0].Code)
	require.Equal(t, "relationship_vector_failed", result.Degradations[1].Code)
	require.Empty(t, search.relationshipInput.QueryEmbedding)
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
		Query: "PostgreSQL",
		Limit: 3,
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

func TestRecallSkipsCommunityDiscoveryWhenPrimaryResultsFillLimit(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	evidenceID := uuid.NewString()
	search := &recallSearchStub{
		contract: &repository.ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &repository.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results: []repository.RecallEvidenceHit{{
				TeamID:     teamID.String(),
				EvidenceID: evidenceID,
				Context:    "PostgreSQL is used.",
				SourceType: "document",
				CreatedAt:  time.Now().UTC(),
			}},
		},
	}
	communities := &recallCommunityStub{}
	svc := NewRecallService(RecallDependencies{
		Search:          search,
		Provider:        &recallProviderStub{available: true, model: "test-model", dims: 3, vector: []float32{1, 0, 0}},
		Communities:     communities,
		CommunityConfig: recallCommunityConfigStub{enabled: true},
	})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		Query: "PostgreSQL",
		Limit: 1,
	})

	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Empty(t, result.DiscoveryPaths)
	require.Empty(t, communities.refreshInput.TeamID)
	require.Empty(t, communities.recallInput.TeamID)
}

func TestRecallReturnsRelatedRelationshipsAndVectorDegradation(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	entityRelationshipID := uuid.NewString()
	equivalentRelationshipID := uuid.NewString()
	valueRelationshipID := uuid.NewString()
	subjectID := uuid.NewString()
	objectEntityID := uuid.NewString()
	objectValueID := uuid.NewString()
	relationshipLimit := 2
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
		relationshipResult: &repository.RecallRelationshipsResult{
			TeamID:        teamID.String(),
			SearchState:   string(domain.SearchProjectionPending),
			VectorOmitted: true,
			Results: []repository.RecallRelationshipHit{
				{
					RelationshipID:            entityRelationshipID,
					EquivalentRelationshipIDs: []string{equivalentRelationshipID},
					SubjectEntityID:           subjectID,
					SubjectName:               "Dense-Mem",
					PredicateKey:              "uses",
					ObjectEntityID:            objectEntityID,
					ObjectName:                "PostgreSQL",
					Polarity:                  "+",
					SearchState:               string(domain.SearchProjectionPending),
				},
				{
					RelationshipID:  valueRelationshipID,
					SubjectEntityID: subjectID,
					SubjectName:     "Dense-Mem",
					PredicateKey:    "has_release",
					ObjectValueID:   objectValueID,
					ObjectValueType: "string",
					ObjectValue:     "v2.3",
					ObjectName:      "v2.3",
					Polarity:        "+",
					SearchState:     string(domain.SearchProjectionCurrent),
				},
			},
		},
	}
	provider := &recallProviderStub{available: true, model: "test-model", dims: 3, vector: []float32{1, 0, 0}}
	svc := NewRecallService(RecallDependencies{Search: search, Provider: provider})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		Query:             "Dense-Mem PostgreSQL",
		RelationshipLimit: &relationshipLimit,
	})
	require.NoError(t, err)
	require.Equal(t, string(domain.SearchProjectionPending), result.SearchStates.Relationships)
	require.Len(t, result.RelatedRelationships, 2)
	require.Equal(t, entityRelationshipID, result.RelatedRelationships[0].RelationshipID)
	require.Equal(t, search.relationshipResult.Results[0].EquivalentRelationshipIDs, result.RelatedRelationships[0].EquivalentRelationshipIDs)
	search.relationshipResult.Results[0].EquivalentRelationshipIDs[0] = uuid.NewString()
	require.Equal(t, []string{equivalentRelationshipID}, result.RelatedRelationships[0].EquivalentRelationshipIDs)
	require.Equal(t, objectEntityID, result.RelatedRelationships[0].Object.EntityID)
	require.Equal(t, "PostgreSQL", result.RelatedRelationships[0].Object.Name)
	require.Equal(t, valueRelationshipID, result.RelatedRelationships[1].RelationshipID)
	require.NotNil(t, result.RelatedRelationships[1].EquivalentRelationshipIDs)
	require.Empty(t, result.RelatedRelationships[1].EquivalentRelationshipIDs)
	require.Equal(t, objectValueID, result.RelatedRelationships[1].Object.ValueID)
	require.Equal(t, "string", result.RelatedRelationships[1].Object.Type)
	require.Equal(t, "v2.3", result.RelatedRelationships[1].Object.Value)
	require.Len(t, result.Degradations, 1)
	require.Equal(t, "relationships", result.Degradations[0].Frontier)
	require.Equal(t, "relationship_vector_warming", result.Degradations[0].Code)
	require.Equal(t, []float32{1, 0, 0}, search.relationshipInput.QueryEmbedding)
	require.Equal(t, relationshipLimit, search.relationshipInput.Limit)
}

func TestRecallSkipsRelatedRelationshipsWhenLimitIsZero(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	relationshipLimit := 0
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
		Query:             "PostgreSQL",
		RelationshipLimit: &relationshipLimit,
	})
	require.NoError(t, err)
	require.False(t, search.relationshipCalled)
	require.Empty(t, result.RelatedRelationships)
	require.Equal(t, string(domain.SearchProjectionNotRequired), result.SearchStates.Relationships)
}

func TestRecallRestoresDefaultResultLimit(t *testing.T) {
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
	svc := NewRecallService(RecallDependencies{Search: search})

	_, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		Query: "limits",
		Limit: 0,
	})
	require.NoError(t, err)
	require.Equal(t, defaultRecallResultLimit, search.input.Limit)
}

func TestRecallAddsOptionalFrontierDegradations(t *testing.T) {
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
		relationshipErr: errors.New("relationship recall failed"),
	}
	communities := &recallCommunityStub{}
	hypotheses := &recallHypothesisStub{err: errors.New("hypotheses failed")}
	svc := NewRecallService(RecallDependencies{
		Search:          search,
		Provider:        &recallProviderStub{available: true, model: "test-model", dims: 3, vector: []float32{1, 0, 0}},
		Communities:     communities,
		CommunityConfig: recallCommunityConfigStub{err: errors.New("community config failed")},
		Hypotheses:      hypotheses,
	})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		Query:             "PostgreSQL",
		IncludeHypotheses: true,
	})
	require.NoError(t, err)
	require.Len(t, result.Degradations, 3)
	require.Equal(t, "relationships", result.Degradations[0].Frontier)
	require.Equal(t, "relationship_discovery_unavailable", result.Degradations[0].Code)
	require.Equal(t, "communities", result.Degradations[1].Frontier)
	require.Equal(t, "community_discovery_unavailable", result.Degradations[1].Code)
	require.Equal(t, "hypotheses", result.Degradations[2].Frontier)
	require.Equal(t, "related_hypotheses_unavailable", result.Degradations[2].Code)
	require.Equal(t, string(domain.SearchProjectionFailed), result.SearchStates.Relationships)
	require.Equal(t, &result.Degradations[0], result.Degradation)
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

type recallSearchStub struct {
	contract           *repository.ActiveSearchContract
	input              repository.RecallEvidenceInput
	relationshipInput  repository.RecallRelationshipsInput
	result             *repository.RecallEvidenceResult
	relationshipResult *repository.RecallRelationshipsResult
	relationshipCalled bool
	relationshipCalls  int
	err                error
	relationshipErr    error
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

func (s *recallSearchStub) RecallRelationships(_ context.Context, input repository.RecallRelationshipsInput) (*repository.RecallRelationshipsResult, error) {
	s.relationshipCalled = true
	s.relationshipCalls++
	s.relationshipInput = input
	if s.relationshipErr != nil {
		return nil, s.relationshipErr
	}
	if s.relationshipResult != nil {
		return s.relationshipResult, nil
	}
	return &repository.RecallRelationshipsResult{
		TeamID:      input.TeamID,
		SearchState: string(domain.SearchProjectionCurrent),
		Results:     []repository.RecallRelationshipHit{},
	}, nil
}

type recallHypothesisStub struct {
	recallInput repository.RecallHypothesesInput
	records     []repository.HypothesisRecord
	err         error
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
