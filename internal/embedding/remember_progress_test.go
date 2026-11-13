package embedding

import (
	"context"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestRememberPartitionEmbeddingReportsSingleBatchCountsBeforeProvider(t *testing.T) {
	for _, operation := range []string{"duplicate_embedding", "embedding"} {
		t.Run(operation, func(t *testing.T) {
			var updates []requestctx.ProgressUpdate
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				require.Equal(t, []requestctx.ProgressUpdate{{Stage: "embedding_batch", Operation: operation, Batch: 1, Batches: 1}}, updates)
				w.Header().Set("Content-Type", "application/json")
				_, err := io.WriteString(w, `{"data":[{"index":0,"embedding":[1]}]}`)
				require.NoError(t, err)
			}))
			defer server.Close()
			provider := NewOpenAIEmbeddingProvider(&config.Config{AIAPIURL: server.URL, AIAPIKey: "synthetic", AIEmbeddingModel: "fixture", AIEmbeddingDimensions: 1}, server.Client())
			ctx := requestctx.WithProgressObserver(requestctx.WithProgressOperation(t.Context(), operation), func(_ context.Context, update requestctx.ProgressUpdate) {
				mu.Lock()
				defer mu.Unlock()
				updates = append(updates, update)
			})
			_, _, err := provider.EmbedBatch(ctx, []string{"one exact input"})
			require.NoError(t, err)
		})
	}
}
