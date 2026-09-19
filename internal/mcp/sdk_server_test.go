package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestSDKHTTPHandlerUsesTheSharedRegistryAndSupportsFrozenAndCurrentFlows(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{
		Name:           "read_tool",
		Description:    "read",
		InputSchema:    map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}},
		OutputSchema:   map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "additionalProperties": false},
		RequiredScopes: []string{"read"},
		Invoke: func(_ context.Context, _ string, input map[string]any) (map[string]any, error) {
			return map[string]any{"value": input["value"]}, nil
		},
	}))
	server := NewServerWithScopes(reg, "profile-a", []string{"read"}, logger)
	handler := server.NewSDKHTTPHandler(true)

	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			method := "initialize"
			params := `{"protocolVersion":"` + version + `"}`
			if version == "2026-07-28" {
				method = "server/discover"
				params = `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`
			}
			body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("MCP-Protocol-Version", version)
			if version == "2026-07-28" {
				request.Header.Set("Mcp-Method", "server/discover")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			var envelope struct {
				Result struct {
					ProtocolVersion   string   `json:"protocolVersion"`
					SupportedVersions []string `json:"supportedVersions"`
				} `json:"result"`
				Error any `json:"error"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			require.Nil(t, envelope.Error)
			if version == "2026-07-28" {
				require.Contains(t, envelope.Result.SupportedVersions, version)
			} else {
				require.Equal(t, version, envelope.Result.ProtocolVersion)
			}
		})
	}

	discoverRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":11,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`))
	discoverRequest.Header.Set("Content-Type", "application/json")
	discoverRequest.Header.Set("Accept", "application/json, text/event-stream")
	discoverRequest.Header.Set("MCP-Protocol-Version", "2026-07-28")
	discoverRequest.Header.Set("Mcp-Method", "server/discover")
	discoverResponse := httptest.NewRecorder()
	handler.ServeHTTP(discoverResponse, discoverRequest)
	require.Equal(t, http.StatusOK, discoverResponse.Code)
	require.Contains(t, discoverResponse.Body.String(), `"supportedVersions"`)
	require.Contains(t, discoverResponse.Body.String(), `2026-07-28`)

	listRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	listRequest.Header.Set("Content-Type", "application/json")
	listRequest.Header.Set("Accept", "application/json, text/event-stream")
	listRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	require.Equal(t, http.StatusOK, listResponse.Code)
	require.Contains(t, listResponse.Body.String(), `"name":"read_tool"`)
	require.Contains(t, listResponse.Body.String(), `"outputSchema":`)
	require.Contains(t, listResponse.Body.String(), `"value":{"type":"string"}`)

	callRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_tool","arguments":{"value":"ok"}}}`))
	callRequest.Header.Set("Content-Type", "application/json")
	callRequest.Header.Set("Accept", "application/json, text/event-stream")
	callRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	callResponse := httptest.NewRecorder()
	handler.ServeHTTP(callResponse, callRequest)
	require.Equal(t, http.StatusOK, callResponse.Code)
	require.Contains(t, callResponse.Body.String(), `\"value\":\"ok\"`)
}

func TestSDKHTTPHandlerLogsRoutineSessionLifecycleAtDebug(t *testing.T) {
	for _, tc := range []struct {
		name      string
		level     slog.Level
		wantLevel string
	}{
		{name: "info", level: slog.LevelInfo},
		{name: "debug", level: slog.LevelDebug, wantLevel: "DEBUG"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, logBuffer := testLoggerWithLevel(t, tc.level)
			server := NewServer(registry.New(), "profile-a", logger)
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("MCP-Protocol-Version", "2025-11-25")
			response := httptest.NewRecorder()
			server.NewSDKHTTPHandler(true).ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)

			records := decodeSDKLogRecords(t, logBuffer.Bytes())
			if tc.wantLevel == "" {
				require.Empty(t, records)
				return
			}
			lifecycle := map[string]int{
				"server connecting":           0,
				"server session connected":    0,
				"server session disconnected": 0,
			}
			for _, record := range records {
				message, ok := record["msg"].(string)
				if !ok {
					continue
				}
				if _, ok := lifecycle[message]; !ok {
					continue
				}
				lifecycle[message]++
				require.Equal(t, tc.wantLevel, record["level"])
				if message != "server connecting" {
					require.Contains(t, record, "session_id")
					require.Empty(t, record["session_id"])
				}
			}
			for message, count := range lifecycle {
				require.Equal(t, 1, count, message)
			}
		})
	}
}

