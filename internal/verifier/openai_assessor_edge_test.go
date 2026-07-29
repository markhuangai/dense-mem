package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/observability"
)

type assessorRoundTripper func(*http.Request) (*http.Response, error)

func (f assessorRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type assessorErrorReader struct{}

func (assessorErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestOpenAIVerifierAssessSemanticUsesOneTurnForValidResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		if _, exists := raw["max_tokens"]; exists {
			t.Fatal("assessor request included max_tokens")
		}
		if _, exists := raw["max_completion_tokens"]; exists {
			t.Fatal("assessor request included max_completion_tokens")
		}
		var request openAIVerifierRequest
		require.NoError(t, json.Unmarshal(mustMarshalJSON(t, raw), &request))
		assert.Equal(t, "assessor-model", request.Model)
		assert.Equal(t, SemanticAssessmentSchemaName, request.ResponseFormat.JSONSchema.Name)
		assert.Contains(t, request.Messages[0].Content, "integrated structure and support assessor")
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(request.Messages[1].Content), &payload))
		assert.NotContains(t, payload, "team_id")
		assert.NotContains(t, payload, "owner_profile_id")

		content, err := json.Marshal(semanticAssessmentTestResponse())
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
			"usage":   map[string]any{"prompt_tokens": 200, "completion_tokens": 100, "total_tokens": 300},
		}))
	}))
	defer srv.Close()

	cfg := newTestVerifierConfig(srv.URL, "sk-test", "assessor-model")
	cfg.AIReviewerModel = ""
	v := NewOpenAIVerifier(cfg, srv.Client())
	req, _ := semanticAssessmentTestRequest(t)
	response, err := v.AssessSemantic(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "assess-1", response.RequestID)
	require.Len(t, response.RelationshipResults, 1)
	assert.Equal(t, "entailed", response.RelationshipResults[0].EvidenceVerdict)
	assert.Equal(t, 1, response.ProviderTurns)
	assert.Equal(t, 200, response.InputTokens)
}

func TestOpenAIVerifierAssessSemanticCorrectsMalformedContentInSameHistory(t *testing.T) {
	var requests []openAIVerifierRequest
	var invalidContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests = append(requests, request)

		response := semanticAssessmentTestResponse()
		if len(requests) == 1 {
			response.RequestID = "wrong-request"
		}
		content := string(mustMarshalJSON(t, response))
		if len(requests) == 1 {
			invalidContent = content
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		}))
	}))
	defer srv.Close()

	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	v.SetMetrics(metrics)
	req, _ := semanticAssessmentTestRequest(t)
	response, err := v.AssessSemantic(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, 2, response.ProviderTurns)
	assert.Greater(t, response.InputTokens, req.InputTokens)
	assert.Equal(t, []string{"system", "user"}, assessmentMessageRoles(requests[0].Messages))
	assert.Equal(t, []string{"system", "user", "assistant", "user"}, assessmentMessageRoles(requests[1].Messages))
	assert.Equal(t, invalidContent, requests[1].Messages[2].Content)

	var correction semanticAssessmentCorrection
	require.NoError(t, json.Unmarshal([]byte(requests[1].Messages[3].Content), &correction))
	require.Len(t, correction.ValidationErrors, 1)
	assert.Equal(t, "request_id", correction.ValidationErrors[0].Field)
	assert.Contains(t, correction.ValidationErrors[0].Message, `expected "assess-1"`)
	assert.Contains(t, correction.Instruction, "complete replacement JSON object")
	assert.Equal(t, 1, metrics.AssessorValidationFailureCount("response_contract"))
}

