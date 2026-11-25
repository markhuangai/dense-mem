package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/stretchr/testify/require"
)

func TestRememberEmbeddingDiagnosticsRetainRealHTTPFailure(t *testing.T) {
	for _, test := range []struct {
		status int
		reason string
	}{
		{http.StatusTooManyRequests, "embedding_rate_limited"},
		{http.StatusRequestTimeout, "embedding_timeout"},
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
		p := &rememberSynchronousProcessor{embeddingExecutor: semanticwrite.NewProviderExecutor(provider)}
		vectors, err := p.embedSearchDocumentBatch(context.Background(), "team", "owner", processorEmbeddingFence("model"), []knowledgecontract.SearchDocumentForEmbedding{processorEmbeddingDocument(2)})
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
		"processing_state": "completed", "search_state": "current", "evidence": []any{}, "errors": []any{},
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

func TestRememberEmbeddingFailureCanBeDiagnosedFromLogsAlone(t *testing.T) {
	embeddingFailureLogAcceptance(t, false)
}
func TestSessionEmbeddingFailureCanBeDiagnosedFromLogsAlone(t *testing.T) {
	embeddingFailureLogAcceptance(t, true)
}
func embeddingFailureLogAcceptance(t *testing.T, preparingSession bool) {
	for _, level := range []slog.Level{observability.LevelTrace, slog.LevelDebug, slog.LevelInfo} {
		t.Run(level.String(), func(t *testing.T) {
			const secret = "remember-provider-credential-canary"
			admitted := `{"password":"admitted exact fact ` + strings.Repeat("α", 1500) + `"}`
			responseBody := `{"error":{"code":"invalid_api_key","message":"configured embedding credential expired ` + secret + `"}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, responseBody)
			}))
			defer server.Close()
			var output bytes.Buffer
			logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level}))
			protector := observability.NewCredentialProtector(secret)
			client := observability.DiagnosticHTTPClient(server.Client(), logger.ContextSlog(), protector)
			provider := embedding.NewOpenAIEmbeddingProvider(&config.Config{AIAPIURL: server.URL, AIAPIKey: secret, AIEmbeddingModel: "model", AIEmbeddingDimensions: 2}, client)
			actor := requestctx.Actor{TeamID: uuid.New(), OwnerID: uuid.New()}
			ctx := requestctx.WithActor(context.Background(), actor)
			ctx = correlation.WithID(ctx, "remember-diagnostic-correlation")
			ctx = observability.WithAuthenticationSecrets(ctx, secret)
			ctx = requestctx.WithRememberInvocationIDSink(ctx)
			document := processorEmbeddingDocument(2)
			document.DocumentText = admitted
			ledger := &rememberPipelineLedgerStub{rememberFailureLedgerStub: &rememberFailureLedgerStub{}, duplicatePlan: &knowledgecontract.RememberDuplicateEmbeddingPlan{
				Documents: []knowledgecontract.SearchDocumentForEmbedding{document}, EmbeddingModel: "model", EmbeddingDimensions: 2, EmbeddingContractID: "contract", SearchIndexGenerationID: "generation", IndexGeneration: 3,
			}}
			processor := NewSynchronousProcessor(ProcessorDependencies{Ledger: ledger, Assessor: &processorAssessmentProviderStub{}, EmbeddingExecutor: semanticwrite.NewProviderExecutor(provider), Logger: logger, DiagnosticProtector: protector})
			input := rememberapp.RememberProcessRequest{
				TeamID: actor.TeamID.String(), OwnerProfileID: actor.OwnerID.String(), IdempotencyKey: "key", RequestHash: "hash", SpaceKind: "team_shared",
				Evidence: []rememberapp.EvidenceInput{{Content: admitted, ForceInsert: true}}, OriginalRequest: []byte(admitted),
				Metadata: map[string]any{"actor": map[string]any{"correlation_id": "remember-diagnostic-correlation"}},
			}
			if preparingSession {
				result, err := processor.PrepareSession(ctx, session.PrepareInput{Scope: session.Scope{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, SpaceID: "private-space", SpaceGeneration: 1}, SubmissionID: "session-submission", RequestHash: input.RequestHash, Evidence: input.Evidence, Metadata: input.Metadata})
				require.Nil(t, result)
				var failure *session.ProcessingError
				require.ErrorAs(t, err, &failure)
				require.Equal(t, "embedding_unavailable", failure.Code)
			} else {
				_, err := processor.ProcessRemember(ctx, input)
				var failure *rememberapp.RememberProcessError
				require.ErrorAs(t, err, &failure)
				require.NotNil(t, failure.Status)
				require.Equal(t, "failed", failure.Status.ProcessingState)
			}
			require.NotContains(t, output.String(), secret)
			failureCount, invocationCount, planned := 0, 0, false
			bodies := map[string]string{}
			counts, totals := map[string]int{}, map[string]int{}
			hashes := map[string]string{}
			for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
				var row map[string]any
				require.NoError(t, json.Unmarshal(line, &row))
				switch row["msg"] {
				case "remember_processing_failed":
					failureCount++
					require.Equal(t, actor.TeamID.String(), row["team_id"])
					require.Equal(t, actor.OwnerID.String(), row["profile_id"])
					require.Equal(t, "remember-diagnostic-correlation", row["correlation_id"])
					require.Equal(t, "embedding", row["failed_phase"])
					require.Equal(t, "provider_authentication_failed", row["failure_code"])
					require.Equal(t, float64(401), row["provider_status_code"])
				case "remember_invocation_completed":
					invocationCount++
				case "remember_embedding_planned":
					planned = true
					require.Equal(t, "model", row["embedding_fence"].(map[string]any)["model"])
				case "ai_exchange_payload":
					direction := row["direction"].(string)
					var chunk observability.DiagnosticBodyChunk
					encoded, _ := json.Marshal(row["payload_chunk"])
					require.NoError(t, json.Unmarshal(encoded, &chunk))
					require.Equal(t, counts[direction], chunk.Index)
					require.Equal(t, "captured", chunk.State)
					counts[direction]++
					totals[direction], hashes[direction] = chunk.Count, chunk.SHA256
					bodies[direction] += chunk.Body
				}
			}
			require.Equal(t, 1, failureCount)
			require.Equal(t, 1, invocationCount)
			if level == slog.LevelInfo {
				require.Empty(t, bodies)
				require.False(t, planned)
				return
			}
			require.True(t, planned)
			require.Greater(t, counts["request"], 1)
			for direction, body := range bodies {
				require.Equal(t, totals[direction], counts[direction])
				digest := sha256.Sum256([]byte(body))
				require.Equal(t, hashes[direction], hex.EncodeToString(digest[:]))
			}
			var wire struct {
				Input []string `json:"input"`
			}
			require.NoError(t, json.Unmarshal([]byte(bodies["request"]), &wire))
			require.Equal(t, []string{admitted}, wire.Input)
			require.Equal(t, strings.ReplaceAll(responseBody, secret, observability.CredentialProtectionRedacted), bodies["response"])
			t.Logf("Log-only diagnosis (Session preparation=%t): the authenticated invocation failed during duplicate embedding under model/generation 3. HTTP 401 and provider_authentication_failed identify an expired configured embedding credential. Replace that credential and retry the unchanged request; no semantic commit occurred.", preparingSession)
		})
	}
}

type lateRememberEmbeddingProvider struct {
	processorEmbeddingProviderStub
	cancel context.CancelFunc
}

func (p *lateRememberEmbeddingProvider) EmbedBatch(context.Context, []string) ([][]float32, string, error) {
	p.cancel()
	return [][]float32{{1, 2}}, p.model, nil
}
func TestRememberEmbeddingRejectsLateSuccessAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &lateRememberEmbeddingProvider{processorEmbeddingProviderStub: processorEmbeddingProviderStub{available: true, model: "model"}, cancel: cancel}
	p := NewSynchronousProcessor(ProcessorDependencies{EmbeddingExecutor: semanticwrite.NewProviderExecutor(provider)})
	completed, err := p.embedSearchDocumentBatch(ctx, "team", "owner", processorEmbeddingFence("model"), []knowledgecontract.SearchDocumentForEmbedding{processorEmbeddingDocument(2)})
	require.ErrorIs(t, err, rememberapp.ErrRememberRequestCancelled)
	require.Empty(t, completed)
}

func TestSessionPreparationUsesExecutorAndPreservesEmbeddingFailureCode(t *testing.T) {
	cause := &embeddingcontract.ProviderError{FailureCode: "provider_response_invalid", FailureClass: "provider_action_required"}
	provider := &processorEmbeddingProviderStub{available: true, model: "model", err: cause}
	ledger := &rememberPipelineLedgerStub{rememberFailureLedgerStub: &rememberFailureLedgerStub{}, duplicatePlan: &knowledgecontract.RememberDuplicateEmbeddingPlan{
		Documents: []knowledgecontract.SearchDocumentForEmbedding{processorEmbeddingDocument(2)}, EmbeddingModel: "model", EmbeddingDimensions: 2, EmbeddingContractID: "contract", SearchIndexGenerationID: "generation", IndexGeneration: 9,
	}}
	p := NewSynchronousProcessor(ProcessorDependencies{Ledger: ledger, EmbeddingExecutor: semanticwrite.NewProviderExecutor(provider)})
	result, err := p.PrepareSession(context.Background(), session.PrepareInput{Scope: session.Scope{TeamID: "team", OwnerProfileID: "owner", SpaceID: "space", SpaceGeneration: 1}, SubmissionID: "session", RequestHash: "hash", Evidence: []rememberapp.EvidenceInput{{Content: "exact session excerpt", ForceInsert: true}}})
	require.Nil(t, result)
	var failure *session.ProcessingError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "embedding_response_invalid", failure.Code)
	require.ErrorIs(t, err, cause)
	require.Equal(t, 1, provider.called)
}
