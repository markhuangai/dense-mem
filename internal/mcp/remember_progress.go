package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const rememberProgressWriteBudget = 100 * time.Millisecond

type sdkProgressHTTPContextKey struct{}
type sdkProgressMetadataContextKey struct{}

type sdkProgressHTTP struct {
	controller *http.ResponseController
	sse        bool
	protocol   string
}

type sdkProgressMetadata struct {
	token     json.RawMessage
	requestID json.RawMessage
}

type sdkRememberProgress struct {
	server   *Server
	tool     string
	session  *sdkmcp.ServerSession
	http     sdkProgressHTTP
	metadata sdkProgressMetadata
	token    any
	mu       sync.Mutex
	last     requestctx.ProgressUpdate
	hasLast  bool
	sequence int
	closed   bool
}

func (s *Server) withSDKRememberProgress(ctx context.Context, name string, req *sdkmcp.CallToolRequest) (context.Context, func()) {
	noop := func() {}
	_, registered := s.registry.Get(name)
	if !registered || !slices.Contains(registry.ContractToolNames(), name) {
		return ctx, noop
	}
	transport, ok := ctx.Value(sdkProgressHTTPContextKey{}).(sdkProgressHTTP)
	if !ok || req == nil || req.Session == nil || req.Params == nil {
		return ctx, noop
	}
	metadata, _ := ctx.Value(sdkProgressMetadataContextKey{}).(sdkProgressMetadata)
	token, valid := sdkProgressToken(metadata.token)
	reason := "enabled"
	switch {
	case !transport.sse:
		reason = "json_response"
	case !valid:
		reason = "missing_or_invalid_token"
	case !sdkToolDispatchRequested(ctx):
		reason = "not_a_request"
	}
	sdkLog(ctx, s.logger, slog.LevelDebug, "mcp_progress_eligibility",
		LogField{Key: "tool", Value: name},
		LogField{Key: "reason", Value: reason}, LogField{Key: "protocol_version", Value: transport.protocol},
		LogField{Key: "sse", Value: transport.sse})
	if reason != "enabled" {
		return ctx, noop
	}
	// The SDK decodes metadata numbers through float64; restore the exact wire token before using its accessor.
	req.Params.Meta["progressToken"] = token
	progress := &sdkRememberProgress{server: s, tool: name, session: req.Session, http: transport, metadata: metadata,
		token: req.Params.GetProgressToken()}
	return requestctx.WithProgressObserver(ctx, progress.emitUpdate), func() { progress.mu.Lock(); defer progress.mu.Unlock(); progress.closed = true }
}

func sdkProgressToken(raw json.RawMessage) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var token any
	if decoder.Decode(&token) != nil {
		return nil, false
	}
	switch value := token.(type) {
	case string:
		return value, true
	case json.Number:
		text := value.String()
		if strings.ContainsAny(text, ".eE") {
			return nil, false
		}
		return value, true
	default:
		return nil, false
	}
}

func progressMessage(stage rememberapp.ProgressStage) string {
	switch stage {
	case rememberapp.ProgressPreparingIntake:
		return "Preparing intake"
	case rememberapp.ProgressCheckingDuplicates:
		return "Checking duplicate Evidence"
	case rememberapp.ProgressAssessingEvidence:
		return "Assessing Evidence"
	case rememberapp.ProgressPreparingSearch:
		return "Preparing search state"
	case rememberapp.ProgressCommitting:
		return "Committing"
	case rememberapp.ProgressValidatingInput:
		return "Validating input"
	case rememberapp.ProgressCheckingReplay:
		return "Checking previous result"
	case rememberapp.ProgressReturningReplay:
		return "Returning saved result"
	case rememberapp.ProgressPreparingBoundaries:
		return "Preparing semantic boundaries"
	case rememberapp.ProgressPartitioning:
		return "Partitioning submitted text"
	case rememberapp.ProgressValidatingPartitions:
		return "Validating partitions"
	case rememberapp.ProgressRegeneratingPartitions:
		return "Regenerating partitions"
	case rememberapp.ProgressDuplicateEmbeddings:
		return "Preparing duplicate-search embeddings"
	case rememberapp.ProgressValidatingAssessment:
		return "Validating assessment"
	case rememberapp.ProgressRegeneratingAssessment:
		return "Regenerating assessment"
	case rememberapp.ProgressPreparingResult:
		return "Preparing result"
	case rememberapp.ProgressRecordingFailure:
		return "Recording failure result"
	case "idempotency_wait":
		return "Waiting for an identical request"
	case "session_lock":
		return "Checking session ownership"
	case "session_windows":
		return "Preparing extraction windows"
	case "session_window_wait":
		return "Waiting for extraction window capacity"
	case "session_extraction":
		return "Extracting exact session excerpts"
	case "session_validation":
		return "Validating session excerpts"
	case "session_regeneration":
		return "Regenerating session excerpts"
	case "session_checkpoint":
		return "Saving validated session excerpts"
	case "recall_policy":
		return "Preparing Recall policy"
	case "recall_contract":
		return "Reading the search contract"
	case "recall_embedding":
		return "Preparing query embedding"
	case "recall_search":
		return "Searching authorized Evidence"
	case "recall_hydration":
		return "Reading authorized Evidence"
	case "recall_conflicts":
		return "Reading Evidence conflicts"
	case "recall_reranking":
		return "Ranking authorized Evidence"
	case "recall_topics":
		return "Selecting related topics"
	case "recall_snapshot":
		return "Recording the Recall snapshot"
	case "trace_read":
		return "Reading the authorized Evidence trace"
	case "retraction_apply":
		return "Applying Evidence retraction"
	case "feedback_record":
		return "Recording Recall feedback"
	case "export_read":
		return "Reading Evidence for export"
	case "export_artifact":
		return "Building the memory pack"
	default:
		return ""
	}
}

