package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestVerifierConfig builds a minimal config.Config pointed at the given
// test-server URL. The remaining fields take zero-value defaults, which is
// fine for tests that do not exercise embedding or other subsystems.
func newTestVerifierConfig(serverURL, apiKey, model string) *config.Config {
	return &config.Config{
		AIAPIURL:        serverURL,
		AIAPIKey:        apiKey,
		AIReviewerModel: model,
		AIVerifierModel: model,
	}
}

func TestOpenAIVerifierSetMetricsNormalizesNil(t *testing.T) {
	v := NewOpenAIVerifier(newTestVerifierConfig("", "", "m"), nil)

	v.SetMetrics(nil)
	require.NotNil(t, v.metrics)

	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	v.SetMetrics(metrics)
	require.Same(t, metrics, v.metrics)
}

func TestOpenAIVerifierAdapterHelpers(t *testing.T) {
	v := NewOpenAIVerifier(newTestVerifierConfig("https://example.com/v1", "sk-test", "gpt-4.1-mini"), nil)
	require.Equal(t, "gpt-4.1-mini", v.ModelName())

	summary := openAIValidationSummary([]SemanticValidationError{
		{Field: "relationships[0].predicate", Message: "is required"},
		{Message: "duplicate response"},
	})
	require.Equal(t, "relationships[0].predicate: is required; duplicate response", summary)
}

// verifierSuccessHandler returns an HTTP handler that always replies with a
// valid verification result carrying the given verdict, confidence, and rationale.
func verifierSuccessHandler(verdict string, confidence float64, rationale string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload := fmt.Sprintf(
			`{"verdict":%q,"confidence":%g,"rationale":%q}`,
			verdict, confidence, rationale,
		)
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": payload}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func writeStructuredChatContent(t *testing.T, w http.ResponseWriter, content any) {
	t.Helper()
	payload, err := json.Marshal(content)
	require.NoError(t, err)
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": string(payload)}},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(resp))
}

