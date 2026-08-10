package embedding

import (
	"context"
	"encoding/json"
	"io"
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

type truncatedResponseReader struct {
	done bool
}

func (r *truncatedResponseReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.ErrUnexpectedEOF
	}
	r.done = true
	return copy(p, `{"data":[`), io.ErrUnexpectedEOF
}

func (r *truncatedResponseReader) Close() error { return nil }

func TestOpenAIProvider_Embed_SendsBearerAuth(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "sk-123",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	vec, model, err := p.Embed(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, "Bearer sk-123", gotAuth)
	assert.Equal(t, "m", model)
	assert.Equal(t, []float32{0.1, 0.2}, vec)
	assert.NotContains(t, gotBody, "api_key", "API key must not appear in body")
}

func TestOpenAIProvider_EmbedBatch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Parse and verify request body
		var reqBody map[string]any
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		assert.Equal(t, "text-embedding-3-small", reqBody["model"])
		assert.Equal(t, float64(3), reqBody["dimensions"])

		input, ok := reqBody["input"].([]interface{})
		require.True(t, ok)
		assert.Len(t, input, 2)
		assert.Equal(t, "hello", input[0])
		assert.Equal(t, "world", input[1])

		// Return response with two embeddings
		resp := `{"data":[{"embedding":[0.1,0.2,0.3]},{"embedding":[0.4,0.5,0.6]}]}`
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "test-key",
		AIEmbeddingModel:          "text-embedding-3-small",
		AIEmbeddingDimensions:     3,
		AIEmbeddingTimeoutSeconds: 10,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	vecs, model, err := p.EmbedBatch(context.Background(), []string{"hello", "world"})
	require.NoError(t, err)
	assert.Equal(t, "text-embedding-3-small", model)
	require.Len(t, vecs, 2)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, vecs[0])
	assert.Equal(t, []float32{0.4, 0.5, 0.6}, vecs[1])
}

func TestOpenAIProviderRecordsProviderUsageBeforeRejectingInvalidResult(t *testing.T) {
	rate := 1_000_000.0
	metrics := observability.NewPrometheusMetrics(observability.AIPricingResolverFunc(func(context.Context) (observability.AIPricing, error) {
		return observability.AIPricing{EmbeddingInputUSDPerMillionTokens: &rate}, nil
	}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"data":  []any{map[string]any{"embedding": []float32{0.1, 0.2}}},
			"usage": map[string]any{"prompt_tokens": 12, "total_tokens": 12},
		}))
	}))
	defer srv.Close()

	p := NewOpenAIEmbeddingProvider(&config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "embedding-model",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}, srv.Client())
	p.SetMetrics(metrics)
	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationRecallEmbedding, 1)
	_, _, err := p.EmbedBatch(ctx, []string{"first", "second"})
	require.ErrorIs(t, err, ErrEmbeddingProvider)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "densemem_ai_operation_cost_usd_total{") {
			continue
		}
		assert.Contains(t, line, `operation="recall_embedding"`)
		assert.Contains(t, line, `component="embedding"`)
		assert.Contains(t, line, `model="embedding-model"`)
		assert.True(t, strings.HasSuffix(line, " 12"), "cost line = %q; want 12 USD", line)
		return
	}
	t.Fatal("provider usage did not produce an AI operation cost sample")
}

func TestOpenAIProviderUsesTotalTokensWhenPromptTokensAreOmitted(t *testing.T) {
	rate := 1_000_000.0
	metrics := observability.NewPrometheusMetrics(observability.AIPricingResolverFunc(func(context.Context) (observability.AIPricing, error) {
		return observability.AIPricing{EmbeddingInputUSDPerMillionTokens: &rate}, nil
	}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"data":  []any{map[string]any{"embedding": []float32{0.1, 0.2}}},
			"usage": map[string]any{"total_tokens": 12},
		}))
	}))
	defer srv.Close()

	p := NewOpenAIEmbeddingProvider(&config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "embedding-model",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}, srv.Client())
	p.SetMetrics(metrics)
	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationRecallEmbedding, 1)
	_, _, err := p.Embed(ctx, "total-only usage")
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "densemem_ai_operation_cost_usd_total{") {
			continue
		}
		assert.Contains(t, line, `operation="recall_embedding"`)
		assert.Contains(t, line, `component="embedding"`)
		assert.Contains(t, line, `model="embedding-model"`)
		assert.True(t, strings.HasSuffix(line, " 12"), "cost line = %q; want 12 USD", line)
		return
	}
	t.Fatal("total embedding tokens did not produce an AI operation cost sample")
}