func TestSDKLoggerDemotesOnlyRoutineSessionLifecycle(t *testing.T) {
	var logBuffer bytes.Buffer
	delegate := slog.New(slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger := newSDKLogger(delegate).With("component", "mcp_sdk").WithGroup("transport")

	for _, message := range []string{
		"server connecting",
		"server session connected",
		"session initialized",
		"server session disconnected",
	} {
		logger.Info(message, "session_id", "session-a")
	}
	logger.Info("resource subscribed", "uri", "memory://resource")
	logger.Warn("calling tools/list: warning", "method", "tools/list")
	logger.Error("server connect error", "error", errors.New("connect failed"))

	records := decodeSDKLogRecords(t, logBuffer.Bytes())
	require.Len(t, records, 7)
	byMessage := make(map[string]map[string]any, len(records))
	for _, record := range records {
		message, ok := record["msg"].(string)
		require.True(t, ok)
		byMessage[message] = record
		require.Equal(t, "mcp_sdk", record["component"])
	}
	for _, message := range []string{
		"server connecting",
		"server session connected",
		"session initialized",
		"server session disconnected",
	} {
		require.Equal(t, "DEBUG", byMessage[message]["level"])
	}
	require.Equal(t, "INFO", byMessage["resource subscribed"]["level"])
	require.Equal(t, "WARN", byMessage["calling tools/list: warning"]["level"])
	require.Equal(t, "ERROR", byMessage["server connect error"]["level"])
}

type sdkContextLogger struct {
	events []string
}

type sdkContextKey struct{}

func (l *sdkContextLogger) record(message string)                        { l.events = append(l.events, message) }
func (l *sdkContextLogger) Error(message string, _ error, _ ...LogField) { l.record(message) }
func (l *sdkContextLogger) Warn(message string, _ ...LogField)           { l.record(message) }
func (l *sdkContextLogger) Trace(message string, _ ...LogField)          { l.record(message) }
func (l *sdkContextLogger) Debug(message string, _ ...LogField)          { l.record(message) }
func (l *sdkContextLogger) Info(message string, _ ...LogField)           { l.record(message) }
func (l *sdkContextLogger) Fatal(message string, _ ...LogField)          { l.record(message) }
func (l *sdkContextLogger) WarnContext(_ context.Context, message string, _ ...LogField) {
	l.record(message)
}
func (l *sdkContextLogger) TraceContext(_ context.Context, message string, _ ...LogField) {
	l.record(message)
}
func (l *sdkContextLogger) DebugContext(_ context.Context, message string, _ ...LogField) {
	l.record(message)
}
func (l *sdkContextLogger) InfoContext(_ context.Context, message string, _ ...LogField) {
	l.record(message)
}
func (l *sdkContextLogger) ErrorContext(_ context.Context, message string, _ error, _ ...LogField) {
	l.record(message)
}
func (l *sdkContextLogger) FatalContext(_ context.Context, message string, _ ...LogField) {
	l.record(message)
}

type sdkTestLogValuer struct{}

func (sdkTestLogValuer) LogValue() slog.Value { return slog.StringValue("resolved-session") }

func TestSDKRootLoggerRoutesContextAndSanitizesSDKFields(t *testing.T) {
	logger := &sdkContextLogger{}
	ctx := context.WithValue(context.Background(), sdkContextKey{}, "request")
	handler := sdkRootLogHandler{logger: logger}
	require.True(t, handler.Enabled(ctx, slog.LevelInfo))
	require.False(t, (sdkRootLogHandler{}).Enabled(ctx, slog.LevelInfo))

	bound, ok := handler.WithAttrs([]slog.Attr{
		slog.String("ignored", "value"),
		slog.Any("session_id", sdkTestLogValuer{}),
	}).(sdkRootLogHandler)
	require.True(t, ok)
	grouped, ok := bound.WithGroup(" transport ").WithGroup("inner").(sdkRootLogHandler)
	require.True(t, ok)
	record := slog.NewRecord(time.Now(), slog.LevelError, "server connect error", 0)
	record.AddAttrs(slog.Any("session_id", sdkTestLogValuer{}), slog.String("ignored", "value"), slog.Any("error", errors.New("provider failure")))
	require.NoError(t, grouped.Handle(ctx, record))
	require.NotEmpty(t, logger.events)
	require.Equal(t, "mcp_sdk_event", logger.events[len(logger.events)-1])

	for _, level := range []slog.Level{slog.Level(-8), slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError, slog.Level(12)} {
		sdkLog(ctx, logger, level, "sdk-log")
	}
	sdkLogError(ctx, logger, slog.LevelInfo, "sdk-error-low", errors.New("low"))
	sdkLogError(ctx, logger, slog.LevelError, "sdk-error-high", errors.New("high"))
	for _, message := range []string{"sdk-log", "sdk-error-low", "sdk-error-high"} {
		require.Contains(t, logger.events, message)
	}

	var resolved slog.Value = slog.AnyValue(sdkTestLogValuer{})
	require.Equal(t, "resolved-session", slogValueAny(resolved))
	require.Equal(t, "mcp_sdk_event", sdkMessageClass("unclassified event"))
	require.Equal(t, "server session connected", sdkMessageClass("server session connected"))

	var nilRecord slog.Record
	require.NoError(t, (sdkRootLogHandler{}).Handle(ctx, nilRecord))
}

func TestSDKToolOutcomeLoggingCoversAllApplicationOutcomesAndReferenceShapes(t *testing.T) {
	logger := &sdkContextLogger{}
	server := &Server{logger: logger}
	started := time.Now()
	refs := map[string]any{
		"submission_id":        "submission-1",
		"attempt_id":           "attempt-1",
		"canonical_attempt_id": "canonical-1",
		"correlation_id":       "correlation-1",
	}
	server.logSDKToolOutcome(context.Background(), "tool", started, &sdkmcp.CallToolResult{StructuredContent: refs}, nil)
	server.logSDKToolOutcome(context.Background(), "tool", started, &sdkmcp.CallToolResult{IsError: true}, nil)
	server.logSDKToolOutcome(context.Background(), "tool", started, nil, errors.New("rpc failure"))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	server.logSDKToolOutcome(cancelled, "tool", started, nil, nil)
	server.logSDKToolOutcome(context.Background(), "tool", started, nil, nil)

	attrs := []LogField{{Key: "existing", Value: true}}
	for _, value := range []any{
		refs,
		json.RawMessage(`{"correlation_id":"raw-correlation"}`),
		[]byte(`{"attempt_id":"byte-attempt"}`),
		[]byte("invalid"),
		"unsupported",
	} {
		attrs = appendSDKApplicationRefs(attrs, value)
	}
	require.GreaterOrEqual(t, len(attrs), 4)
	require.Contains(t, logger.events, "mcp_tool_outcome")
	(&Server{}).logSDKToolOutcome(context.Background(), "tool", started, nil, nil)
}

func TestSDKToolApplicationOutcomePreservesTerminalResultsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.Equal(t, "success", sdkToolApplicationOutcome(ctx, &sdkmcp.CallToolResult{}, nil))
	require.Equal(t, "tool_error", sdkToolApplicationOutcome(ctx, &sdkmcp.CallToolResult{IsError: true}, nil))
	require.Equal(t, "tool_error", sdkToolApplicationOutcome(ctx, &sdkmcp.CallToolResult{
		IsError: true,
		StructuredContent: map[string]any{
			"errors": []any{map[string]any{"code": "provider_unavailable"}},
		},
	}, nil))
	require.Equal(t, "cancelled", sdkToolApplicationOutcome(ctx, &sdkmcp.CallToolResult{
		IsError: true,
		StructuredContent: map[string]any{
			"errors": []any{map[string]any{"code": "request_cancelled"}},
		},
	}, nil))
	require.Equal(t, "rpc_error", sdkToolApplicationOutcome(ctx, nil, errors.New("provider failure")))
	require.Equal(t, "cancelled", sdkToolApplicationOutcome(ctx, nil, context.Canceled))
	require.Equal(t, "cancelled", sdkToolApplicationOutcome(ctx, nil, nil))
}

