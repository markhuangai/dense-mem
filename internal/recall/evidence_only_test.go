package recall

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	"github.com/stretchr/testify/require"
)

func TestEvidenceOnlyUnitRecallTopicsRequireUniqueBoundedUUIDs(t *testing.T) {
	id := uuid.NewString()
	zero, ten, eleven, negative := 0, 10, 11, -1
	for _, request := range []RecallRequest{{TopicIDs: []string{id}}, {Query: "question"}, {TopicIDs: []string{id}, TopicLimit: &zero}, {TopicIDs: []string{id}, TopicLimit: &ten}} {
		require.NoError(t, validateRecallRequest(request))
	}
	for _, request := range []RecallRequest{{}, {TopicIDs: []string{}}, {TopicIDs: []string{id, id}}, {TopicIDs: []string{"foreign-name"}}, {TopicIDs: []string{uuid.Nil.String()}}, {TopicIDs: []string{id}, TopicLimit: &eleven}, {Query: "question", TopicLimit: &negative}} {
		require.Error(t, validateRecallRequest(request))
	}
	normalized := normalizeRecallRequest(RecallRequest{Query: " question "})
	require.Nil(t, normalized.TopicIDs)
	require.Equal(t, 3, *normalized.TopicLimit)
	require.Equal(t, 0, *normalizeRecallRequest(RecallRequest{TopicIDs: []string{id}, TopicLimit: &zero}).TopicLimit)
}

type evidenceTopicFixture struct {
	*recallSearchStub
	topicInput  recallcontract.RecallEvidenceInput
	returnedIDs []string
	limit       int
	selection   recallcontract.RelatedTopicResult
	err         error
}

func (f *evidenceTopicFixture) RelatedEvidenceTopics(_ context.Context, input recallcontract.RecallEvidenceInput, ids []string, limit int) (recallcontract.RelatedTopicResult, error) {
	f.topicInput, f.returnedIDs, f.limit = input, ids, limit
	return f.selection, f.err
}

func TestEvidenceOnlyUnitRecallBindsActorAndProjectsDirectTopics(t *testing.T) {
	team, owner, shared := uuid.New(), uuid.New(), uuid.New()
	evidence, topic := uuid.NewString(), uuid.NewString()
	fixture := &evidenceTopicFixture{recallSearchStub: &recallSearchStub{
		contract: &searchcontract.ActiveSearchContract{EmbeddingDimensions: 3, EmbeddingModel: "test-model"},
		result:   &recallcontract.RecallEvidenceResult{SearchState: "current", Results: []recallcontract.RecallEvidenceHit{{TeamID: team.String(), EvidenceID: evidence, Context: "exact Evidence", Source: "document:1", SourceType: "document"}}},
	}, selection: recallcontract.RelatedTopicResult{Topics: []recallcontract.RelatedTopic{{TopicID: topic, Name: "Operations", Description: "Operating procedures", EligibleEvidenceCount: 8, Rank: 1}}, Truncated: true}}
	provider := &recallProviderStub{available: true, model: "test-model", dims: 3, vector: []float32{1, 0, 0}}
	svc := NewRecallService(RecallDependencies{Search: fixture, Provider: provider, OntologyConfig: recallOntologyConfigStub{enabled: true}})
	ctx := requestctx.WithActor(t.Context(), requestctx.Actor{TeamID: team, OwnerID: owner, AllowedSpaces: []domain.MemorySpaceAccess{{ID: shared, Kind: domain.MemorySpaceTeamShared}}})
	result, err := svc.Recall(ctx, RecallRequest{Query: " exact ", TopicIDs: []string{topic}})
	require.NoError(t, err)
	require.Equal(t, team.String(), fixture.input.TeamID)
	require.Equal(t, shared.String(), fixture.input.SpaceID)
	require.Equal(t, []string{topic}, fixture.input.TopicIDs)
	require.Equal(t, []float32{1, 0, 0}, fixture.input.QueryEmbedding)
	require.Equal(t, "exact", provider.query)
	require.Equal(t, 3, fixture.limit)
	require.Equal(t, []string{evidence}, fixture.returnedIDs)
	require.Equal(t, fixture.selection.Topics, result.RelatedTopics)
	require.True(t, result.TopicsTruncated)
	require.Equal(t, "exact Evidence", result.Results[0].Context)
	require.Empty(t, result.Degradations)
}

func TestEvidenceOnlyUnitRecallTopicFailureIsVisibleAndExplicitFiltersFail(t *testing.T) {
	ctx := authenticatedRememberContext(uuid.New(), uuid.New(), uuid.New())
	fixture := &evidenceTopicFixture{recallSearchStub: &recallSearchStub{contract: &searchcontract.ActiveSearchContract{}, result: &recallcontract.RecallEvidenceResult{SearchState: "current"}}, err: errors.New("topic snapshot unavailable")}
	svc := NewRecallService(RecallDependencies{Search: fixture, OntologyConfig: recallOntologyConfigStub{enabled: true}})
	result, err := svc.Recall(ctx, RecallRequest{Query: "question"})
	require.NoError(t, err)
	require.Contains(t, result.Degradations, RecallDegradationResult{Frontier: "topics", Optional: true, Code: "topic_unavailable", Message: "Related topics are unavailable."})
	result, err = svc.Recall(ctx, RecallRequest{TopicIDs: []string{uuid.NewString()}})
	require.ErrorIs(t, err, ErrTopicUnavailable)
	require.Nil(t, result)
	private := requestctx.WithActor(t.Context(), requestctx.Actor{TeamID: uuid.New(), OwnerID: uuid.New(), AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.New(), Kind: domain.MemorySpacePrivate}}})
	result, err = svc.Recall(private, RecallRequest{TopicIDs: []string{uuid.NewString()}})
	require.ErrorIs(t, err, ErrTopicUnavailable)
	require.Nil(t, result)
}