// TestOpenAIVerifier covers the happy path and all validation branches
// required by AC-24 (structured output) and AC-25 (response validation).
func TestOpenAIVerifier(t *testing.T) {
	t.Run("HappyPath_Entailed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify request shape.
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/chat/completions", r.URL.Path)
			assert.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			// Decode and inspect the request body.
			var reqBody openAIVerifierRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
			assert.Equal(t, "gpt-4o-mini", reqBody.Model)
			require.NotNil(t, reqBody.Temperature)
			assert.Equal(t, float64(0), *reqBody.Temperature)
			assert.Equal(t, "json_schema", reqBody.ResponseFormat.Type)
			assert.True(t, reqBody.ResponseFormat.JSONSchema.Strict)
			require.Len(t, reqBody.Messages, 2)
			assert.Equal(t, "system", reqBody.Messages[0].Role)
			assert.Equal(t, "user", reqBody.Messages[1].Role)

			verifierSuccessHandler("entailed", 0.95, "The evidence directly supports the claim.")(w, r)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "sk-test", "gpt-4o-mini"), srv.Client())

		got, err := v.Verify(context.Background(), Request{
			ProfileID: "profile-A",
			Predicate: "The sky is blue.",
			Context:   "Atmospheric scattering favours short wavelengths.",
		})

		require.NoError(t, err)
		assert.Equal(t, "entailed", got.Verdict)
		assert.Equal(t, 0.95, got.Confidence)
		assert.NotEmpty(t, got.Reasoning)
		// AC-28: RawJSON must be preserved.
		assert.NotEmpty(t, got.RawJSON)
	})

	t.Run("UsesVerifierEndpointAndKey", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/chat/completions", r.URL.Path)
			assert.Equal(t, "Bearer verifier-key", r.Header.Get("Authorization"))

			var reqBody openAIVerifierRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
			assert.Equal(t, "verifier-model", reqBody.Model)

			verifierSuccessHandler("entailed", 0.9, "The verifier endpoint was used.")(w, r)
		}))
		defer srv.Close()

		cfg := &config.Config{
			AIAPIURL:         "https://embedding.example.com/v1",
			AIAPIKey:         "embedding-key",
			AIVerifierAPIURL: srv.URL,
			AIVerifierAPIKey: "verifier-key",
			AIReviewerModel:  "reviewer-model",
			AIVerifierModel:  "verifier-model",
		}
		v := NewOpenAIVerifier(cfg, srv.Client())

		got, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})

		require.NoError(t, err)
		assert.Equal(t, "entailed", got.Verdict)
	})

	t.Run("IncludesTemporalScopeWhenProvided", func(t *testing.T) {
		validFrom := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
		validTo := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody openAIVerifierRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
			require.Len(t, reqBody.Messages, 2)
			var userPayload map[string]any
			require.NoError(t, json.Unmarshal([]byte(reqBody.Messages[1].Content), &userPayload))
			assert.Equal(t, "service TIER-W-001 owner uses owner-coral", userPayload["claim"])
			assert.Equal(t, "2026-06-20T00:00:00Z", userPayload["valid_from"])
			assert.Equal(t, "2026-06-27T00:00:00Z", userPayload["valid_to"])

			verifierSuccessHandler("entailed", 0.9, "Temporal scope was included.")(w, r)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())

		got, err := v.Verify(context.Background(), Request{
			ProfileID: "p",
			Predicate: "service TIER-W-001 owner uses owner-coral",
			Context:   "Since 2026-06-20, service TIER-W-001 owner uses owner-coral.",
			ValidFrom: &validFrom,
			ValidTo:   &validTo,
		})

		require.NoError(t, err)
		assert.Equal(t, "entailed", got.Verdict)
	})

	t.Run("DisableTemperatureOmitsField", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var raw map[string]json.RawMessage
			require.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
			if _, ok := raw["temperature"]; ok {
				t.Fatal("temperature field was present, want omitted")
			}

			verifierSuccessHandler("entailed", 0.9, "Temperature was omitted.")(w, r)
		}))
		defer srv.Close()

		cfg := newTestVerifierConfig(srv.URL, "k", "m")
		cfg.AIVerifierDisableTemperature = true
		v := NewOpenAIVerifier(cfg, srv.Client())

		got, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})

		require.NoError(t, err)
		assert.Equal(t, "entailed", got.Verdict)
	})

	t.Run("HappyPath_Contradicted", func(t *testing.T) {
		srv := httptest.NewServer(verifierSuccessHandler("contradicted", 0.8, "Evidence contradicts the claim."))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		got, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		require.NoError(t, err)
		assert.Equal(t, "contradicted", got.Verdict)
	})

	t.Run("HappyPath_Insufficient", func(t *testing.T) {
		srv := httptest.NewServer(verifierSuccessHandler("insufficient", 0.3, "Not enough evidence."))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		got, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		require.NoError(t, err)
		assert.Equal(t, "insufficient", got.Verdict)
	})

	t.Run("InvalidVerdict_ReturnsErrMalformed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload := `{"verdict":"unknown","confidence":0.5,"rationale":"Some reason"}`
			resp := map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": payload}}},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		assert.ErrorIs(t, err, ErrVerifierMalformedResponse)
	})

	t.Run("ConfidenceTooHigh_ReturnsErrMalformed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload := `{"verdict":"entailed","confidence":1.5,"rationale":"Some reason"}`
			resp := map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": payload}}},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		assert.ErrorIs(t, err, ErrVerifierMalformedResponse)
	})

	t.Run("ConfidenceNegative_ReturnsErrMalformed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload := `{"verdict":"entailed","confidence":-0.1,"rationale":"Some reason"}`
			resp := map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": payload}}},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		assert.ErrorIs(t, err, ErrVerifierMalformedResponse)
	})

	t.Run("EmptyRationale_ReturnsErrMalformed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload := `{"verdict":"entailed","confidence":0.8,"rationale":""}`
			resp := map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": payload}}},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		assert.ErrorIs(t, err, ErrVerifierMalformedResponse)
	})

	t.Run("WhitespaceOnlyRationale_ReturnsErrMalformed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload := `{"verdict":"entailed","confidence":0.8,"rationale":"   "}`
			resp := map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": payload}}},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		assert.ErrorIs(t, err, ErrVerifierMalformedResponse)
	})

	t.Run("NoChoices_ReturnsErrMalformed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		assert.ErrorIs(t, err, ErrVerifierMalformedResponse)
	})

	t.Run("RateLimitResponse_ReturnsErrRateLimit", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "rate limit exceeded"},
			})
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		assert.ErrorIs(t, err, ErrVerifierRateLimit)
	})

	t.Run("ServerError5xx_ReturnsErrProvider", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "internal server error"},
			})
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		assert.ErrorIs(t, err, ErrVerifierProvider)
	})

	t.Run("EvidenceTruncatedToMaxItemChars", func(t *testing.T) {
		var capturedBody openAIVerifierRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			verifierSuccessHandler("insufficient", 0.3, "Not enough.")(w, r)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())

		longContext := strings.Repeat("a", openAIVerifierMaxItemChars+500)
		_, err := v.Verify(context.Background(), Request{
			ProfileID: "p",
			Predicate: "claim",
			Context:   longContext,
		})
		require.NoError(t, err)

		require.Len(t, capturedBody.Messages, 2)
		var userPayload struct {
			Claim    string   `json:"claim"`
			Evidence []string `json:"evidence"`
		}
		require.NoError(t, json.Unmarshal([]byte(capturedBody.Messages[1].Content), &userPayload))
		require.Len(t, userPayload.Evidence, 1)
		assert.LessOrEqual(t, len(userPayload.Evidence[0]), openAIVerifierMaxItemChars)
	})

	t.Run("ControlCharsStripped", func(t *testing.T) {
		var capturedBody openAIVerifierRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			verifierSuccessHandler("entailed", 0.9, "Supported.")(w, r)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())

		// \x00 and \x01 are control characters that should be stripped.
		// \t and \n should be preserved.
		contextWithControls := "valid\x00text\x01with\tnewline\ncontent"
		_, err := v.Verify(context.Background(), Request{
			ProfileID: "p",
			Predicate: "claim",
			Context:   contextWithControls,
		})
		require.NoError(t, err)

		var userPayload struct {
			Evidence []string `json:"evidence"`
		}
		require.NoError(t, json.Unmarshal([]byte(capturedBody.Messages[1].Content), &userPayload))
		require.Len(t, userPayload.Evidence, 1)
		assert.NotContains(t, userPayload.Evidence[0], "\x00")
		assert.NotContains(t, userPayload.Evidence[0], "\x01")
		assert.Contains(t, userPayload.Evidence[0], "\t")
		assert.Contains(t, userPayload.Evidence[0], "\n")
	})

	t.Run("EmptyContext_EmptyEvidenceList", func(t *testing.T) {
		var capturedBody openAIVerifierRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			verifierSuccessHandler("insufficient", 0.2, "No evidence provided.")(w, r)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		require.NoError(t, err)

		var userPayload struct {
			Evidence []string `json:"evidence"`
		}
		require.NoError(t, json.Unmarshal([]byte(capturedBody.Messages[1].Content), &userPayload))
		assert.Empty(t, userPayload.Evidence)
	})

	t.Run("TrailingSlashInBaseURL", func(t *testing.T) {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			verifierSuccessHandler("entailed", 0.9, "OK")(w, r)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL+"/", "k", "m"), srv.Client())
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		require.NoError(t, err)
		assert.Equal(t, "/chat/completions", gotPath)
	})

	t.Run("NilHTTPClient_UsesDefault", func(t *testing.T) {
		cfg := newTestVerifierConfig("https://api.example.com", "k", "m")
		v := NewOpenAIVerifier(cfg, nil)
		assert.NotNil(t, v.httpClient)
	})

	t.Run("ImplementsVerifierInterface", func(t *testing.T) {
		var _ Verifier = (*OpenAIVerifier)(nil)
		assert.True(t, true, "compile-time interface assertion passed")
	})
}

