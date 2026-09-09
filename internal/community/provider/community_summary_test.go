package provider

import (
	"context"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarizeCommunityOwnsSchemaRequestAndResponseMapping(t *testing.T) {
	input := domain.CommunitySummaryInput{CommunityID: "community-1", SummaryInputHash: "sha256:input", Relationships: []domain.CommunitySummaryRelationship{{RelationshipID: "11111111-1111-1111-1111-111111111111", Subject: "Dense-Mem", Predicate: "uses", Object: "PostgreSQL"}}}
	var gotSchema string
	result, err := SummarizeCommunity(context.Background(), "summary-model", input, func(_ context.Context, model, schemaName string, schema map[string]any, prompt string, payload any) (string, error) {
		assert.Equal(t, "summary-model", model)
		assert.Equal(t, "community_summary", schemaName)
		assert.NotEmpty(t, schema)
		assert.Equal(t, Prompt, prompt)
		gotSchema = schemaName
		assert.Equal(t, input, payload)
		return `{"summary":"Storage choices","top_entities":[],"top_predicates":[],"admitted_relationship_ids":["11111111-1111-1111-1111-111111111111"],"admitted_evidence_ids":[],"admitted_support_quotes":[]}`, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "community_summary", gotSchema)
	assert.Equal(t, "Storage choices", result.Summary)
	assert.Equal(t, "sha256:input", result.InputHash)
	assert.Equal(t, "summary-model", result.ProviderModel)
	assert.NotEmpty(t, result.ResponseHash)
}

func TestSummarizeCommunityRejectsMalformedCompleteResponse(t *testing.T) {
	_, err := SummarizeCommunity(context.Background(), "model", domain.CommunitySummaryInput{CommunityID: "community-1", Relationships: []domain.CommunitySummaryRelationship{{RelationshipID: "relationship-1"}}}, func(context.Context, string, string, map[string]any, string, any) (string, error) {
		return "not-json", nil
	})
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	assert.Equal(t, "not-json", malformed.RawJSON)
}
