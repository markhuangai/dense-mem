package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/stretchr/testify/require"
)

type cloudflareMeasurementAttempt struct {
	Items          int    `json:"items"`
	HTTPStatus     int    `json:"http_status"`
	LatencyMS      int64  `json:"latency_ms"`
	Injected       bool   `json:"injected_failure"`
	ObservedTokens *int64 `json:"observed_input_tokens"`
}

type cloudflareMeasurementResult struct {
	Size              int                            `json:"documents"`
	Repetition        int                            `json:"repetition"`
	Policy            string                         `json:"retry_policy"`
	InjectedStatus    int                            `json:"injected_later_chunk_status"`
	LatencyMS         int64                          `json:"latency_ms"`
	ExceededTenSecond bool                           `json:"exceeded_10_seconds"`
	Succeeded         bool                           `json:"succeeded"`
	ReturnedVectors   int                            `json:"returned_vectors"`
	FailureClass      string                         `json:"failure_class,omitempty"`
	RetriedDocuments  int                            `json:"resent_documents"`
	Retries           int                            `json:"retries"`
	ObservedTokens    int64                          `json:"observed_input_tokens"`
	UsageAvailable    bool                           `json:"all_usage_available"`
	ComputedNeurons   *float64                       `json:"computed_neurons_from_observed_tokens"`
	Attempts          []cloudflareMeasurementAttempt `json:"http_attempts"`
}

type cloudflareMeasurementTransport struct {
	base     http.RoundTripper
	failure  int
	seen     map[string]bool
	resent   int
	chunks   map[string]bool
	retries  int
	attempts []cloudflareMeasurementAttempt
}