func TestOpenAIVerifierSemanticAdapters(t *testing.T) {
	t.Run("ProposeSemantic", func(t *testing.T) {
		content := "Dense-Mem uses PostgreSQL."
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody openAIVerifierRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
			assert.Equal(t, "reviewer-model", reqBody.Model)
			assert.Equal(t, ProviderProposalSchemaName, reqBody.ResponseFormat.JSONSchema.Name)
			require.Len(t, reqBody.Messages, 2)
			assert.Contains(t, reqBody.Messages[0].Content, "structure extraction")

			proposal := map[string]any{
				"predicate_options": []string{"uses"},
				"entity_proposals": []map[string]any{
					{
						"ref":         "project_1",
						"name":        "Dense-Mem",
						"entity_kind": "project",
						"evidence":    []map[string]any{{"evidence_index": 0, "start": 0, "end": len([]rune("Dense-Mem"))}},
					},
					{
						"ref":         "db_1",
						"name":        "PostgreSQL",
						"entity_kind": "project",
						"evidence": []map[string]any{{
							"evidence_index": 0,
							"start":          len([]rune("Dense-Mem uses ")),
							"end":            len([]rune("Dense-Mem uses PostgreSQL")),
						}},
					},
				},
				"relationship_proposals": []map[string]any{{
					"proposal_id":        "rel:uses",
					"subject_ref":        "project_1",
					"original_predicate": "uses",
					"object_ref":         "db_1",
					"evidence":           []map[string]any{{"evidence_index": 0, "start": 0, "end": len([]rune(content))}},
				}},
			}
			writeStructuredChatContent(t, w, proposal)
		}))
		defer srv.Close()

		cfg := newTestVerifierConfig(srv.URL, "sk-test", "verifier-model")
		cfg.AIReviewerModel = "reviewer-model"
		v := NewOpenAIVerifier(cfg, srv.Client())
		got, err := v.ProposeSemantic(context.Background(), ProviderProposalRequest{
			RequestID:        "extract-1",
			PredicateOptions: []string{"uses"},
			Evidence: []SemanticReviewEvidence{{
				EvidenceID:    "evidence:0",
				EvidenceIndex: 0,
				Content:       content,
			}},
		})
		require.NoError(t, err)
		require.Len(t, got.RelationshipProposals, 1)
		assert.Equal(t, "rel:uses", got.RelationshipProposals[0].ProposalID)
	})

	t.Run("ReviewSemantic", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody openAIVerifierRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
			assert.Equal(t, "verifier-model", reqBody.Model)
			assert.Equal(t, VerifierResponseSchemaName, reqBody.ResponseFormat.JSONSchema.Name)
			assert.Contains(t, reqBody.Messages[0].Content, "semantic verifier")
			assert.Contains(t, reqBody.Messages[0].Content, `actions "create" and "ambiguous" require candidate_entity_id to be null`)
			var userPayload map[string]any
			require.NoError(t, json.Unmarshal([]byte(reqBody.Messages[1].Content), &userPayload))
			assert.Equal(t, float64(2), userPayload["attempt"])
			assert.Equal(t, []any{"entity_results[0].candidate_entity_id: must be null"}, userPayload["validation_feedback"])
			assert.Equal(t, "sha256:previous", userPayload["previous_response_hash"])
			assert.NotContains(t, userPayload, "team_id")
			assert.NotContains(t, userPayload, "owner_profile_id")
			predicateKey := "uses"
			writeStructuredChatContent(t, w, map[string]any{
				"request_id":       "verify-1",
				"security_signals": []any{},
				"entity_results": []map[string]any{
					{"ref": "project_1", "action": "create", "candidate_entity_id": nil, "confidence": 0.9, "rationale": "Evidence names the project."},
					{"ref": "db_1", "action": "create", "candidate_entity_id": nil, "confidence": 0.9, "rationale": "Evidence names the database."},
				},
				"relationship_results": []map[string]any{{
					"ref":              "rel:uses",
					"predicate_status": "resolved",
					"predicate_key":    predicateKey,
					"evidence_verdict": "entailed",
					"confidence":       0.9,
					"rationale":        "The evidence states the relationship.",
				}},
			})
		}))
		defer srv.Close()

		content := "Dense-Mem uses PostgreSQL."
		cfg := newTestVerifierConfig(srv.URL, "sk-test", "verifier-model")
		cfg.AIReviewerModel = "reviewer-model"
		v := NewOpenAIVerifier(cfg, srv.Client())
		got, err := v.ReviewSemantic(context.Background(), SemanticReviewRequest{
			RequestID:            "verify-1",
			TeamID:               "team-a",
			OwnerProfileID:       "profile-a",
			Attempt:              2,
			ValidationFeedback:   []string{"entity_results[0].candidate_entity_id: must be null"},
			PreviousResponseHash: "sha256:previous",
			Evidence: []SemanticReviewEvidence{{
				EvidenceID: "evidence:0",
				Content:    content,
			}},
			EntityMentions: []SemanticEntityMention{
				{Ref: "project_1", Surface: "Dense-Mem", Kind: "project", EvidenceID: "evidence:0", Start: 0, End: len([]rune("Dense-Mem"))},
				{Ref: "db_1", Surface: "PostgreSQL", Kind: "project", EvidenceID: "evidence:0", Start: len([]rune("Dense-Mem uses ")), End: len([]rune("Dense-Mem uses PostgreSQL"))},
			},
			RelationshipObservations: []SemanticRelationshipObservation{{
				Ref:               "rel:uses",
				SubjectRef:        "project_1",
				OriginalPredicate: "uses",
				PredicateCandidates: []SemanticPredicateCandidate{{
					PredicateKey:        "uses",
					Version:             1,
					AllowedSubjectKinds: []string{"project"},
					AllowedObjectKinds:  []string{"project"},
				}},
				ObjectRef:  "db_1",
				EvidenceID: "evidence:0",
				Quote:      content,
				Start:      0,
				End:        len([]rune(content)),
			}},
		})
		require.NoError(t, err)
		require.Len(t, got.RelationshipResults, 1)
		assert.Equal(t, "rel:uses", got.RelationshipResults[0].Ref)
	})
}

