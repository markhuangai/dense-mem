package memoryservice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestOpenAISemanticProviderReviewSemanticUsesStructuredReview(t *testing.T) {
	var captured semanticChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.Equal(t, "semantic_relationship_review", captured.ResponseFormat.JSONSchema.Name)

		var payload semanticReviewPayload
		require.Len(t, captured.Messages, 2)
		require.NoError(t, json.Unmarshal([]byte(captured.Messages[1].Content), &payload))
		require.Equal(t, "review-1", payload.RequestID)
		require.Equal(t, []semanticReviewEvidencePayload{{
			Index:   5,
			Content: "Dense-Mem uses Postgres.",
			Source:  "seed",
			Units: []semanticReviewUnitPayload{{
				UnitID: "e5_u1",
				Text:   "Dense-Mem uses Postgres.",
			}},
		}}, payload.Evidence)

		writeSemanticChatContent(t, w, `{
			"relationships": [{
				"ref": "r1",
				"unit_id": "e5_u1",
				"subject_name": "Dense-Mem",
				"subject_kind": "project",
				"predicate": "uses",
				"polarity": "+",
				"object_name": "Postgres",
				"object_kind": "product",
				"object_value": "",
				"quote": "Dense-Mem uses Postgres.",
				"confidence": 1.0
			}],
			"skips": []
		}`)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	result, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-1",
		Evidence: []domain.MemoryEvidence{{
			Index:   5,
			Content: " Dense-Mem uses Postgres. ",
			Source:  "seed",
		}},
	})

	require.NoError(t, err)
	require.Equal(t, "review-model", captured.Model)
	require.Nil(t, captured.Temperature)
	require.Equal(t, "review-model", result.Model)
	require.Contains(t, result.RawJSON, `"relationships"`)
	require.Len(t, result.Relationships, 1)
	rel := result.Relationships[0]
	require.Equal(t, "Dense-Mem", rel.SubjectName)
	require.Equal(t, domain.SemanticEntityProject, rel.SubjectKind)
	require.Equal(t, "uses", rel.Predicate)
	require.Equal(t, domain.PolarityPlus, rel.Polarity)
	require.Equal(t, "Postgres", rel.ObjectName)
	require.Equal(t, domain.SemanticEntityProduct, rel.ObjectKind)
	require.Equal(t, domain.SemanticTierCandidate, rel.Tier)
	require.Equal(t, domain.SemanticStatusActive, rel.Status)
	require.Equal(t, 1.0, rel.Confidence)
	require.Equal(t, 5, rel.EvidenceIndex)
	require.Equal(t, "Dense-Mem uses Postgres.", rel.Quote)
	require.Equal(t, 0, rel.SpanStart)
	require.Equal(t, len("Dense-Mem uses Postgres."), rel.SpanEnd)
}

func TestOpenAISemanticProviderVerifySemanticUsesStructuredVerifier(t *testing.T) {
	var captured semanticChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/chat/completions", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.Equal(t, "semantic_relationship_verification", captured.ResponseFormat.JSONSchema.Name)

		var payload semanticVerifierRequest
		require.NoError(t, json.Unmarshal([]byte(captured.Messages[1].Content), &payload))
		require.Equal(t, semanticVerifierSchemaVersion, payload.SchemaVersion)
		require.Equal(t, "verify-1", payload.RequestID)
		require.Contains(t, payload.Relationships, semanticVerifierRelationshipRef(0))

		resp := semanticVerifierResponseForRequest(payload)
		writeSemanticChatJSON(t, w, resp)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)
	req := buildSemanticVerifierRequest("verify-1", semanticRelationshipsFixture())

	resp, err := provider.VerifySemantic(context.Background(), req)

	require.NoError(t, err)
	require.Equal(t, "verify-model", captured.Model)
	require.Nil(t, captured.Temperature)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(captured.ResponseFormat.JSONSchema.Schema, &schema))
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	schemaVersion, ok := properties["schema_version"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "string", schemaVersion["type"])
	require.Equal(t, semanticVerifierSchemaVersion, schemaVersion["const"])
	require.Equal(t, "verify-model", provider.ModelName())
	require.Equal(t, semanticVerifierSchemaVersion, resp.SchemaVersion)
	require.Equal(t, "verify-1", resp.RequestID)
	require.Len(t, resp.EntityResults, 1)
	require.Len(t, resp.RelationshipResults, 1)
}

