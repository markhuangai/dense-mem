package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
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

func TestRememberProcessorCommitsValidatedAssessmentAndResult(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "key", RequestHash: "hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "fact", ForceInsert: true}},
	}
	commitResult := map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": "committed", "submission_kind": "remember",
		"processing_state": "completed", "search_state": "current", "evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
	}
	ledger := &rememberPipelineLedgerStub{
		rememberFailureLedgerStub: &rememberFailureLedgerStub{},
		plan:                      &knowledgecontract.InlineEmbeddingPlan{},
		commitResult:              &knowledgecontract.SynchronousRememberCommitResult{IngestID: "committed", Outcome: "completed", PublicResult: commitResult},
	}
	metrics := observability.NewPrometheusMetrics()
	logger := &rememberProcessorLogCapture{}
	processor := &rememberSynchronousProcessor{
		ledger:   ledger,
		catalog:  &processorAssessmentCatalogStub{},
		provider: &processorAssessmentProviderStub{},
		metrics:  metrics, logger: logger,
	}
	ctx := requestctx.WithRememberInvocationIDSink(context.Background())
	status, err := processor.ProcessRemember(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "committed", status.SubmissionID)
	require.Equal(t, "completed", status.ProcessingState)
	require.Equal(t, "evaluated_zero", ledger.invocation.Outcome)
	require.Equal(t, "execution", ledger.invocation.Classification)
	require.NotEmpty(t, requestctx.RememberInvocationIDFromContext(ctx))
	require.Contains(t, rememberMetricsText(t, metrics), `densemem_logical_operation_attempts_total{classification="execution",operation="remember",outcome="evaluated_zero"} 1`)
	require.NotNil(t, ledger.commitInput.AssessorDiagnostics)
	var phases []map[string]any
	for index, message := range logger.infos {
		if message == "remember_phase_completed" {
			phases = append(phases, logger.infoAttrs[index])
		}
	}
	require.Len(t, phases, 4)
	for index, phase := range []string{"duplicate_embedding", "assessment", "embedding", "commit"} {
		attrs := phases[index]
		require.Equal(t, phase, attrs["phase"])
		require.Equal(t, input.TeamID, attrs["team_id"])
		require.Equal(t, input.OwnerProfileID, attrs["owner_profile_id"])
		require.NotEmpty(t, attrs["attempt_id"])
		require.GreaterOrEqual(t, attrs["duration_ms"].(int64), int64(0))
	}
	require.NotNil(t, phases[1]["assessor_diagnostics"])
	ledger.commitErr = context.DeadlineExceeded
	ledger.commitResult = nil
	_, err = processor.ProcessRemember(ctx, input)
	require.Error(t, err)
	require.Equal(t, "commit", ledger.failure.Attempt.FailedPhase)
	require.NotNil(t, ledger.failure.Attempt.AssessorDiagnostics)

}