func (p *sdkRememberProgress) emitUpdate(ctx context.Context, update requestctx.ProgressUpdate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	message := progressMessage(rememberapp.ProgressStage(update.Stage))
	operation := map[string]string{
		"partition": "partition provider", "assessment": "assessment provider",
		"duplicate_embedding": "duplicate embedding provider", "embedding": "embedding provider",
		"session_extraction": "session extraction provider", "recall_embedding": "query embedding provider",
		"recall_reranking": "Recall ranking provider",
	}[update.Operation]
	if operation != "" {
		switch update.Stage {
		case "provider_wait":
			message = "Waiting for " + operation + " capacity"
		case "provider_retry":
			message = "Waiting before retrying " + operation
		case "provider_request":
			message = "Calling " + operation
		case "embedding_batch":
			message = "Preparing embedding batch"
		case "provider_regeneration":
			message = "Regenerating " + operation + " response"
		}
	}
	if message == "" {
		return
	}
	if update.Attempt > 0 && update.Total >= update.Attempt && update.Total <= 100 {
		message += fmt.Sprintf(" (%d/%d)", update.Attempt, update.Total)
	}
	if update.Batch > 0 && update.Batches >= update.Batch && update.Batches <= 500 {
		message += fmt.Sprintf(" (%d/%d)", update.Batch, update.Batches)
	}
	if p.closed || (p.hasLast && p.last == update) || message == "" || ctx.Err() != nil {
		return
	}
	p.last, p.hasLast = update, true
	p.sequence++
	attrs := []LogField{
		{Key: "tool", Value: p.tool},
		{Key: "request_id", Value: json.RawMessage(p.metadata.requestID)},
		{Key: "progress_token", Value: p.token}, {Key: "stage", Value: update.Stage},
		{Key: "operation", Value: update.Operation}, {Key: "attempt", Value: update.Attempt}, {Key: "batch", Value: update.Batch},
		{Key: "sequence", Value: p.sequence}, {Key: "protocol_version", Value: p.http.protocol},
		{Key: "invocation_id", Value: requestctx.RememberInvocationIDFromContext(ctx)},
		{Key: "write_budget_ms", Value: rememberProgressWriteBudget.Milliseconds()},
	}
	sdkLog(ctx, p.server.logger, slog.LevelDebug, "mcp_progress_sending", attrs...)
	started := time.Now()
	writeCtx, cancel := context.WithTimeout(ctx, rememberProgressWriteBudget)
	defer cancel()
	deadline, _ := writeCtx.Deadline()
	err := p.http.controller.SetWriteDeadline(deadline)
	if err == nil {
		err = p.session.NotifyProgress(writeCtx, &sdkmcp.ProgressNotificationParams{
			ProgressToken: p.token, Progress: float64(p.sequence), Message: message,
		})
		err = errors.Join(err, p.http.controller.SetWriteDeadline(time.Time{}))
	}
	attrs = append(attrs, LogField{Key: "duration_ms", Value: time.Since(started).Milliseconds()})
	if err != nil {
		p.closed = true
		reason := "delivery_failed"
		if errors.Is(err, http.ErrNotSupported) {
			reason = "write_deadline_unsupported"
		}
		attrs = append(attrs, LogField{Key: "reason", Value: reason}, LogField{Key: "error", Value: err.Error()}, LogField{Key: "disabled", Value: true})
		sdkLog(context.WithoutCancel(ctx), p.server.logger, slog.LevelWarn, "mcp_progress_delivery_failed", attrs...)
		return
	}
	sdkLog(ctx, p.server.logger, slog.LevelDebug, "mcp_progress_delivered", attrs...)
}
