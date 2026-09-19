package handler

import (
	"context"

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

func (a mcpLoggerAdapter) Trace(message string, fields ...mcp.LogField) {
	if logger, ok := a.logger.(interface {
		Trace(string, ...httpcontract.LogAttr)
	}); ok {
		logger.Trace(message, httpFields(fields)...)
		return
	}
	a.logger.Debug(message, httpFields(fields)...)
}

func (a mcpLoggerAdapter) Debug(message string, fields ...mcp.LogField) {
	a.logger.Debug(message, httpFields(fields)...)
}

func (a mcpLoggerAdapter) Info(message string, fields ...mcp.LogField) {
	a.logger.Info(message, httpFields(fields)...)
}

func (a mcpLoggerAdapter) Warn(message string, fields ...mcp.LogField) {
	a.logger.Warn(message, httpFields(fields)...)
}

func (a mcpLoggerAdapter) WarnContext(ctx context.Context, message string, fields ...mcp.LogField) {
	attrs := httpFields(fields)
	if contextual, ok := a.logger.(interface {
		WarnContext(context.Context, string, ...httpcontract.LogAttr)
	}); ok {
		contextual.WarnContext(ctx, message, attrs...)
		return
	}
	a.logger.Warn(message, attrs...)
}

func (a mcpLoggerAdapter) TraceContext(ctx context.Context, message string, fields ...mcp.LogField) {
	if logger, ok := a.logger.(interface {
		TraceContext(context.Context, string, ...httpcontract.LogAttr)
	}); ok {
		logger.TraceContext(ctx, message, httpFields(fields)...)
		return
	}
	a.mcpLogContextFallback(ctx, "debug", message, fields...)
}

func (a mcpLoggerAdapter) DebugContext(ctx context.Context, message string, fields ...mcp.LogField) {
	if logger, ok := a.logger.(interface {
		DebugContext(context.Context, string, ...httpcontract.LogAttr)
	}); ok {
		logger.DebugContext(ctx, message, httpFields(fields)...)
		return
	}
	a.logger.Debug(message, httpFields(fields)...)
}

func (a mcpLoggerAdapter) InfoContext(ctx context.Context, message string, fields ...mcp.LogField) {
	if logger, ok := a.logger.(interface {
		InfoContext(context.Context, string, ...httpcontract.LogAttr)
	}); ok {
		logger.InfoContext(ctx, message, httpFields(fields)...)
		return
	}
	a.logger.Info(message, httpFields(fields)...)
}

func (a mcpLoggerAdapter) ErrorContext(ctx context.Context, message string, err error, fields ...mcp.LogField) {
	if logger, ok := a.logger.(interface {
		ErrorContext(context.Context, string, error, ...httpcontract.LogAttr)
	}); ok {
		logger.ErrorContext(ctx, message, err, httpFields(fields)...)
		return
	}
	a.logger.Error(message, err, httpFields(fields)...)
}

func (a mcpLoggerAdapter) Fatal(message string, fields ...mcp.LogField) {
	if logger, ok := a.logger.(interface {
		Fatal(string, ...httpcontract.LogAttr)
	}); ok {
		logger.Fatal(message, httpFields(fields)...)
		return
	}
	a.logger.Error(message, nil, httpFields(fields)...)
}

func (a mcpLoggerAdapter) FatalContext(ctx context.Context, message string, fields ...mcp.LogField) {
	if logger, ok := a.logger.(interface {
		FatalContext(context.Context, string, ...httpcontract.LogAttr)
	}); ok {
		logger.FatalContext(ctx, message, httpFields(fields)...)
		return
	}
	a.mcpLogContextFallback(ctx, "error", message, fields...)
}

func (a mcpLoggerAdapter) mcpLogContextFallback(ctx context.Context, level, message string, fields ...mcp.LogField) {
	attrs := httpFields(fields)
	switch level {
	case "error":
		a.logger.Error(message, nil, attrs...)
	case "debug":
		if logger, ok := a.logger.(interface {
			DebugContext(context.Context, string, ...httpcontract.LogAttr)
		}); ok {
			logger.DebugContext(ctx, message, attrs...)
			return
		}
		a.logger.Debug(message, attrs...)
	default:
		a.logger.Info(message, attrs...)
	}
}

func httpFields(fields []mcp.LogField) []httpcontract.LogAttr {
	converted := make([]httpcontract.LogAttr, 0, len(fields))
	for _, field := range fields {
		converted = append(converted, httpcontract.LogAttr{Key: field.Key, Value: field.Value})
	}
	return converted
}