func TestOpenAIVerifierSemanticAdapterErrors(t *testing.T) {
	validProposalRequest := func() ProviderProposalRequest {
		return ProviderProposalRequest{
			RequestID:        "extract-1",
			PredicateOptions: []string{"uses"},
			Evidence: []SemanticReviewEvidence{{
				EvidenceID:    "evidence:0",
				EvidenceIndex: 0,
				Content:       "Dense-Mem uses PostgreSQL.",
			}},
		}
	}
	validReviewRequest := func() SemanticReviewRequest {
		content := "Dense-Mem uses PostgreSQL."
		return SemanticReviewRequest{
			RequestID: "verify-1",
			TeamID:    "team-a",
			Evidence: []SemanticReviewEvidence{{
				EvidenceID: "evidence:0",
				Content:    content,
			}},
			EntityMentions: []SemanticEntityMention{
				{Ref: "project_1", Surface: "Dense-Mem", Kind: "project", EvidenceID: "evidence:0", Start: 0, End: len([]rune("Dense-Mem"))},
				{Ref: "db_1", Surface: "PostgreSQL", Kind: "project", EvidenceID: "evidence:0", Start: len([]rune("Dense-Mem uses ")), End: len([]rune("Dense-Mem uses PostgreSQL"))},
			},
			RelationshipObservations: []SemanticRelationshipObservation{{
				Ref:               "rel:uses",
				SubjectRef:        "project_1",
				OriginalPredicate: "uses",
				PredicateCandidates: []SemanticPredicateCandidate{{
					PredicateKey:        "uses",
					Version:             1,
					AllowedSubjectKinds: []string{"project"},
					AllowedObjectKinds:  []string{"project"},
				}},
				ObjectRef:  "db_1",
				EvidenceID: "evidence:0",
				Quote:      content,
				Start:      0,
				End:        len([]rune(content)),
			}},
		}
	}

	t.Run("invalid proposal request", func(t *testing.T) {
		v := NewOpenAIVerifier(newTestVerifierConfig("https://example.com/v1", "sk-test", "gpt-4o-mini"), nil)
		_, err := v.ProposeSemantic(context.Background(), ProviderProposalRequest{})
		var providerErr *ProviderError
		require.ErrorAs(t, err, &providerErr)
		assert.Contains(t, providerErr.Message, "invalid provider proposal request")
	})

	t.Run("invalid review request", func(t *testing.T) {
		v := NewOpenAIVerifier(newTestVerifierConfig("https://example.com/v1", "sk-test", "gpt-4o-mini"), nil)
		_, err := v.ReviewSemantic(context.Background(), SemanticReviewRequest{})
		var providerErr *ProviderError
		require.ErrorAs(t, err, &providerErr)
		assert.Contains(t, providerErr.Message, "invalid semantic review request")
	})

	t.Run("no choices", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{}}))
		}))
		defer srv.Close()
		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "sk-test", "gpt-4o-mini"), srv.Client())

		_, err := v.ProposeSemantic(context.Background(), validProposalRequest())

		var malformed *MalformedResponseError
		require.ErrorAs(t, err, &malformed)
		assert.Contains(t, malformed.Message, "no choices")
	})

	t.Run("malformed proposal content", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": "not-json"}}},
			}))
		}))
		defer srv.Close()
		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "sk-test", "gpt-4o-mini"), srv.Client())

		_, err := v.ProposeSemantic(context.Background(), validProposalRequest())

		var malformed *MalformedResponseError
		require.ErrorAs(t, err, &malformed)
		assert.Contains(t, malformed.Message, "failed to parse provider proposal response")
	})

	t.Run("provider status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "upstream unavailable"},
			}))
		}))
		defer srv.Close()
		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "sk-test", "gpt-4o-mini"), srv.Client())

		_, err := v.ReviewSemantic(context.Background(), validReviewRequest())

		var providerErr *ProviderError
		require.ErrorAs(t, err, &providerErr)
		assert.Equal(t, "upstream unavailable", providerErr.Message)
	})

	t.Run("rate limited", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "slow down"},
			}))
		}))
		defer srv.Close()
		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "sk-test", "gpt-4o-mini"), srv.Client())

		_, err := v.ReviewSemantic(context.Background(), validReviewRequest())

		var rateLimited *RateLimitError
		require.ErrorAs(t, err, &rateLimited)
		assert.Equal(t, "slow down", rateLimited.Message)
	})
}

