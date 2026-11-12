package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
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
	session  *sdkmcp.ServerSession
	http     sdkProgressHTTP
	metadata sdkProgressMetadata
	token    any
	seen     map[rememberapp.ProgressStage]bool
	sequence int
	closed   bool
}

func (s *Server) withSDKRememberProgress(ctx context.Context, name string, req *sdkmcp.CallToolRequest) (context.Context, func()) {
	noop := func() {}
	if name != "remember" {
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
		LogField{Key: "reason", Value: reason}, LogField{Key: "protocol_version", Value: transport.protocol},
		LogField{Key: "sse", Value: transport.sse})
	if reason != "enabled" {
		return ctx, noop
	}
	// The SDK decodes metadata numbers through float64; restore the exact wire token before using its accessor.
	req.Params.Meta["progressToken"] = token
	progress := &sdkRememberProgress{server: s, session: req.Session, http: transport, metadata: metadata,
		token: req.Params.GetProgressToken(), seen: make(map[rememberapp.ProgressStage]bool, 5)}
	return rememberapp.WithProgressObserver(ctx, progress.emit), func() { progress.closed = true }
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
	default:
		return ""
	}
}

func (p *sdkRememberProgress) emit(ctx context.Context, stage rememberapp.ProgressStage) {
	message := progressMessage(stage)
	if p.closed || p.seen[stage] || message == "" || ctx.Err() != nil {
		return
	}
	p.seen[stage] = true
	p.sequence++
	attrs := []LogField{
		{Key: "request_id", Value: json.RawMessage(p.metadata.requestID)},
		{Key: "progress_token", Value: p.token}, {Key: "stage", Value: string(stage)},
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
