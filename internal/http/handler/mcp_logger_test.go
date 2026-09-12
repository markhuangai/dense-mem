package handler

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/mcp"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestNewMCPLoggerPreservesSanitizedLogging(t *testing.T) {
	var output bytes.Buffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	adapted := NewMCPLogger(logger)

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