type cancellationRejectingMCPLogSink struct {
	records []observability.LogRecord
}

func (s *cancellationRejectingMCPLogSink) WriteLog(ctx context.Context, record observability.LogRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.records = append(s.records, record)
	return nil
}

type contextualMCPLogger struct {
	testLoggerAdapter
}

type sdkCancellingLogWriter struct {
	cancel context.CancelFunc
}

func (w sdkCancellingLogWriter) Write(data []byte) (int, error) {
	w.cancel()
	return len(data), nil
}

func (l contextualMCPLogger) ErrorContext(ctx context.Context, message string, err error, fields ...LogField) {
	if logger, ok := l.delegate.(interface {
		ErrorContext(context.Context, string, error, ...observability.LogAttr)
	}); ok {
		logger.ErrorContext(ctx, message, err, testObservabilityFields(fields)...)
		return
	}
	l.Error(message, err, fields...)
}

func TestSDKToolOutcomeLoggingDetachesCancelledContextForPersistence(t *testing.T) {
	for _, test := range []struct {
		name    string
		result  *sdkmcp.CallToolResult
		err     error
		outcome string
	}{
		{name: "success", result: &sdkmcp.CallToolResult{}, outcome: "success"},
		{name: "tool error", result: &sdkmcp.CallToolResult{IsError: true}, outcome: "tool_error"},
		{name: "rpc error", err: errors.New("rpc failure"), outcome: "rpc_error"},
		{name: "cancelled", outcome: "cancelled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, cancelBeforeLog := range []bool{true, false} {
				ctx, cancel := context.WithCancel(correlation.WithID(context.Background(), "trusted-correlation"))
				defer cancel()
				root := observability.NewWithHandler(slog.NewJSONHandler(sdkCancellingLogWriter{cancel: cancel}, &slog.HandlerOptions{Level: slog.LevelDebug}))
				sink := &cancellationRejectingMCPLogSink{}
				require.NoError(t, root.AttachSink(sink))
				server := &Server{logger: contextualMCPLogger{testLoggerAdapter{delegate: root}}}
				if cancelBeforeLog || test.outcome == "cancelled" {
					cancel()
				}

				server.logSDKToolOutcome(ctx, "tool", time.Now(), test.result, test.err)

				require.ErrorIs(t, ctx.Err(), context.Canceled)
				require.Len(t, sink.records, 1)
				require.Equal(t, "mcp_tool_outcome", sink.records[0].Message)
				require.Equal(t, test.outcome, sink.records[0].Attrs["application_outcome"])
				require.Equal(t, "trusted-correlation", sink.records[0].CorrelationID)
			}
		})
	}
}

