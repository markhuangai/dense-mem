package handler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/mcp"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type mcpTestLogger struct{ delegate observability.LogProvider }
type mcpContextKey struct{}

func (l mcpTestLogger) Info(message string, attrs ...httpcontract.LogAttr) {
	if l.delegate == nil {
		return
	}
	l.delegate.Info(message, mcpObservabilityAttrs(attrs)...)
}
func (l mcpTestLogger) Error(message string, err error, attrs ...httpcontract.LogAttr) {
	if l.delegate == nil {
		return
	}
	l.delegate.Error(message, err, mcpObservabilityAttrs(attrs)...)
}
func (l mcpTestLogger) Warn(message string, attrs ...httpcontract.LogAttr) {
	if l.delegate == nil {
		return
	}
	l.delegate.Warn(message, mcpObservabilityAttrs(attrs)...)
}
func (l mcpTestLogger) WarnContext(ctx context.Context, message string, attrs ...httpcontract.LogAttr) {
	if l.delegate == nil {
		return
	}
	if contextual, ok := l.delegate.(observability.ContextLogProvider); ok {
		contextual.WarnContext(ctx, message, mcpObservabilityAttrs(attrs)...)
		return
	}
	l.delegate.Warn(message, mcpObservabilityAttrs(attrs)...)
}
func (l mcpTestLogger) Debug(message string, attrs ...httpcontract.LogAttr) {
	if l.delegate == nil {
		return
	}
	l.delegate.Debug(message, mcpObservabilityAttrs(attrs)...)
}
func (l mcpTestLogger) With(attrs ...httpcontract.LogAttr) httpcontract.LogProvider {
	if l.delegate == nil {
		return l
	}
	return mcpTestLogger{delegate: l.delegate.With(mcpObservabilityAttrs(attrs)...)}
}
func mcpObservabilityAttrs(attrs []httpcontract.LogAttr) []observability.LogAttr {
	converted := make([]observability.LogAttr, 0, len(attrs))
	for _, attr := range attrs {
		converted = append(converted, observability.LogAttr{Key: attr.Key, Value: attr.Value})
	}
	return converted
}

func TestNewMCPLoggerPreservesSanitizedLogging(t *testing.T) {
	var output bytes.Buffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	adapted := NewMCPLogger(mcpTestLogger{delegate: logger}).(mcpLoggerAdapter)
	secret := "mcp-context-auth-secret"
	ctx := observability.WithAuthenticationSecrets(context.Background(), secret)

	adapted.WarnContext(ctx, "mcp_tool_input_rejected", mcp.LogField{Key: "correlation_id", Value: secret}, mcp.LogField{Key: "tool", Value: "remember"})
	adapted.Error("mcp tool failed", errors.New("upstream password=secret-password"), mcp.LogField{Key: "team_id", Value: "team-a"})

	text := output.String()
	if !strings.Contains(text, `"msg":"mcp_tool_input_rejected"`) || !strings.Contains(text, `"tool":"remember"`) {
		t.Fatalf("adapted warning = %s", text)
	}
	if strings.Contains(text, secret) || strings.Contains(text, "secret-password") {
		t.Fatalf("adapted logger leaked sensitive data: %s", text)
	}
}

func TestNewMCPLoggerNilIsSafe(t *testing.T) {
	if NewMCPLogger(nil) != nil {
		t.Fatal("nil logger should remain nil")
	}
}

func TestMCPLoggerAdapterRoutesLevelsWithoutContextFallbacks(t *testing.T) {
	var output bytes.Buffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	adapted := NewMCPLogger(mcpTestLogger{delegate: logger}).(mcpLoggerAdapter)
	ctx := context.WithValue(context.Background(), mcpContextKey{}, "context")
	field := mcp.LogField{Key: "route", Value: "/mcp"}

	adapted.Trace("trace", field)
	adapted.Debug("debug", field)
	adapted.Info("info", field)
	adapted.Warn("warn", field)
	adapted.Error("error", errors.New("error"), field)
	adapted.TraceContext(ctx, "trace-context", field)
	adapted.DebugContext(ctx, "debug-context", field)
	adapted.InfoContext(ctx, "info-context", field)
	adapted.WarnContext(ctx, "warn-context", field)
	adapted.ErrorContext(ctx, "error-context", errors.New("error"), field)
	adapted.Fatal("fatal", field)
	adapted.FatalContext(ctx, "fatal-context", field)

	for _, message := range []string{"trace", "debug", "info", "warn", "error", "warn-context", "fatal"} {
		if !strings.Contains(output.String(), `"msg":"`+message+`"`) {
			t.Fatalf("adapter output missing %q: %s", message, output.String())
		}
	}
}

type fullMCPLogger struct {
	events   []string
	contexts []context.Context
}