// TestOpenAIVerifier_CrossProfileIsolation verifies that successive Verify calls
// for different profiles produce independent results with no cross-contamination,
// as required by AGENTS.md.
func TestOpenAIVerifier_CrossProfileIsolation(t *testing.T) {
	const profileA = "profile-A"
	const profileB = "profile-B"

	// The test server echoes back a verdict that encodes which profile's claim
	// it received. This lets us assert that profile B never receives profile A's data.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody openAIVerifierRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)

		// Extract the claim from the user message.
		var userPayload struct {
			Claim string `json:"claim"`
		}
		if len(reqBody.Messages) >= 2 {
			_ = json.Unmarshal([]byte(reqBody.Messages[1].Content), &userPayload)
		}

		// Encode which profile was seen into the rationale so the test can assert on it.
		var verdict, rationale string
		switch {
		case strings.Contains(userPayload.Claim, profileA):
			verdict = "entailed"
			rationale = "Claim belongs to " + profileA
		case strings.Contains(userPayload.Claim, profileB):
			verdict = "insufficient"
			rationale = "Claim belongs to " + profileB
		default:
			verdict = "insufficient"
			rationale = "Unknown profile"
		}

		payload := fmt.Sprintf(`{"verdict":%q,"confidence":0.9,"rationale":%q}`, verdict, rationale)
		resp := map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": payload}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "k", "m"), srv.Client())

	// Verify for profile A.
	respA, err := v.Verify(context.Background(), Request{
		ProfileID: profileA,
		Predicate: "claim for " + profileA,
	})
	require.NoError(t, err)
	assert.Equal(t, "entailed", respA.Verdict)
	assert.Contains(t, respA.Reasoning, profileA)

	// Verify for profile B — must receive its own independent result.
	respB, err := v.Verify(context.Background(), Request{
		ProfileID: profileB,
		Predicate: "claim for " + profileB,
	})
	require.NoError(t, err)
	assert.Equal(t, "insufficient", respB.Verdict)
	assert.Contains(t, respB.Reasoning, profileB)

	// Profile B's result must not contain profile A's data.
	assert.NotContains(t, respB.RawJSON, profileA,
		"profile B result must not leak profile A data")
	assert.NotEqual(t, respA.Verdict, respB.Verdict,
		"profiles must produce independent verdicts")
}

func TestOpenAIVerifierSharesConcurrencyGateAcrossOperations(t *testing.T) {
	var active atomic.Int64
	var maximum atomic.Int64
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		verifierSuccessHandler("entailed", 0.9, "supported")(w, r)
	}))
	defer srv.Close()

	cfg := newTestVerifierConfig(srv.URL, "key", "model")
	cfg.AIVerifierMaxConcurrency = 2
	v := NewOpenAIVerifier(cfg, srv.Client())

	errs := make(chan error, 3)
	var calls sync.WaitGroup
	calls.Add(3)
	go func() {
		defer calls.Done()
		_, err := v.Verify(context.Background(), Request{ProfileID: "p", Predicate: "claim"})
		errs <- err
	}()
	for range 2 {
		go func() {
			defer calls.Done()
			_, err := v.openAIStructuredChatJSON(
				context.Background(),
				"model",
				"test",
				map[string]any{"type": "object"},
				"system",
				map[string]string{"claim": "claim"},
			)
			errs <- err
		}()
	}

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for requests to enter the verifier")
		}
	}
	select {
	case <-started:
		t.Fatal("third request entered before a verifier slot was released")
	case <-time.After(100 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })
	calls.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int64(2), maximum.Load())
}