func TestSDKToolOutcomeLoggingUsesTrustedCorrelationContext(t *testing.T) {
	root := observability.NewWithHandler(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink := &cancellationRejectingMCPLogSink{}
	require.NoError(t, root.AttachSink(sink))
	server := &Server{logger: testLoggerAdapter{delegate: root}}

	clientContext := correlation.WithClientProvidedID(context.Background(), "client-controlled-correlation")
	clientContext = requestctx.WithAuthenticationVerified(clientContext)
	server.logSDKToolOutcome(clientContext, "tool", time.Now(), &sdkmcp.CallToolResult{
		StructuredContent: map[string]any{"correlation_id": "payload-correlation"},
	}, nil)

	trustedContext := correlation.WithID(context.Background(), "trusted-correlation")
	server.logSDKToolOutcome(trustedContext, "tool", time.Now(), &sdkmcp.CallToolResult{
		StructuredContent: map[string]any{"correlation_id": "payload-correlation"},
	}, nil)

	require.Len(t, sink.records, 2)
	require.Empty(t, sink.records[0].CorrelationID)
	_, hasUntrusted := sink.records[0].Attrs["correlation_id"]
	require.False(t, hasUntrusted)
	require.Equal(t, "trusted-correlation", sink.records[1].CorrelationID)
}

func TestSDKToolLookupFailureLoggingDetachesCancelledContextForPersistence(t *testing.T) {
	root := observability.NewWithHandler(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink := &cancellationRejectingMCPLogSink{}
	require.NoError(t, root.AttachSink(sink))
	server := &Server{logger: contextualMCPLogger{testLoggerAdapter{delegate: root}}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server.logSDKToolLookupFailure(ctx, registry.Tool{}, false, false, errCodeMethodNotFound)

	require.Len(t, sink.records, 1)
	require.Equal(t, "mcp_tool_outcome", sink.records[0].Message)
	require.Equal(t, "tool_error", sink.records[0].Attrs["application_outcome"])
}

func TestSDKHTTPHandlerRejectsUnknownProtocolHeader(t *testing.T) {
	logger, _ := testLogger(t)
	server := NewServer(registry.New(), "profile-a", logger)
	handler := server.NewSDKHTTPHandler(true)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2099-01-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
}

func TestSDKErrorAdaptersPreserveJSONRPCDetails(t *testing.T) {
	require.Nil(t, sdkRPCError(nil))

	err := sdkRPCError(&rpcError{Code: errCodeToolFailure, Message: "tool failed", Data: map[string]any{"retryable": true}})
	var sdkErr *sdkjsonrpc.Error
	require.ErrorAs(t, err, &sdkErr)
	require.Equal(t, int64(errCodeToolFailure), sdkErr.Code)
	require.Equal(t, "tool failed", sdkErr.Message)
	require.JSONEq(t, `{"retryable":true}`, string(sdkErr.Data))

	err = sdkRPCError(&rpcError{Code: errCodeInvalidParams, Message: "invalid params"})
	require.ErrorAs(t, err, &sdkErr)
	require.Nil(t, sdkErr.Data)

	err = sdkProtocolError("  malformed request ")
	require.ErrorAs(t, err, &sdkErr)
	require.Equal(t, int64(errCodeInvalidRequest), sdkErr.Code)
	require.Equal(t, "malformed request", sdkErr.Message)
}

func TestSDKHTTPHandlerMapsUnknownAndUnauthorizedTools(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{Name: "write_tool", RequiredScopes: []string{"write"}}))
	server := NewServerWithScopesAndTeamContext(reg, "profile-a", []string{"read"}, TeamContext{Name: "Project"}, logger)

	for _, tc := range []struct {
		name        string
		accept      string
		tool        string
		wantCode    int
		wantMessage string
	}{
		{name: "unknown JSON", accept: "application/json", tool: "missing_tool", wantCode: errCodeMethodNotFound, wantMessage: "tool not found"},
		{name: "scope JSON", accept: "application/json", tool: "write_tool", wantCode: errCodeToolFailure, wantMessage: "insufficient scope"},
		{name: "scope SSE", accept: "text/event-stream", tool: "write_tool", wantCode: errCodeToolFailure, wantMessage: "insufficient scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"`+tc.tool+`","arguments":{}}}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("MCP-Protocol-Version", "2025-11-25")
			response := httptest.NewRecorder()
			server.NewSDKHTTPHandler(tc.accept != "text/event-stream").ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			require.Contains(t, response.Body.String(), `"code":`+strconv.Itoa(tc.wantCode))
			require.Contains(t, response.Body.String(), tc.wantMessage)
			if tc.accept == "text/event-stream" {
				require.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
			}
		})
	}

	require.False(t, server.writeSDKToolLookupError(httptest.NewRecorder(), &http.Request{Method: http.MethodGet}, true))
}