func (m *cloudflareMeasurementTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	input, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(input))
	var body openAIEmbeddingRequest
	if err := json.Unmarshal(input, &body); err != nil {
		return nil, err
	}
	if m.chunks == nil {
		m.chunks = map[string]bool{}
	}
	if m.chunks[string(input)] {
		m.retries++
	}
	m.chunks[string(input)] = true
	for _, text := range body.Input {
		if m.seen[text] {
			m.resent++
		}
		m.seen[text] = true
	}
	attempt := cloudflareMeasurementAttempt{Items: len(body.Input)}
	started := time.Now()
	if m.failure != 0 && len(m.attempts) == 1 {
		attempt.HTTPStatus, attempt.Injected = m.failure, true
		m.attempts = append(m.attempts, attempt)
		return &http.Response{StatusCode: m.failure, Header: http.Header{}, Request: request,
			Body: io.NopCloser(strings.NewReader(`{"error":{"code":"temporarily_unavailable"}}`))}, nil
	}
	response, err := m.base.RoundTrip(request)
	attempt.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		m.attempts = append(m.attempts, attempt)
		return nil, err
	}
	attempt.HTTPStatus = response.StatusCode
	data, readErr := io.ReadAll(io.LimitReader(response.Body, openAIEmbeddingMaxResponseBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		m.attempts = append(m.attempts, attempt)
		if readErr != nil {
			return nil, readErr
		}
		return nil, closeErr
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	var decoded openAIEmbeddingResponse
	if response.StatusCode == http.StatusOK && json.Unmarshal(data, &decoded) == nil && decoded.Usage != nil {
		tokens := decoded.Usage.PromptTokens
		if tokens == 0 {
			tokens = decoded.Usage.TotalTokens
		}
		attempt.ObservedTokens = &tokens
	}
	m.attempts = append(m.attempts, attempt)
	return response, nil
}

// Wrapping the inner provider reproduces the previous whole-batch retry path.
type cloudflareWholeBatchProvider struct {
	embeddingcontract.EmbeddingProviderInterface
}

func TestCloudflareMeasurementCountsRetryBeforeUnstartedFinalChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body openAIEmbeddingRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if body.Input[0] == "document-100" {
			<-r.Context().Done()
			return
		}
		data := make([]map[string]any, len(body.Input))
		for index := range data {
			data[index] = map[string]any{"index": index, "embedding": []float32{1}}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	defer server.Close()
	transport := &cloudflareMeasurementTransport{base: http.DefaultTransport, failure: http.StatusServiceUnavailable, seen: map[string]bool{}}
	inner := NewOpenAIEmbeddingProvider(&config.Config{AIAPIURL: server.URL, AIAPIKey: "fixture", AIEmbeddingModel: "m", AIEmbeddingDimensions: 1, AIEmbeddingMaxBatchItems: 100}, &http.Client{Transport: transport})
	provider := NewRetryEmbeddingProviderWithKeyAndOptions(inner, newTestLogger(), "fixture", RetryEmbeddingOptions{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond})
	texts := make([]string, 256)
	for index := range texts {
		texts[index] = fmt.Sprintf("document-%d", index)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	vectors, _, err := provider.EmbedBatch(ctx, texts)
	require.Error(t, err)
	require.Empty(t, vectors)
	require.Len(t, transport.attempts, 3)
	require.Equal(t, 1, transport.retries)
	require.Equal(t, 100, transport.resent)
}

func TestCloudflareBatchMeasurement(t *testing.T) {
	if os.Getenv("DENSE_MEM_CLOUDFLARE_BATCH_MEASUREMENT") != "1" {
		t.Skip("opt-in live Cloudflare measurement")
	}
	account, token := os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("CLOUDFLARE_API_TOKEN")
	require.NotEmpty(t, account, "CLOUDFLARE_ACCOUNT_ID is required")
	require.NotEmpty(t, token, "CLOUDFLARE_API_TOKEN is required")
	output := os.Getenv("DENSE_MEM_CLOUDFLARE_MEASUREMENT_OUTPUT")
	require.NotEmpty(t, output, "measurement output path is required")
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	encoder := json.NewEncoder(file)
	measure := func(size, repetition, failure int, wholeBatch bool) {
		transport := &cloudflareMeasurementTransport{base: http.DefaultTransport, failure: failure, seen: map[string]bool{}}
		client := &http.Client{Transport: transport, Timeout: 60 * time.Second}
		inner := NewOpenAIEmbeddingProvider(&config.Config{
			AIAPIURL: "https://api.cloudflare.com/client/v4/accounts/" + account + "/ai/v1",
			AIAPIKey: token, AIEmbeddingModel: "@cf/baai/bge-m3", AIEmbeddingDimensions: 1024,
			AIEmbeddingMaxBatchItems: 100, AIEmbeddingTimeoutSeconds: 60,
		}, client)
		var provider embeddingcontract.EmbeddingProviderInterface = inner
		policy := "failed_chunk"
		if wholeBatch {
			provider, policy = cloudflareWholeBatchProvider{inner}, "legacy_whole_batch"
		}
		retry := NewRetryEmbeddingProviderWithKey(provider, newTestLogger(), token)
		texts := make([]string, size)
		for index := range texts {
			texts[index] = fmt.Sprintf("Document %d records that Dense-Mem owns evidence, provenance, retrieval, and team isolation in PostgreSQL.", index)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		started := time.Now()
		vectors, _, embedErr := retry.EmbedBatch(ctx, texts)
		elapsed := time.Since(started)
		cancel()
		result := cloudflareMeasurementResult{Size: size, Repetition: repetition, Policy: policy, InjectedStatus: failure,
			LatencyMS: elapsed.Milliseconds(), ExceededTenSecond: elapsed > 10*time.Second,
			Succeeded: embedErr == nil, ReturnedVectors: len(vectors), RetriedDocuments: transport.resent,
			UsageAvailable: true, Attempts: transport.attempts}
		for _, attempt := range transport.attempts {
			if attempt.Injected {
				continue
			}
			if attempt.HTTPStatus != http.StatusOK || attempt.ObservedTokens == nil {
				result.UsageAvailable = false
			} else {
				result.ObservedTokens += *attempt.ObservedTokens
			}
		}
		result.Retries = transport.retries
		if result.UsageAvailable && result.ObservedTokens > 0 {
			// Cloudflare publishes 1075 Neurons per million BGE-M3 input tokens.
			neurons := float64(result.ObservedTokens) * 1075 / 1000000
			result.ComputedNeurons = &neurons
		}
		if embedErr != nil {
			result.FailureClass = embeddingcontract.ClassifyFailure(embedErr).Code
			require.Empty(t, vectors, "incomplete batch exposed partial vectors")
		} else {
			require.Len(t, vectors, size)
		}
		require.NoError(t, encoder.Encode(result))
		t.Logf("documents=%d repetition=%d policy=%s injected_status=%d latency_ms=%d success=%t resent=%d", size, repetition, policy, failure, result.LatencyMS, result.Succeeded, result.RetriedDocuments)
		if embedErr != nil && result.FailureClass != "provider_timeout" {
			t.Fatalf("live measurement failed: %s", result.FailureClass)
		}
	}
	for _, size := range []int{100, 200, 256} {
		for repetition := 1; repetition <= 5; repetition++ {
			measure(size, repetition, 0, false)
		}
	}
	for _, size := range []int{200, 256} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			measure(size, 1, status, true)
			measure(size, 1, status, false)
		}
	}
}