func TestOpenAIVerifierAssessSemanticStopsConversationOnProviderFailure(t *testing.T) {
	var requests []openAIVerifierRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests = append(requests, request)
		if len(requests) == 1 {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": "not-json"}}},
			}))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, err := w.Write([]byte("provider-private-body"))
		require.NoError(t, err)
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	req, _ := semanticAssessmentTestRequest(t)
	_, err := v.AssessSemantic(context.Background(), req)
	require.ErrorIs(t, err, ErrVerifierProvider)
	var provider *ProviderError
	require.ErrorAs(t, err, &provider)
	assert.Equal(t, ProviderFailureClassHTTPServer, provider.FailureClass)
	assert.Equal(t, http.StatusServiceUnavailable, provider.StatusCode)
	assert.NotContains(t, err.Error(), "provider-private-body")
	require.Len(t, requests, 2)
	assert.Equal(t, []string{"system", "user", "assistant", "user"}, assessmentMessageRoles(requests[1].Messages))
}

func assessmentMessageRoles(messages []openAIVerifierMessage) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	return roles
}

func TestSemanticAssessmentResponseForCorrectionClassifiesLocalFailures(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, validationErrors := PrepareSemanticAssessmentRequest(req, limits)
	require.Empty(t, validationErrors)
	validContent := string(mustMarshalJSON(t, semanticAssessmentTestResponse()))

	t.Run("local output budget", func(t *testing.T) {
		limited := limits
		limited.MaxOutputTokens = 1
		_, errs, stage := semanticAssessmentResponseForCorrection(
			prepared,
			openAIStructuredChatResult{Content: validContent},
			limited,
		)
		require.NotEmpty(t, errs)
		assert.Equal(t, "response_output_tokens", stage)
		assert.Equal(t, "output_tokens", errs[0].Field)
	})

	t.Run("json type", func(t *testing.T) {
		content := strings.Replace(validContent, `"request_id":"assess-1"`, `"request_id":123`, 1)
		_, errs, stage := semanticAssessmentResponseForCorrection(
			prepared,
			openAIStructuredChatResult{Content: content},
			limits,
		)
		require.NotEmpty(t, errs)
		assert.Equal(t, "response_json", stage)
		assert.Equal(t, "request_id", errs[0].Field)
		assert.Contains(t, errs[0].Message, "required JSON type")
	})

	t.Run("tokenizer", func(t *testing.T) {
		unsupported := limits
		unsupported.Tokenizer = "unsupported"
		_, errs, stage := semanticAssessmentResponseForCorrection(
			prepared,
			openAIStructuredChatResult{Content: validContent},
			unsupported,
		)
		require.NotEmpty(t, errs)
		assert.Equal(t, "response_json", stage)
		assert.Equal(t, "response", errs[0].Field)
	})

	assert.Equal(t, ProviderFailureClassHTTPUnexpected, openAIHTTPFailureClass(http.StatusFound))
}