func TestSDKHTTPHandlerPreservesLargeNumericRPCID(t *testing.T) {
	logger, _ := testLogger(t)
	server := NewServer(registry.New(), "profile-a", logger)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"missing_tool","arguments":{}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response := httptest.NewRecorder()
	server.NewSDKHTTPHandler(true).ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"id":9007199254740993`)
}

func TestSDKHTTPHandlerPreservesOversizedBodyForSDKValidation(t *testing.T) {
	logger, _ := testLogger(t)
	server := NewServer(registry.New(), "profile-a", logger)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", 4<<20+1)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response := httptest.NewRecorder()
	server.NewSDKHTTPHandler(true).ServeHTTP(response, request)
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
}

func TestSDKHTTPHandlerValidatesBeforeToolLookup(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{Name: "write_tool", RequiredScopes: []string{"write"}}))
	server := NewServerWithScopes(reg, "profile-a", []string{"read"}, logger)
	handler := server.NewSDKHTTPHandler(true)

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"missing_tool","arguments":{}}}`))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnsupportedMediaType, response.Code)
	require.NotContains(t, response.Body.String(), "tool not found")

	request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"missing_tool","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), `"code":-32020`)

	request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":{},"method":"tools/call","params":{"name":"missing_tool","arguments":{}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.NotContains(t, response.Body.String(), "tool not found")

	request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"missing_tool","arguments":"invalid"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.NotContains(t, response.Body.String(), "tool not found")
}