func TestOpenAISemanticProviderVerifySemanticRepairsVerifierValidationErrors(t *testing.T) {
	req := buildSemanticVerifierRequest("verify-1", semanticRelationshipsFixture())
	var captured []semanticChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request semanticChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		captured = append(captured, request)
		require.Equal(t, "semantic_relationship_verification", request.ResponseFormat.JSONSchema.Name)

		resp := semanticVerifierResponseForRequest(req)
		if len(captured) == 1 {
			resp.EntityResults = nil
			writeSemanticChatJSON(t, w, resp)
			return
		}

		require.Len(t, request.Messages, 4)
		require.Equal(t, "assistant", request.Messages[2].Role)
		require.Contains(t, request.Messages[2].Content, `"entity_results"`)
		require.Equal(t, "user", request.Messages[3].Role)
		require.Contains(t, request.Messages[3].Content, "entity_results coverage mismatch")
		require.Contains(t, request.Messages[3].Content, `schema_version must be "dense-mem-verifier/v1"`)
		require.Contains(t, request.Messages[3].Content, `request_id must be "verify-1"`)
		require.Contains(t, request.Messages[3].Content, "exactly one entity_results item")
		writeSemanticChatJSON(t, w, resp)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	resp, err := provider.VerifySemantic(context.Background(), req)

	require.NoError(t, err)
	require.Len(t, captured, 2)
	require.Len(t, resp.EntityResults, len(req.Entities))
	require.Len(t, resp.RelationshipResults, len(req.Relationships))
}

func TestOpenAISemanticProviderReviewSemanticSkipsEmptyEvidence(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	result, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-1",
		Evidence: []domain.MemoryEvidence{
			{Index: 0, Content: "  "},
			{Index: 1},
		},
	})

	require.NoError(t, err)
	require.False(t, called)
	require.Empty(t, result.Relationships)
	require.Empty(t, result.Model)
}

func TestOpenAISemanticProviderUsesVerifierModelAsDefaultReviewerAndTemperature(t *testing.T) {
	var captured semanticChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		writeSemanticChatContent(t, w, `{"relationships": [], "skips": [{"unit_id": "e0_u1", "reason": "unsupported"}]}`)
	}))
	defer server.Close()

	provider := NewOpenAISemanticProvider(&config.Config{
		AIVerifierAPIURL:         server.URL,
		AIVerifierAPIKey:         "test-key",
		AIVerifierModel:          "verify-model",
		AIVerifierTimeoutSeconds: 5,
	}, server.Client())

	result, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-1",
		Evidence:  []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem uses Postgres."}},
	})

	require.NoError(t, err)
	require.Equal(t, "verify-model", captured.Model)
	require.NotNil(t, captured.Temperature)
	require.Equal(t, 0.0, *captured.Temperature)
	require.Equal(t, "verify-model", result.Model)
}

func TestOpenAISemanticProviderReturnsProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "quota exhausted"},
		}))
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	_, err := provider.VerifySemantic(context.Background(), buildSemanticVerifierRequest("verify-1", semanticRelationshipsFixture()))

	require.Error(t, err)
	var rateLimitErr *verifier.RateLimitError
	require.ErrorAs(t, err, &rateLimitErr)
	require.Contains(t, err.Error(), "quota exhausted")
}