func TestOpenAIVerifierAssessSemanticRejectsProviderBoundaries(t *testing.T) {
	testCases := []struct {
		name         string
		handler      http.HandlerFunc
		wantType     any
		wantDetail   string
		wantCalls    int
		wantAttempts int
		wantClass    string
		wantStatus   int
		forbidDetail string
	}{
		{
			name: "reported input token overage",
			handler: assessorResponseHandler(t, semanticAssessmentTestResponse(), &openAIVerifierUsage{
				PromptTokens: int64(DefaultSemanticAssessmentLimits().MaxInputTokens + 1),
			}),
			wantType:     &MalformedResponseError{},
			wantDetail:   "input tokens beyond semantic assessment limit",
			wantCalls:    1,
			wantAttempts: 1,
			wantClass:    "input_budget",
		},
		{
			name: "reported output token overage",
			handler: assessorResponseHandler(t, semanticAssessmentTestResponse(), &openAIVerifierUsage{
				CompletionTokens: int64(DefaultSemanticAssessmentLimits().MaxOutputTokens + 1),
			}),
			wantType:     &MalformedResponseError{},
			wantDetail:   "remained invalid after bounded correction",
			wantCalls:    SemanticAssessmentMaxProviderTurns,
			wantAttempts: SemanticAssessmentMaxProviderTurns,
			wantClass:    "malformed_exhausted",
		},
		{
			name:         "invalid structured content",
			handler:      assessorRawResponseHandler(t, "not-json", nil),
			wantType:     &MalformedResponseError{},
			wantDetail:   "remained invalid after bounded correction",
			wantCalls:    SemanticAssessmentMaxProviderTurns,
			wantAttempts: SemanticAssessmentMaxProviderTurns,
			wantClass:    "malformed_exhausted",
		},
		{
			name: "request dependent invalid response",
			handler: func() http.HandlerFunc {
				response := semanticAssessmentTestResponse()
				response.RequestID = "other-request"
				return assessorResponseHandler(t, response, nil)
			}(),
			wantType:     &MalformedResponseError{},
			wantDetail:   "remained invalid after bounded correction",
			wantCalls:    SemanticAssessmentMaxProviderTurns,
			wantAttempts: SemanticAssessmentMaxProviderTurns,
			wantClass:    "malformed_exhausted",
		},
		{
			name: "rate limited",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "slow down"}}))
			},
			wantType:     &RateLimitError{},
			wantDetail:   "provider returned HTTP 429",
			wantCalls:    1,
			forbidDetail: "slow down",
		},
		{
			name: "provider status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "upstream unavailable"}}))
			},
			wantType:     &ProviderError{},
			wantDetail:   "provider returned HTTP 502",
			wantCalls:    1,
			wantClass:    ProviderFailureClassHTTPServer,
			wantStatus:   http.StatusBadGateway,
			forbidDetail: "upstream unavailable",
		},
		{
			name: "bad request",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "do not expose me"}}))
			},
			wantType:     &ProviderError{},
			wantDetail:   "provider returned HTTP 400",
			wantCalls:    1,
			wantClass:    ProviderFailureClassHTTPClient,
			wantStatus:   http.StatusBadRequest,
			forbidDetail: "do not expose me",
		},
		{
			name: "unauthorized",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "secret diagnostic"}}))
			},
			wantType:     &ProviderError{},
			wantDetail:   "provider returned HTTP 401",
			wantCalls:    1,
			wantClass:    ProviderFailureClassHTTPClient,
			wantStatus:   http.StatusUnauthorized,
			forbidDetail: "secret diagnostic",
		},
		{
			name: "empty choices",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{}}))
			},
			wantType:   &ProviderError{},
			wantDetail: "no choices",
			wantCalls:  1,
			wantClass:  ProviderFailureClassProtocol,
		},
		{
			name: "invalid outer envelope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, err := io.WriteString(w, "not-json")
				require.NoError(t, err)
			},
			wantType:   &ProviderError{},
			wantDetail: "invalid provider response envelope",
			wantCalls:  1,
			wantClass:  ProviderFailureClassProtocol,
		},
		{
			name: "non-json service unavailable",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, err := io.WriteString(w, "not-json")
				require.NoError(t, err)
			},
			wantType:   &ProviderError{},
			wantDetail: "provider returned HTTP 503",
			wantCalls:  1,
			wantClass:  ProviderFailureClassHTTPServer,
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				testCase.handler(w, r)
			}))
			defer srv.Close()
			v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
			req, _ := semanticAssessmentTestRequest(t)
			_, err := v.AssessSemantic(context.Background(), req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantDetail)
			if testCase.forbidDetail != "" {
				assert.NotContains(t, err.Error(), testCase.forbidDetail)
			}
			assert.Equal(t, testCase.wantCalls, calls)
			switch testCase.wantType.(type) {
			case *MalformedResponseError:
				var malformed *MalformedResponseError
				require.ErrorAs(t, err, &malformed)
				assert.Equal(t, testCase.wantAttempts, malformed.Attempts)
				assert.Equal(t, testCase.wantClass, malformed.FailureClass)
			case *RateLimitError:
				var rateLimited *RateLimitError
				require.ErrorAs(t, err, &rateLimited)
			case *ProviderError:
				var provider *ProviderError
				require.ErrorAs(t, err, &provider)
				assert.Equal(t, testCase.wantClass, provider.FailureClass)
				assert.Equal(t, testCase.wantStatus, provider.StatusCode)
			default:
				t.Fatalf("unsupported expected error type %T", testCase.wantType)
			}
		})
	}
}