func TestOpenAIProviderMarksMalformedSuccessfulResponseUnpriced(t *testing.T) {
	metrics := observability.NewPrometheusMetrics()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{"))
	}))
	defer srv.Close()

	p := NewOpenAIEmbeddingProvider(&config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "embedding-model",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}, srv.Client())
	p.SetMetrics(metrics)
	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationRecallEmbedding, 1)
	_, _, err := p.Embed(ctx, "malformed response")
	require.ErrorIs(t, err, ErrEmbeddingProvider)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, "densemem_ai_operation_unpriced_total{") &&
			strings.Contains(line, "operation=\"recall_embedding\"") &&
			strings.Contains(line, "component=\"embedding\"") &&
			strings.Contains(line, "model=\"embedding-model\"") &&
			strings.Contains(line, "reason=\"missing_usage\"") {
			assert.True(t, strings.HasSuffix(line, " 1"), "unpriced line = %q; want one missing-usage observation", line)
			return
		}
	}
	t.Fatal("malformed successful provider response did not produce an unpriced observation")
}

func TestOpenAIProviderDoesNotMarkMalformedProviderErrorUnpriced(t *testing.T) {
	metrics := observability.NewPrometheusMetrics()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("{"))
	}))
	defer srv.Close()

	p := NewOpenAIEmbeddingProvider(&config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "embedding-model",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}, srv.Client())
	p.SetMetrics(metrics)
	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationRecallEmbedding, 1)
	_, _, err := p.Embed(ctx, "malformed provider error")
	require.Error(t, err)
	require.Contains(t, err.Error(), "status=429")

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.NotContains(t, recorder.Body.String(), `reason="missing_usage"`)
}

func TestOpenAIProvider_Non200Response(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "bad-key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	_, _, err := p.Embed(context.Background(), "test")
	require.Error(t, err)
	var httpErr *ProviderHTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, 401, httpErr.Status)
	assert.Equal(t, "provider returned status 401", httpErr.Message)
	assert.NotContains(t, err.Error(), "Invalid API key")
}

func TestOpenAIProvider_NonJSONServerErrorIsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2.5")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`upstream unavailable`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	_, _, err := p.Embed(context.Background(), "test")
	require.Error(t, err)
	var httpErr *ProviderHTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadGateway, httpErr.Status)
	assert.Equal(t, "provider returned a non-JSON error response", httpErr.Message)
	assert.NotContains(t, err.Error(), "upstream unavailable")
	assert.Equal(t, 2500*time.Millisecond, httpErr.RetryAfter)
}

func TestOpenAIProvider_ClassifiesTruncatedResponseAsTransientNetworkFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &truncatedResponseReader{},
			Header:     make(http.Header),
		}, nil
	})}
	p := NewOpenAIEmbeddingProvider(&config.Config{
		AIAPIURL: "http://embedding.test", AIAPIKey: "key", AIEmbeddingModel: "m", AIEmbeddingDimensions: 2,
	}, client)

	_, _, err := p.Embed(context.Background(), "truncated")
	require.Error(t, err)
	metadata := ClassifyFailure(err)
	assert.Equal(t, "transient", metadata.Class)
	assert.Equal(t, "provider_network_error", metadata.Code)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestOpenAIProviderErrorHelpersBoundProviderMetadata(t *testing.T) {
	if got := nonJSONHTTPMessage([]byte("provider body")); got != "provider returned a non-JSON error response" {
		t.Fatalf("nonJSONHTTPMessage = %q", got)
	}
	if got := providerErrorCode(nil); got != "" {
		t.Fatalf("providerErrorCode(nil) = %q", got)
	}
	if got := providerErrorType(nil); got != "" {
		t.Fatalf("providerErrorType(nil) = %q", got)
	}
	value := struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}{Code: " quota ", Type: "type"}
	if got := providerErrorCode(&value); got != "quota" {
		t.Fatalf("providerErrorCode = %q", got)
	}
	if got := providerErrorType(&value); got != "type" {
		t.Fatalf("providerErrorType = %q", got)
	}
	if got := retryAfterDuration(""); got != 0 {
		t.Fatalf("retryAfterDuration(empty) = %s", got)
	}
	if got := retryAfterDuration("2.5"); got != 2500*time.Millisecond {
		t.Fatalf("retryAfterDuration(seconds) = %s", got)
	}
	if got := retryAfterDuration("invalid"); got != 0 {
		t.Fatalf("retryAfterDuration(invalid) = %s", got)
	}
}

