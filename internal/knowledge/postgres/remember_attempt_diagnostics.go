package postgres

import (
	"context"
	"strings"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

// RememberAttemptDiagnosticsRepository is the narrow application port for
// control-only attempt diagnostics and retention.
type RememberAttemptDiagnosticsRepository interface {
	ListRememberAttemptDiagnostics(context.Context, RememberAttemptDiagnosticFilter) (*RememberAttemptDiagnosticRecordPage, error)
	GetRememberAttemptDiagnostic(context.Context, string, string) (*RememberAttemptDiagnosticRecord, error)
}

var _ RememberAttemptDiagnosticsRepository = (*Store)(nil)

func validRememberDiagnosticCaptureState(value string) bool {
	switch strings.TrimSpace(value) {
	case "captured", "truncated", "not_captured", "hash_only", "provider_not_called", "no_response", "interrupted", "not_delivered", "unavailable":
		return true
	default:
		return false
	}
}

func rememberDiagnosticCaptureState(outcome string, requestBody, responseBody []byte) string {
	return knowledgecontract.DiagnosticCaptureState("", outcome, len(requestBody), len(responseBody))
}