func TestOpenAIVerifierAssessSemanticRejectsInvalidRequestBeforeProviderCall(t *testing.T) {
	v := NewOpenAIVerifier(newTestVerifierConfig("https://example.invalid", "key", "assessor-model"), nil)
	_, err := v.AssessSemantic(context.Background(), SemanticAssessmentRequest{})
	var provider *ProviderError
	require.ErrorAs(t, err, &provider)
	assert.Contains(t, provider.Message, "invalid semantic assessment request")
}

func TestOpenAIVerifierWithUsageSurfacesMarshalRequestAndTransportFailures(t *testing.T) {
	v := NewOpenAIVerifier(newTestVerifierConfig("https://example.invalid", "key", "assessor-model"), nil)

	_, err := v.openAIStructuredChatJSONWithUsage(context.Background(), "model", "schema", map[string]any{"invalid": func() {}}, "system", map[string]any{})
	var provider *ProviderError
	require.ErrorAs(t, err, &provider)
	assert.Contains(t, provider.Message, "failed to marshal structured schema")

	_, err = v.openAIStructuredChatJSONWithUsage(context.Background(), "model", "schema", map[string]any{}, "system", func() {})
	require.ErrorAs(t, err, &provider)
	assert.Contains(t, provider.Message, "failed to marshal user payload")

	v.baseURL = "://invalid"
	_, err = v.openAIStructuredChatJSONWithUsage(context.Background(), "model", "schema", map[string]any{}, "system", map[string]any{})
	require.ErrorAs(t, err, &provider)
	assert.Contains(t, provider.Message, "failed to create HTTP request")

	v = NewOpenAIVerifier(newTestVerifierConfig("https://example.invalid", "key", "assessor-model"), &http.Client{
		Transport: assessorRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		}),
	})
	_, err = v.openAIStructuredChatJSONWithUsage(context.Background(), "model", "schema", map[string]any{}, "system", map[string]any{})
	require.ErrorAs(t, err, &provider)
	assert.Contains(t, provider.Message, "HTTP request failed")
	assert.NotContains(t, err.Error(), "dial failed")

	v = NewOpenAIVerifier(newTestVerifierConfig("https://example.invalid", "key", "assessor-model"), &http.Client{
		Transport: assessorRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	})
	_, err = v.openAIStructuredChatJSONWithUsage(context.Background(), "model", "schema", map[string]any{}, "system", map[string]any{})
	var timeout *TimeoutError
	require.ErrorAs(t, err, &timeout)

	v.sem = make(chan struct{}, 1)
	v.sem <- struct{}{}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err = v.acquire(canceled)
	require.ErrorAs(t, err, &timeout)
}

func TestDecodeOpenAIVerifierAPIResponseBoundsTransport(t *testing.T) {
	_, err := decodeOpenAIVerifierAPIResponse(assessorErrorReader{})
	require.ErrorContains(t, err, "read failed")

	_, err = decodeOpenAIVerifierAPIResponse(strings.NewReader("not-json"))
	require.Error(t, err)

	_, err = decodeOpenAIVerifierAPIResponse(strings.NewReader(strings.Repeat("x", openAIVerifierMaxResponseBytes+1)))
	require.ErrorContains(t, err, "transport limit")
}

func assessorResponseHandler(t *testing.T, response SemanticAssessmentResponse, usage *openAIVerifierUsage) http.HandlerFunc {
	t.Helper()
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	return assessorRawResponseHandler(t, string(encoded), usage)
}

func assessorRawResponseHandler(t *testing.T, content string, usage *openAIVerifierUsage) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": content}}}}
		if usage != nil {
			response["usage"] = usage
		}
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}
}
