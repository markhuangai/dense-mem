package handler

import (
	"github.com/markhuangai/dense-mem/internal/mcp"
	"github.com/markhuangai/dense-mem/internal/observability"
)

// NewMCPLogger adapts the shared sanitized logger at the HTTP-to-MCP
// composition boundary. MCP itself depends only on its narrow transport port.
func NewMCPLogger(logger observability.LogProvider) mcp.Logger {
	if logger == nil {
		return nil
	}
	return mcpLoggerAdapter{logger: logger}
}

type mcpLoggerAdapter struct {
	logger observability.LogProvider
}

func (a mcpLoggerAdapter) Error(message string, err error, fields ...mcp.LogField) {
	a.logger.Error(message, err, observabilityFields(fields)...)
}

func (a mcpLoggerAdapter) Warn(message string, fields ...mcp.LogField) {
	a.logger.Warn(message, observabilityFields(fields)...)
}

func observabilityFields(fields []mcp.LogField) []observability.LogAttr {
	converted := make([]observability.LogAttr, 0, len(fields))
	for _, field := range fields {
		converted = append(converted, observability.LogAttr{Key: field.Key, Value: field.Value})
	}
	return converted
}