func TestSDKHTTPHandlerResolvesRuntimeToolPolicyOncePerRequest(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	for _, name := range []string{
		registry.ToolSubmitRecallSessionFeedback,
		registry.ToolRecallMemory,
		registry.ToolListDreams,
	} {
		require.NoError(t, reg.Register(registry.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}))
	}
	var recallCalls, dreamCalls int
	feedback := recallFeedbackConfigStub{calls: &recallCalls}
	dreams := dreamingConfigStub{calls: &dreamCalls}
	server := NewServerWithScopesTeamContextAndRuntimeConfig(
		reg,
		"profile-a",
		[]string{"read"},
		TeamContext{},
		logger,
		&feedback,
		&dreams,
	)
	handler := server.NewSDKHTTPHandler(true)

	for _, enabled := range []bool{true, false, true} {
		feedback.enabled, dreams.enabled = enabled, enabled
		recallCalls, dreamCalls = 0, 0
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		require.Equal(t, http.StatusOK, response.Code)
		var envelope rpcResp
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		require.Nil(t, envelope.Error)
		var listed sdkmcp.ListToolsResult
		require.NoError(t, json.Unmarshal(envelope.Result, &listed))
		if enabled {
			require.Len(t, listed.Tools, 3)
		} else {
			require.Len(t, listed.Tools, 1)
			require.Equal(t, registry.ToolRecallMemory, listed.Tools[0].Name)
		}
		require.Equal(t, 1, recallCalls)
		require.Equal(t, 1, dreamCalls)
	}
}

func TestSDKHTTPHandlerDoesNotAnswerInitializedNotification(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	server := NewServerWithScopes(reg, "profile-a", []string{"read"}, logger)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response := httptest.NewRecorder()
	server.NewSDKHTTPHandler(true).ServeHTTP(response, request)
	require.Equal(t, http.StatusAccepted, response.Code)
	require.Empty(t, response.Body.String())
}

func TestSDKToolHandlerRejectsInvalidAndUnserializableResults(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	register := func(name string, invoke registry.ToolInvoker) {
		require.NoError(t, reg.Register(registry.Tool{Name: name, Invoke: invoke}))
	}
	register("good", func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"content": []map[string]any{{"text": "ok"}}}, nil
	})
	register("failed", func(context.Context, string, map[string]any) (map[string]any, error) {
		return nil, errors.New("provider unavailable")
	})
	server := NewServer(reg, "profile-a", logger)

	good, err := server.sdkToolHandler("good")(context.Background(), &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{Arguments: json.RawMessage(`{"value":"ok"}`)}})
	require.NoError(t, err)
	require.Len(t, good.Content, 1)
	require.False(t, good.IsError)
	require.Equal(t, map[string]any{"content": []map[string]any{{"text": "ok"}}}, good.StructuredContent)

	_, err = server.sdkToolHandler("good")(context.Background(), &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{Arguments: json.RawMessage("{")}})
	requireSDKError(t, err, errCodeInvalidParams, "invalid params")

	_, err = server.sdkToolHandler("failed")(context.Background(), nil)
	requireSDKError(t, err, errCodeToolFailure, "tool execution failed; contact an operator")
}

