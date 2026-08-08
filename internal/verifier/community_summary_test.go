package verifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func communitySummaryTestInput() domain.CommunitySummaryInput {
	return domain.CommunitySummaryInput{
		CommunityID:      "community-1",
		SummaryInputHash: "sha256:summary-input",
		Relationships: []domain.CommunitySummaryRelationship{{
			RelationshipID: "11111111-1111-1111-1111-111111111111",
			EvidenceIDs:    []string{"22222222-2222-2222-2222-222222222222"},
			SupportQuotes: []domain.CommunitySummarySupportQuote{{
				EvidenceID: "22222222-2222-2222-2222-222222222222",
				Quote:      "Dense-Mem uses PostgreSQL.",
			}},
			Subject:   "Dense-Mem",
			Predicate: "uses",
			Object:    "PostgreSQL",
		}},
	}
}

func TestSummarizeCommunityValidatesInputAndResponse(t *testing.T) {
	var received openAIVerifierRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		assert.Equal(t, "community_summary", received.ResponseFormat.JSONSchema.Name)
		writeStructuredChatContent(t, w, map[string]any{
			"summary":                   "Storage choices for Dense-Mem.",
			"top_entities":              []string{"Dense-Mem", "Dense-Mem", "PostgreSQL", "Redis", "Go", "Extra"},
			"top_predicates":            []string{"uses", "uses", "stores"},
			"admitted_relationship_ids": []string{"11111111-1111-1111-1111-111111111111"},
			"admitted_evidence_ids":     []string{"22222222-2222-2222-2222-222222222222"},
			"admitted_support_quotes": []map[string]string{{
				"evidence_id": "22222222-2222-2222-2222-222222222222",
				"quote":       "Dense-Mem uses PostgreSQL.",
			}},
		})
	}))
	defer server.Close()

	verifier := NewOpenAIVerifier(newTestVerifierConfig(server.URL, "key", "summary-model"), server.Client())
	got, err := verifier.SummarizeCommunity(context.Background(), communitySummaryTestInput())
	require.NoError(t, err)
	assert.Equal(t, "Storage choices for Dense-Mem.", got.Summary)
	assert.Equal(t, []string{"Dense-Mem", "Dense-Mem", "PostgreSQL", "Redis", "Go", "Extra"}, got.TopEntities)
	assert.Equal(t, []string{"uses", "uses", "stores"}, got.TopPredicates)
	assert.Equal(t, "sha256:summary-input", got.InputHash)
	assert.Equal(t, "summary-model", got.ProviderModel)
	assert.NotEmpty(t, got.ResponseHash)
}

func TestSummarizeCommunityRejectsUnavailableEmptyAndMalformed(t *testing.T) {
	var unavailable *ProviderError
	_, err := (*OpenAIVerifier)(nil).SummarizeCommunity(context.Background(), communitySummaryTestInput())
	require.ErrorAs(t, err, &unavailable)

	verifier := &OpenAIVerifier{model: "summary-model"}
	_, err = verifier.SummarizeCommunity(context.Background(), domain.CommunitySummaryInput{CommunityID: "community-1"})
	require.Error(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeStructuredChatContent(t, w, map[string]any{
			"summary": "",
		})
	}))
	defer server.Close()
	verifier = NewOpenAIVerifier(newTestVerifierConfig(server.URL, "key", "summary-model"), server.Client())
	_, err = verifier.SummarizeCommunity(context.Background(), communitySummaryTestInput())
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
}