func TestOpenAISemanticProviderReturnsNonRateLimitProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "provider unavailable"},
		}))
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	_, err := provider.VerifySemantic(context.Background(), buildSemanticVerifierRequest("verify-1", semanticRelationshipsFixture()))

	require.Error(t, err)
	var providerErr *verifier.ProviderError
	require.ErrorAs(t, err, &providerErr)
	require.Contains(t, err.Error(), "provider unavailable")
}

func TestOpenAISemanticProviderRejectsMalformedHTTPResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{`))
		require.NoError(t, err)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	_, err := provider.VerifySemantic(context.Background(), buildSemanticVerifierRequest("verify-1", semanticRelationshipsFixture()))

	require.Error(t, err)
	var malformed *verifier.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Contains(t, err.Error(), "failed to decode")
}

func TestOpenAISemanticProviderRejectsMalformedProviderResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{}}))
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	_, err := provider.VerifySemantic(context.Background(), buildSemanticVerifierRequest("verify-1", semanticRelationshipsFixture()))

	require.Error(t, err)
	var malformed *verifier.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Contains(t, err.Error(), "one choice")
}

func TestOpenAISemanticProviderReviewSemanticRejectsMalformedStructuredContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSemanticChatContent(t, w, `{"relationships": [], "unexpected": true}`)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	_, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-1",
		Evidence:  []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem uses Postgres."}},
	})

	require.Error(t, err)
	var malformed *verifier.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
}

func TestOpenAISemanticProviderVerifySemanticRejectsMalformedStructuredContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSemanticChatContent(t, w, `{
			"schema_version": "dense-mem-verifier/v1",
			"request_id": "verify-1",
			"entity_results": [],
			"relationship_results": [],
			"unexpected": true
		}`)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	_, err := provider.VerifySemantic(context.Background(), buildSemanticVerifierRequest("verify-1", semanticRelationshipsFixture()))

	require.Error(t, err)
	var malformed *verifier.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
}

func TestOpenAISemanticProviderReviewSemanticRejectsConversionErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSemanticChatContent(t, w, `{
			"relationships": [{
				"ref": "r1",
				"unit_id": "missing",
				"subject_name": "Dense-Mem",
				"subject_kind": "project",
				"predicate": "uses",
				"polarity": "+",
				"object_name": "Postgres",
				"object_kind": "product",
				"object_value": "",
				"quote": "Dense-Mem uses Postgres.",
				"confidence": 0.8
			}],
			"skips": []
		}`)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	_, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-1",
		Evidence:  []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem uses Postgres."}},
	})

	require.ErrorContains(t, err, `unit_id "missing" is unknown`)
	var malformed *verifier.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
}

func TestOpenAISemanticProviderReviewSemanticRepairsReviewerValidationErrors(t *testing.T) {
	var captured []semanticChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request semanticChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		captured = append(captured, request)
		require.Equal(t, "semantic_relationship_review", request.ResponseFormat.JSONSchema.Name)

		if len(captured) == 1 {
			writeSemanticChatContent(t, w, `{
				"relationships": [{
					"ref": "EMBL-DDBJ-GenBank-collaboration",
					"unit_id": "e0_u1",
					"subject_name": "DDBJ",
					"subject_kind": "organization",
					"predicate": " ",
					"polarity": "+",
					"object_name": "GenBank",
					"object_kind": "organization",
					"object_value": "",
					"quote": "In collaboration with DDBJ and GenBank the database is produced",
					"confidence": 0.8
				}],
				"skips": []
			}`)
			return
		}

		require.Len(t, request.Messages, 4)
		require.Equal(t, "assistant", request.Messages[2].Role)
		require.Contains(t, request.Messages[2].Content, "EMBL-DDBJ-GenBank-collaboration")
		require.Equal(t, "user", request.Messages[3].Role)
		require.Contains(t, request.Messages[3].Content, "predicate must be lower_snake_case ASCII")
		require.Contains(t, request.Messages[3].Content, "lower_snake_case")

		writeSemanticChatContent(t, w, `{
			"relationships": [{
				"ref": "r1",
				"unit_id": "e0_u1",
				"subject_name": "EMBL Nucleotide Sequence Database",
				"subject_kind": "product",
				"predicate": "maintained_by",
				"polarity": "+",
				"object_name": "European Bioinformatics Institute",
				"object_kind": "organization",
				"object_value": "",
				"quote": "the database is produced, maintained and distributed at the European Bioinformatics Institute",
				"confidence": 0.8
			}],
			"skips": []
		}`)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	result, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-1",
		Evidence: []domain.MemoryEvidence{{
			Index:   0,
			Content: "In collaboration with DDBJ and GenBank the database is produced, maintained and distributed at the European Bioinformatics Institute.",
		}},
	})

	require.NoError(t, err)
	require.Len(t, captured, 2)
	require.Len(t, result.Relationships, 1)
	require.Equal(t, "maintained_by", result.Relationships[0].Predicate)
	require.Contains(t, result.RawJSON, "maintained_by")
}

func TestOpenAISemanticProviderReviewSemanticRepairIncludesExactQuoteContext(t *testing.T) {
	var captured []semanticChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request semanticChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		captured = append(captured, request)

		if len(captured) == 1 {
			writeSemanticChatContent(t, w, `{
				"relationships": [{
					"ref": "r1",
					"unit_id": "e0_u1",
					"subject_name": "Dense-Mem",
					"subject_kind": "project",
					"predicate": "stores_in",
					"polarity": "+",
					"object_name": "Postgres",
					"object_kind": "product",
					"object_value": "",
					"quote": "Dense-Mem stores memory in PostgreSQL",
					"confidence": 0.8
				}],
				"skips": []
			}`)
			return
		}

		require.Len(t, request.Messages, 4)
		require.Equal(t, "assistant", request.Messages[2].Role)
		require.Contains(t, request.Messages[2].Content, "Dense-Mem stores memory in PostgreSQL")
		require.Equal(t, "user", request.Messages[3].Role)
		require.Contains(t, request.Messages[3].Content, `quote "Dense-Mem stores memory in PostgreSQL" is not an exact substring`)
		require.Contains(t, request.Messages[3].Content, `unit "e0_u1" text "Dense-Mem stores durable memory in Postgres."`)

		writeSemanticChatContent(t, w, `{
			"relationships": [{
				"ref": "r1",
				"unit_id": "e0_u1",
				"subject_name": "Dense-Mem",
				"subject_kind": "project",
				"predicate": "stores_in",
				"polarity": "+",
				"object_name": "Postgres",
				"object_kind": "product",
				"object_value": "",
				"quote": "Dense-Mem stores durable memory in Postgres.",
				"confidence": 0.8
			}],
			"skips": []
		}`)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)
	result, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-quote-context",
		Evidence: []domain.MemoryEvidence{{
			Index:   0,
			Content: "Dense-Mem stores durable memory in Postgres.",
		}},
	})

	require.NoError(t, err)
	require.Len(t, captured, 2)
	require.Len(t, result.Relationships, 1)
	require.Equal(t, "Dense-Mem stores durable memory in Postgres.", result.Relationships[0].Quote)
}

func TestOpenAISemanticProviderReviewSemanticMapsWhitespaceNormalizedQuoteToExactEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSemanticChatContent(t, w, `{
			"relationships": [{
				"ref": "r1",
				"unit_id": "e0_u1",
				"subject_name": "Wilkes Land",
				"subject_kind": "place",
				"predicate": "extends_to",
				"polarity": "+",
				"object_name": "",
				"object_kind": "unknown",
				"object_value": "Pourquoi Pas Point",
				"quote": "Wilkes Land extends from Cape Hordern in 100°31' E to Pourquoi Pas Point",
				"confidence": 0.8
			}],
			"skips": []
		}`)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)
	result, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-nbsp",
		Evidence: []domain.MemoryEvidence{{
			Index:   0,
			Content: "Wilkes Land extends from Cape Hordern in 100°31'\u00a0E to Pourquoi Pas Point.",
		}},
	})

	require.NoError(t, err)
	require.Len(t, result.Relationships, 1)
	require.Equal(t, "Wilkes Land extends from Cape Hordern in 100°31'\u00a0E to Pourquoi Pas Point", result.Relationships[0].Quote)
	require.Contains(t, result.Relationships[0].Quote, "\u00a0")
}

func TestOpenAISemanticProviderReviewSemanticRepairExplainsMissingObjectForm(t *testing.T) {
	var captured []semanticChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request semanticChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		captured = append(captured, request)
		if len(captured) == 1 {
			writeSemanticChatContent(t, w, `{
				"relationships": [{
					"ref": "r1",
					"unit_id": "e0_u1",
					"subject_name": "Lionel Messi",
					"subject_kind": "person",
					"predicate": "scored_goals_in",
					"polarity": "+",
					"object_name": "",
					"object_kind": "unknown",
					"object_value": "",
					"quote": "Messi has scored eight goals in Copa América",
					"confidence": 0.8
				}],
				"skips": []
			}`)
			return
		}

		require.Len(t, request.Messages, 4)
		require.Equal(t, "user", request.Messages[3].Role)
		require.Contains(t, request.Messages[3].Content, "has neither object_name nor object_value")
		require.Contains(t, request.Messages[3].Content, "set object_name for a named entity")
		require.Contains(t, request.Messages[3].Content, "set object_value for a scalar/text value")
		writeSemanticChatContent(t, w, `{
			"relationships": [{
				"ref": "r1",
				"unit_id": "e0_u1",
				"subject_name": "Lionel Messi",
				"subject_kind": "person",
				"predicate": "scored_goals_in",
				"polarity": "+",
				"object_name": "Copa América",
				"object_kind": "concept",
				"object_value": "",
				"quote": "Messi has scored eight goals in Copa América",
				"confidence": 0.8
			}],
			"skips": []
		}`)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)
	result, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-object-form",
		Evidence:  []domain.MemoryEvidence{{Index: 0, Content: "Messi has scored eight goals in Copa América."}},
	})

	require.NoError(t, err)
	require.Len(t, captured, 2)
	require.Len(t, result.Relationships, 1)
	require.Equal(t, "Copa América", result.Relationships[0].ObjectName)
}

func TestOpenAISemanticProviderReviewSemanticRepairsMissingUnitInSameConversation(t *testing.T) {
	var captured []semanticChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request semanticChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		captured = append(captured, request)
		if len(captured) == 1 {
			writeSemanticChatContent(t, w, `{"relationships": [], "skips": []}`)
			return
		}

		require.Len(t, request.Messages, 4)
		require.Equal(t, captured[0].Messages[:2], request.Messages[:2])
		require.Equal(t, "assistant", request.Messages[2].Role)
		require.Contains(t, request.Messages[2].Content, `"relationships": []`)
		require.Equal(t, "user", request.Messages[3].Role)
		require.Contains(t, request.Messages[3].Content, "unit coverage mismatch: missing [e0_u1]")
		require.Contains(t, request.Messages[3].Content, "Replace the entire prior response")
		writeSemanticChatContent(t, w, `{
			"relationships": [{
				"ref": "r1",
				"unit_id": "e0_u1",
				"subject_name": "Dense-Mem",
				"subject_kind": "project",
				"predicate": "stores_in",
				"polarity": "+",
				"object_name": "Postgres",
				"object_kind": "product",
				"object_value": "",
				"quote": "Dense-Mem stores durable memory in Postgres.",
				"confidence": 0.8
			}],
			"skips": []
		}`)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)
	result, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-coverage",
		Evidence: []domain.MemoryEvidence{{
			Index:   0,
			Content: "Dense-Mem stores durable memory in Postgres. It supports semantic recall.",
		}},
	})

	require.NoError(t, err)
	require.Len(t, captured, 2)
	require.Len(t, result.Relationships, 1)
}

func TestOpenAISemanticProviderReviewSemanticFailsAfterRepairAttempts(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		writeSemanticChatContent(t, w, `{
			"relationships": [{
				"ref": "r1",
				"unit_id": "e0_u1",
				"subject_name": "Dense-Mem",
				"subject_kind": "project",
				"predicate": "uses!",
				"polarity": "+",
				"object_name": "Postgres",
				"object_kind": "product",
				"object_value": "",
				"quote": "Dense-Mem uses Postgres.",
				"confidence": 0.8
			}],
			"skips": []
		}`)
	}))
	defer server.Close()

	provider := semanticProviderForTest(server)

	_, err := provider.ReviewSemantic(context.Background(), semanticReviewRequest{
		RequestID: "review-1",
		Evidence:  []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem uses Postgres."}},
	})

	require.ErrorContains(t, err, "predicate must be lower_snake_case ASCII")
	require.Equal(t, 3, calls)
	var malformed *verifier.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Contains(t, malformed.RawJSON, "uses!")
}

func TestOpenAISemanticProviderRequiresConfiguration(t *testing.T) {
	provider := NewOpenAISemanticProvider(&config.Config{}, nil)

	_, err := provider.VerifySemantic(context.Background(), buildSemanticVerifierRequest("verify-1", semanticRelationshipsFixture()))

	require.ErrorContains(t, err, "not configured")
	require.Equal(t, 60*time.Second, provider.client.Timeout)
}

func TestSemanticReviewConversionAcceptsStrictRelationship(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem stores durable memory in Postgres."}}
	units := splitSemanticReviewUnits(0, evidence[0].Content)

	relationships, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
		Relationships: []semanticReviewRelationship{semanticReviewRelationshipFixture("r1", "e0_u1")},
	})

	require.NoError(t, err)
	require.Len(t, relationships, 1)
	rel := relationships[0]
	require.Equal(t, "Dense-Mem", rel.SubjectName)
	require.Equal(t, domain.SemanticEntityProject, rel.SubjectKind)
	require.Equal(t, "stores_in", rel.Predicate)
	require.Equal(t, domain.PolarityPlus, rel.Polarity)
	require.Equal(t, domain.SemanticEntityProduct, rel.ObjectKind)
	require.Equal(t, "Postgres", rel.ObjectName)
	require.Equal(t, 0.8, rel.Confidence)
	require.Equal(t, evidence[0].Content, rel.Quote)
	require.Equal(t, 0, rel.SpanStart)
	require.Equal(t, len(evidence[0].Content), rel.SpanEnd)
}

func TestSemanticReviewConversionRejectsFormerFallbacks(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem stores durable memory in Postgres."}}
	units := splitSemanticReviewUnits(0, evidence[0].Content)
	tests := []struct {
		name    string
		mutate  func(*semanticReviewRelationship)
		wantErr string
	}{
		{
			name: "unknown entity kind",
			mutate: func(rel *semanticReviewRelationship) {
				rel.SubjectKind = "unexpected-kind"
			},
			wantErr: "subject_kind is invalid",
		},
		{
			name: "normalized predicate",
			mutate: func(rel *semanticReviewRelationship) {
				rel.Predicate = "Stores In"
			},
			wantErr: "predicate must be lower_snake_case ASCII",
		},
		{
			name: "missing exact quote",
			mutate: func(rel *semanticReviewRelationship) {
				rel.Quote = "missing exact quote"
			},
			wantErr: `quote "missing exact quote" is not an exact substring`,
		},
		{
			name: "clamped confidence",
			mutate: func(rel *semanticReviewRelationship) {
				rel.Confidence = -0.5
			},
			wantErr: "confidence is out of range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel := semanticReviewRelationshipFixture("r1", "e0_u1")
			tt.mutate(&rel)
			_, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
				Relationships: []semanticReviewRelationship{rel},
			})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSemanticReviewConversionRequiresExactUnitCoverage(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem stores durable memory in Postgres. It supports semantic recall."}}
	units := splitSemanticReviewUnits(0, evidence[0].Content)

	_, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
		Relationships: []semanticReviewRelationship{},
	})
	require.ErrorContains(t, err, "unit coverage mismatch: missing [e0_u1]")

	relationships, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
		Relationships: []semanticReviewRelationship{semanticReviewRelationshipFixture("r1", "e0_u1")},
	})
	require.NoError(t, err)
	require.Len(t, relationships, 1)
}

func TestSemanticReviewConversionRejectsRelationshipAndSkipForSameUnit(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem stores durable memory in Postgres."}}
	units := splitSemanticReviewUnits(0, evidence[0].Content)

	_, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
		Relationships: []semanticReviewRelationship{semanticReviewRelationshipFixture("r1", "e0_u1")},
		Skips:         []semanticReviewSkip{{UnitID: "e0_u1", Reason: "duplicate"}},
	})

	require.ErrorContains(t, err, "cannot contain relationships and a skip")
}

func TestSemanticReviewConversionRejectsDuplicateReviewerRefs(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem stores durable memory in Postgres."}}
	units := splitSemanticReviewUnits(0, evidence[0].Content)

	_, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
		Relationships: []semanticReviewRelationship{
			semanticReviewRelationshipFixture("r1", "e0_u1"),
			semanticReviewRelationshipFixture("r1", "e0_u1"),
		},
	})

	require.ErrorContains(t, err, "duplicate relationship ref")
}

func TestSplitSemanticReviewUnitsPreservesEvidenceItemBoundary(t *testing.T) {
	content := "On July 13, 2026, Dense-Mem used https://example.com/v2. It stored 42 facts."

	units := splitSemanticReviewUnits(7, content)

	require.Equal(t, []semanticReviewUnit{
		{UnitID: "e7_u1", EvidenceIndex: 7, Text: content, Start: 0, End: len(content)},
	}, units)
}

func semanticReviewRelationshipFixture(ref, unitID string) semanticReviewRelationship {
	return semanticReviewRelationship{
		Ref:         ref,
		UnitID:      unitID,
		SubjectName: "Dense-Mem",
		SubjectKind: "project",
		Predicate:   "stores_in",
		Polarity:    "+",
		ObjectName:  "Postgres",
		ObjectKind:  "product",
		Quote:       "Dense-Mem stores durable memory in Postgres.",
		Confidence:  0.8,
	}
}

func TestDecodeClosedJSONRejectsTrailingData(t *testing.T) {
	var decoded semanticReviewAPIResult

	err := decodeClosedJSON(`{"relationships": []} {}`, &decoded)

	require.Error(t, err)
	var malformed *verifier.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Contains(t, err.Error(), "trailing data")
}

func TestDecodeClosedJSONRejectsUnknownFields(t *testing.T) {
	var decoded semanticReviewAPIResult

	err := decodeClosedJSON(`{"relationships": [], "extra": true}`, &decoded)

	require.Error(t, err)
	var malformed *verifier.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Contains(t, err.Error(), "failed to decode")
}

func semanticProviderForTest(server *httptest.Server) *OpenAISemanticProvider {
	return NewOpenAISemanticProvider(&config.Config{
		AIVerifierAPIURL:             server.URL + "/",
		AIVerifierAPIKey:             "test-key",
		AIReviewerModel:              "review-model",
		AIVerifierModel:              "verify-model",
		AIVerifierDisableTemperature: true,
		AIVerifierTimeoutSeconds:     5,
	}, server.Client())
}

func writeSemanticChatJSON(t *testing.T, w http.ResponseWriter, content any) {
	t.Helper()
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	writeSemanticChatContent(t, w, string(raw))
}

func writeSemanticChatContent(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"content": strings.TrimSpace(content),
			},
		}},
	}))
}
