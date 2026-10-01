package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/embedding"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/stretchr/testify/require"
)

func TestRememberEmbeddingDiagnosticsRetainRealHTTPFailure(t *testing.T) {
	for _, test := range []struct {
		status int
		reason string
	}{
		{http.StatusTooManyRequests, "embedding_rate_limited"},
		{http.StatusServiceUnavailable, "embedding_server_failure"},
		{http.StatusUnauthorized, "embedding_request_rejected"},
		{http.StatusForbidden, "embedding_request_rejected"},
		{0, "embedding_transport_failure"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if test.status == 0 {
				connection, _, err := w.(http.Hijacker).Hijack()
				require.NoError(t, err)
				require.NoError(t, connection.Close())
				return
			}
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(test.status)
			_, _ = w.Write([]byte(`{"error":{"code":"temporarily_unavailable","message":"private upstream cause"}}`))
		}))
		provider := embedding.NewOpenAIEmbeddingProvider(&config.Config{
			AIAPIURL: server.URL, AIAPIKey: "fixture", AIEmbeddingModel: "model", AIEmbeddingDimensions: 2,
		}, server.Client())
		p := &rememberSynchronousProcessor{embedder: provider}
		vectors, err := p.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{processorEmbeddingDocument(2)})
		server.Close()
		require.Empty(t, vectors)
		require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingUnavailable)
		reason, details := rememberapp.RememberFailureDetails(err, "embedding")
		public := rememberapp.TerminalStatusErrorWithDetails(rememberapp.TerminalErrorEmbeddingUnavailable, reason, details)
		require.Equal(t, test.reason, public.ReasonCode)
		require.Contains(t, public.Message, "embedding service")
		require.NotContains(t, public.Message, "private upstream cause")
		require.NotContains(t, public.Message, server.URL)
		if test.status == http.StatusUnauthorized || test.status == http.StatusForbidden {
			require.Contains(t, public.Message, "operator")
		}
		require.Equal(t, "retry_same_request", public.NextAction)
		require.NoError(t, rememberapp.ValidateTerminalStatusError(public))
		if test.status == http.StatusTooManyRequests {
			require.Equal(t, 2, public.Details["retry_after_seconds"])
		}
	}
}
