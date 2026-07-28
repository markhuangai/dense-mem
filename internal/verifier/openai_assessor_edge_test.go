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
)

type assessorRoundTripper func(*http.Request) (*http.Response, error)

func (f assessorRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type assessorErrorReader struct{}

func (assessorErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestOpenAIVerifierAssessSemanticRejectsProviderBoundaries(t *testing.T) {
	testCases := []struct {
		name       string
		handler    http.HandlerFunc
		wantType   any
		wantDetail string
	}{
		{
			name: "reported input token overage",
			handler: assessorResponseHandler(t, semanticAssessmentTestResponse(), &openAIVerifierUsage{
				PromptTokens: int64(DefaultSemanticAssessmentLimits().MaxInputTokens + 1),
			}),
			wantType:   &MalformedResponseError{},
			wantDetail: "input tokens beyond semantic assessment limit",
		},
		{
			name: "reported output token overage",
			handler: assessorResponseHandler(t, semanticAssessmentTestResponse(), &openAIVerifierUsage{
				CompletionTokens: int64(DefaultSemanticAssessmentLimits().MaxOutputTokens + 1),
			}),
			wantType:   &MalformedResponseError{},
			wantDetail: "output tokens beyond semantic assessment limit",
		},
		{
			name:       "invalid structured content",
			handler:    assessorRawResponseHandler(t, "not-json", nil),
			wantType:   &MalformedResponseError{},
			wantDetail: "failed to parse semantic assessment response",
		},
		{
			name: "request dependent invalid response",
			handler: func() http.HandlerFunc {
				response := semanticAssessmentTestResponse()
				response.RequestID = "other-request"
				return assessorResponseHandler(t, response, nil)
			}(),
			wantType:   &MalformedResponseError{},
			wantDetail: "invalid semantic assessment response",
		},
		{
			name: "rate limited",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "slow down"}}))
			},
			wantType:   &RateLimitError{},
			wantDetail: "slow down",
		},
		{
			name: "provider status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "upstream unavailable"}}))
			},
			wantType:   &ProviderError{},
			wantDetail: "upstream unavailable",
		},
		{
			name: "empty choices",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{}}))
			},
			wantType:   &MalformedResponseError{},
			wantDetail: "no choices",
		},
		{
			name: "invalid outer envelope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, err := io.WriteString(w, "not-json")
				require.NoError(t, err)
			},
			wantType:   &MalformedResponseError{},
			wantDetail: "failed to decode API response",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			srv := httptest.NewServer(testCase.handler)
			defer srv.Close()
			v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
			req, _ := semanticAssessmentTestRequest(t)
			_, err := v.AssessSemantic(context.Background(), req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantDetail)
			switch testCase.wantType.(type) {
			case *MalformedResponseError:
				var malformed *MalformedResponseError
				require.ErrorAs(t, err, &malformed)
			case *RateLimitError:
				var rateLimited *RateLimitError
				require.ErrorAs(t, err, &rateLimited)
			case *ProviderError:
				var provider *ProviderError
				require.ErrorAs(t, err, &provider)
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

	v.sem = make(chan struct{}, 1)
	v.sem <- struct{}{}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err = v.acquire(canceled)
	var timeout *TimeoutError
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
