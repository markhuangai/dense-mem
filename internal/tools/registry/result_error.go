package registry

import "errors"

// ToolResultError carries a bounded, already-serialized tool outcome. It is
// distinct from protocol/input errors so the MCP transport can return
// structuredContent and isError without exposing a Go or database error.
type ToolResultError struct {
	Result map[string]any
}

func (e *ToolResultError) Error() string {
	return "tool returned a structured error result"
}

func NewToolResultError(result map[string]any) error {
	return &ToolResultError{Result: result}
}

func ToolResultFromError(err error) (*ToolResultError, bool) {
	var resultErr *ToolResultError
	if !errors.As(err, &resultErr) || resultErr == nil || resultErr.Result == nil {
		return nil, false
	}
	return resultErr, true
}
