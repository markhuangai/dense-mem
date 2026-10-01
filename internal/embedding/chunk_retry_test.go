package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRetryEmbeddingRetainsSuccessfulChunks(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var requests [][]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body openAIEmbeddingRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				requests = append(requests, body.Input)
				if len(requests) == 3 {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"code":"server_error"}}`))
					return
				}
				data := make([]map[string]any, len(body.Input))
				for index, text := range body.Input {
					data[index] = map[string]any{"index": index, "embedding": []float32{float32(len(text))}}
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
			}))
			defer server.Close()
			provider := newChunkRetryProvider(server)
			vectors, model, err := provider.EmbedBatch(context.Background(), []string{"a", "bb", "ccc", "dddd", "eeeee"})
			require.NoError(t, err)
			require.Equal(t, "m", model)
			require.Equal(t, [][]float32{{1}, {2}, {3}, {4}, {5}}, vectors)
			require.Equal(t, [][]string{{"a", "bb"}, {"ccc", "dddd"}, {"eeeee"}, {"eeeee"}}, requests)
		})
	}
}

func TestRetryEmbeddingSharesOneChunkRetryBudget(t *testing.T) {
	var requests []string
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body openAIEmbeddingRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		key := body.Input[0]
		requests = append(requests, key)
		counts[key]++
		if counts[key] == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"server_error"}}`))
			return
		}
		data := make([]map[string]any, len(body.Input))
		for index := range data {
			data[index] = map[string]any{"embedding": []float32{1}}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	defer server.Close()
	vectors, model, err := newChunkRetryProvider(server).EmbedBatch(context.Background(), []string{"a", "b", "c", "d", "e", "f", "g"})
	require.Error(t, err)
	require.Nil(t, vectors)
	require.Empty(t, model)
	require.Equal(t, []string{"a", "a", "c", "c", "e", "e", "g"}, requests)
}

func TestRetryEmbeddingChunkDelayHonorsOriginalDeadline(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body openAIEmbeddingRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		requests = append(requests, body.Input[0])
		mu.Unlock()
		if body.Input[0] == "c" {
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1]},{"embedding":[2]}]}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	vectors, model, err := newChunkRetryProvider(server).EmbedBatch(ctx, []string{"a", "b", "c", "d"})
	require.Error(t, err)
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	require.Nil(t, vectors)
	require.Empty(t, model)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"a", "c"}, requests)
}

func newChunkRetryProvider(server *httptest.Server) *RetryEmbeddingProvider {
	inner := NewOpenAIEmbeddingProvider(&config.Config{
		AIAPIURL: server.URL, AIAPIKey: "key", AIEmbeddingModel: "m", AIEmbeddingDimensions: 1,
		AIEmbeddingMaxBatchItems: 2,
	}, server.Client())
	return NewRetryEmbeddingProviderWithKeyAndOptions(inner, newTestLogger(), "key", RetryEmbeddingOptions{
		BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
	})
}