func TestOpenAIProviderParsesAllowlistedFailureCodeAndTypeWithoutMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"billing secret detail","code":"insufficient_quota","type":"insufficient_quota","unknown":"discard"}}`))
	}))
	defer srv.Close()

	p := NewOpenAIEmbeddingProvider(&config.Config{
		AIAPIURL: srv.URL, AIAPIKey: "key", AIEmbeddingModel: "m", AIEmbeddingDimensions: 2,
	}, srv.Client())
	_, _, err := p.Embed(context.Background(), "test")
	var httpErr *ProviderHTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, "insufficient_quota", httpErr.Code)
	assert.Equal(t, "insufficient_quota", httpErr.Type)
	assert.NotContains(t, err.Error(), "billing secret detail")
	metadata := ClassifyFailure(err)
	assert.Equal(t, "provider_action_required", metadata.Class)
	assert.Equal(t, "provider_quota_exhausted", metadata.Code)
}

func TestOpenAIProvider_WrongDimensions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return embedding with wrong dimensions
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3,0.4]}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2, // Config says 2, but server returns 4
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	_, _, err := p.Embed(context.Background(), "test")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmbeddingProvider)
	assert.Contains(t, err.Error(), "expected 2 dimensions, got 4")
}

func TestOpenAIProvider_WrongNumberOfEmbeddings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return fewer embeddings than requested
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	_, _, err := p.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmbeddingProvider)
	assert.Contains(t, err.Error(), "expected 3 embeddings, got 1")
}

func TestOpenAIProvider_IsAvailable(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		key        string
		model      string
		dimensions int
		want       bool
	}{
		{
			name:       "all configured",
			url:        "https://api.example.com",
			key:        "key",
			model:      "model",
			dimensions: 1536,
			want:       true,
		},
		{
			name:       "missing URL",
			url:        "",
			key:        "key",
			model:      "model",
			dimensions: 1536,
			want:       false,
		},
		{
			name:       "missing key",
			url:        "https://api.example.com",
			key:        "",
			model:      "model",
			dimensions: 1536,
			want:       false,
		},
		{
			name:       "missing model",
			url:        "https://api.example.com",
			key:        "key",
			model:      "",
			dimensions: 1536,
			want:       false,
		},
		{
			name:       "zero dimensions",
			url:        "https://api.example.com",
			key:        "key",
			model:      "model",
			dimensions: 0,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				AIAPIURL:                  tt.url,
				AIAPIKey:                  tt.key,
				AIEmbeddingModel:          tt.model,
				AIEmbeddingDimensions:     tt.dimensions,
				AIEmbeddingTimeoutSeconds: 5,
			}
			p := NewOpenAIEmbeddingProvider(cfg, nil)
			assert.Equal(t, tt.want, p.IsAvailable())
		})
	}
}

func TestOpenAIProvider_ModelNameAndDimensions(t *testing.T) {
	cfg := &config.Config{
		AIAPIURL:                  "https://api.example.com",
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "text-embedding-3-small",
		AIEmbeddingDimensions:     1536,
		AIEmbeddingTimeoutSeconds: 30,
	}
	p := NewOpenAIEmbeddingProvider(cfg, nil)

	assert.Equal(t, "text-embedding-3-small", p.ModelName())
	assert.Equal(t, 1536, p.Dimensions())
}

func TestOpenAIProvider_DefaultTimeout(t *testing.T) {
	cfg := &config.Config{
		AIAPIURL:                  "https://api.example.com",
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 0, // Zero timeout should default to 30s
	}
	p := NewOpenAIEmbeddingProvider(cfg, nil)

	assert.Equal(t, 30*time.Second, p.timeout)
}

func TestOpenAIProvider_NilHTTPClient(t *testing.T) {
	cfg := &config.Config{
		AIAPIURL:                  "https://api.example.com",
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 10,
	}
	p := NewOpenAIEmbeddingProvider(cfg, nil)

	assert.NotNil(t, p.httpClient)
}

func TestOpenAIProvider_TrailingSlashInURL(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL + "/", // Trailing slash
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     1,
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	_, _, err := p.Embed(context.Background(), "test")
	require.NoError(t, err)
	assert.Equal(t, "/embeddings", gotURL)
}

func TestOpenAIProvider_ImplementsInterface(t *testing.T) {
	// Compile-time assertion that OpenAIEmbeddingProvider implements EmbeddingProviderInterface
	var _ EmbeddingProviderInterface = (*OpenAIEmbeddingProvider)(nil)
	assert.True(t, true, "compile-time interface assertion passed")
}

func TestOpenAIProvider_EmbedBatchCapsConcurrentRequests(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
		AIEmbeddingMaxConcurrency: 2,
	}
	provider := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	errs := make(chan error, 3)
	var calls sync.WaitGroup
	for range 3 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			_, _, err := provider.EmbedBatch(context.Background(), []string{"text"})
			errs <- err
		}()
	}

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for requests to enter the provider")
		}
	}
	select {
	case <-started:
		t.Fatal("third request entered before a provider slot was released")
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

func TestOpenAIProvider_EmbedBatchWaitHonorsContextCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
		AIEmbeddingMaxConcurrency: 1,
	}
	provider := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	firstDone := make(chan error, 1)
	go func() {
		_, _, err := provider.EmbedBatch(context.Background(), []string{"first"})
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the first request")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := provider.EmbedBatch(ctx, []string{"second"})
	require.ErrorIs(t, err, ErrEmbeddingTimeout)

	releaseOnce.Do(func() { close(release) })
	require.NoError(t, <-firstDone)
}
