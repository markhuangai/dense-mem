package mcp

// LogField is the bounded attribute shape accepted by the MCP transport
// logger. The transport owns this shape so it does not depend on a logging
// implementation or backend package.
type LogField struct {
	Key   string
	Value any
}

// Logger is the minimal logging surface needed by MCP protocol handling.
// Implementations remain responsible for redaction and sink behavior.
type Logger interface {
	Error(string, error, ...LogField)
	Warn(string, ...LogField)
}
