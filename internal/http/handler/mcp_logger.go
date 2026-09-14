package handler

import (
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/mcp"
)

// NewMCPLogger adapts the shared sanitized logger at the HTTP-to-MCP
// composition boundary. MCP itself depends only on its narrow transport port.
func NewMCPLogger(logger httpcontract.LogProvider) mcp.Logger {
	if logger == nil {
		return nil
	}
	return mcpLoggerAdapter{logger: logger}
}

type mcpLoggerAdapter struct {
	logger httpcontract.LogProvider
}

func (a mcpLoggerAdapter) Error(message string, err error, fields ...mcp.LogField) {
	a.logger.Error(message, err, httpFields(fields)...)
}

func (a mcpLoggerAdapter) Warn(message string, fields ...mcp.LogField) {
	a.logger.Warn(message, httpFields(fields)...)
}

func httpFields(fields []mcp.LogField) []httpcontract.LogAttr {
	converted := make([]httpcontract.LogAttr, 0, len(fields))
	for _, field := range fields {
		converted = append(converted, httpcontract.LogAttr{Key: field.Key, Value: field.Value})
	}
	return converted
}