func (l *fullMCPLogger) record(message string) { l.events = append(l.events, message) }
func (l *fullMCPLogger) Info(message string, _ ...httpcontract.LogAttr) {
	l.record(message)
}
func (l *fullMCPLogger) Error(message string, _ error, _ ...httpcontract.LogAttr) {
	l.record(message)
}
func (l *fullMCPLogger) Warn(message string, _ ...httpcontract.LogAttr) {
	l.record(message)
}
func (l *fullMCPLogger) Debug(message string, _ ...httpcontract.LogAttr) {
	l.record(message)
}
func (l *fullMCPLogger) With(_ ...httpcontract.LogAttr) httpcontract.LogProvider { return l }
func (l *fullMCPLogger) Trace(message string, _ ...httpcontract.LogAttr) {
	l.record(message)
}
func (l *fullMCPLogger) Fatal(message string, _ ...httpcontract.LogAttr) {
	l.record(message)
}
func (l *fullMCPLogger) InfoContext(ctx context.Context, message string, _ ...httpcontract.LogAttr) {
	l.contexts = append(l.contexts, ctx)
	l.record(message)
}
func (l *fullMCPLogger) ErrorContext(ctx context.Context, message string, _ error, _ ...httpcontract.LogAttr) {
	l.contexts = append(l.contexts, ctx)
	l.record(message)
}
func (l *fullMCPLogger) WarnContext(ctx context.Context, message string, _ ...httpcontract.LogAttr) {
	l.contexts = append(l.contexts, ctx)
	l.record(message)
}
func (l *fullMCPLogger) DebugContext(ctx context.Context, message string, _ ...httpcontract.LogAttr) {
	l.contexts = append(l.contexts, ctx)
	l.record(message)
}
func (l *fullMCPLogger) TraceContext(ctx context.Context, message string, _ ...httpcontract.LogAttr) {
	l.contexts = append(l.contexts, ctx)
	l.record(message)
}
func (l *fullMCPLogger) FatalContext(ctx context.Context, message string, _ ...httpcontract.LogAttr) {
	l.contexts = append(l.contexts, ctx)
	l.record(message)
}

func TestMCPLoggerAdapterUsesOptionalContextualSurface(t *testing.T) {
	logger := &fullMCPLogger{}
	adapted := NewMCPLogger(logger).(mcpLoggerAdapter)
	ctx := context.WithValue(context.Background(), mcpContextKey{}, "request")
	field := mcp.LogField{Key: "route", Value: "/mcp"}

	adapted.Trace("trace", field)
	adapted.WarnContext(ctx, "warn-context", field)
	adapted.TraceContext(ctx, "trace-context", field)
	adapted.DebugContext(ctx, "debug-context", field)
	adapted.InfoContext(ctx, "info-context", field)
	adapted.ErrorContext(ctx, "error-context", errors.New("failure"), field)
	adapted.Fatal("fatal", field)
	adapted.FatalContext(ctx, "fatal-context", field)

	for _, message := range []string{
		"trace", "warn-context", "trace-context", "debug-context", "info-context",
		"error-context", "fatal", "fatal-context",
	} {
		require.Contains(t, logger.events, message)
	}
	require.Len(t, logger.contexts, 6)
}

type baseMCPLogger struct{ events []string }

func (l *baseMCPLogger) Info(message string, _ ...httpcontract.LogAttr) {
	l.events = append(l.events, message)
}
func (l *baseMCPLogger) Error(message string, _ error, _ ...httpcontract.LogAttr) {
	l.events = append(l.events, message)
}
func (l *baseMCPLogger) Warn(message string, _ ...httpcontract.LogAttr) {
	l.events = append(l.events, message)
}
func (l *baseMCPLogger) Debug(message string, _ ...httpcontract.LogAttr) {
	l.events = append(l.events, message)
}
func (l *baseMCPLogger) With(_ ...httpcontract.LogAttr) httpcontract.LogProvider { return l }

func TestMCPLoggerAdapterFallsBackForBaseLoggerContextMethods(t *testing.T) {
	logger := &baseMCPLogger{}
	adapted := NewMCPLogger(logger).(mcpLoggerAdapter)
	ctx := context.Background()

	adapted.WarnContext(ctx, "warn-context")
	adapted.TraceContext(ctx, "trace-context")
	adapted.DebugContext(ctx, "debug-context")
	adapted.InfoContext(ctx, "info-context")
	adapted.ErrorContext(ctx, "error-context", errors.New("failure"))
	adapted.FatalContext(ctx, "fatal-context")

	require.Equal(t, []string{
		"warn-context", "trace-context", "debug-context", "info-context", "error-context", "fatal-context",
	}, logger.events)
}
