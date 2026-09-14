package handler

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/mcp"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type mcpTestLogger struct{ delegate observability.LogProvider }

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
	adapted := NewMCPLogger(mcpTestLogger{delegate: logger})

	adapted.Warn("mcp_tool_input_rejected", mcp.LogField{Key: "token", Value: "secret-token"}, mcp.LogField{Key: "tool", Value: "remember"})
	adapted.Error("mcp tool failed", errors.New("upstream password=secret-password"), mcp.LogField{Key: "team_id", Value: "team-a"})

	text := output.String()
	if !strings.Contains(text, `"msg":"mcp_tool_input_rejected"`) || !strings.Contains(text, `"tool":"remember"`) {
		t.Fatalf("adapted warning = %s", text)
	}
	if strings.Contains(text, "secret-token") || strings.Contains(text, "secret-password") {
		t.Fatalf("adapted logger leaked sensitive data: %s", text)
	}
}

func TestNewMCPLoggerNilIsSafe(t *testing.T) {
	if NewMCPLogger(nil) != nil {
		t.Fatal("nil logger should remain nil")
	}
}