func TestSDKToolHandlerReturnsStructuredToolErrors(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	result := map[string]any{"processing_state": "failed", "errors": []any{map[string]any{"code": "embedding_unavailable"}}}
	require.NoError(t, reg.Register(registry.Tool{Name: "terminal", Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
		return nil, registry.NewToolResultError(result)
	}}))
	server := NewServer(reg, "profile-a", logger)

	got, err := server.sdkToolHandler("terminal")(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, got.IsError)
	require.Equal(t, result, got.StructuredContent)
	require.Len(t, got.Content, 1)
	text := got.Content[0].(*sdkmcp.TextContent).Text
	require.JSONEq(t, `{"processing_state":"failed","errors":[{"code":"embedding_unavailable"}]}`, text)
}

func TestSDKToolHandlerCountsEachToolOutcomeOnce(t *testing.T) {
	logger, logBuffer := testLogger(t)
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{Name: "good", Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"content": []map[string]any{{"text": "ok"}}}, nil
	}}))
	require.NoError(t, reg.Register(registry.Tool{Name: "failed", Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
		return nil, registry.NewToolResultError(map[string]any{"code": "failed"})
	}}))
	server := NewServer(reg, "profile-a", logger)

	ctx, metrics := domain.WithMCPToolMetrics(context.Background())
	result, err := server.sdkToolHandler("good")(ctx, nil)
	require.NoError(t, err)
	require.False(t, result.IsError)
	calls, failures := metrics.Snapshot()
	require.Equal(t, int64(1), calls)
	require.Zero(t, failures)

	ctx, metrics = domain.WithMCPToolMetrics(context.Background())
	result, err = server.sdkToolHandler("failed")(ctx, nil)
	require.NoError(t, err)
	require.True(t, result.IsError)
	calls, failures = metrics.Snapshot()
	require.Equal(t, int64(1), calls)
	require.Equal(t, int64(1), failures)
	require.Equal(t, 2, strings.Count(logBuffer.String(), `"msg":"mcp_tool_outcome"`))
	require.Contains(t, logBuffer.String(), `"application_outcome":"tool_error"`)
}

func TestSDKToolHandlerDoesNotCountToolNotifications(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{Name: "good", Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"content": []map[string]any{{"text": "ok"}}}, nil
	}}))
	server := NewServer(reg, "profile-a", logger)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"good","arguments":{}}}`))
	request.Header.Set("Content-Type", "application/json")
	annotateSDKToolDispatch(request)
	ctx, metrics := domain.WithMCPToolMetrics(request.Context())
	result, err := server.sdkToolHandler("good")(ctx, &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{Arguments: json.RawMessage(`{}`)}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	calls, failures := metrics.Snapshot()
	require.Zero(t, calls)
	require.Zero(t, failures)
}

func TestSDKLookupRejectionCountsAsOneFailedCall(t *testing.T) {
	logger, logBuffer := testLogger(t)
	server := NewServer(registry.New(), "profile-a", logger)
	unknownName := "unknown-secret-shaped-tool"
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"`+unknownName+`","arguments":{}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	ctx, metrics := domain.WithMCPToolMetrics(request.Context())
	*request = *request.WithContext(ctx)
	response := httptest.NewRecorder()
	require.True(t, server.writeSDKToolLookupError(response, request, true))
	require.NotContains(t, logBuffer.String(), unknownName)
	require.Contains(t, logBuffer.String(), `"lookup_reason":"tool_not_available"`)
	calls, failures := metrics.Snapshot()
	require.Equal(t, int64(1), calls)
	require.Equal(t, int64(1), failures)
}

func requireSDKError(t *testing.T, err error, code int, message string) {
	t.Helper()
	var sdkErr *sdkjsonrpc.Error
	require.ErrorAs(t, err, &sdkErr)
	require.Equal(t, int64(code), sdkErr.Code)
	require.Contains(t, sdkErr.Message, message)
}

func decodeSDKLogRecords(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) == 1 && len(lines[0]) == 0 {
		return nil
	}
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		require.NoError(t, json.Unmarshal(line, &record))
		records = append(records, record)
	}
	return records
}
