package observability

import (
	"bytes"
	"encoding/json"
	"io"
)

// DiagnosticProtector is the narrow root-owned capture seam used by
// application capabilities. It keeps retained payload policy out of services.
type DiagnosticProtector interface {
	ProtectDiagnosticBytes([]byte, int, ...string) ([]byte, CredentialProtectionUnavailableReason)
}

// ProtectDiagnosticBytes returns a detached, bounded JSON representation of an
// admitted diagnostic payload. It protects exact configured or request-scoped
// credentials while preserving admitted user content and operational causes.
func (p *CredentialProtector) ProtectDiagnosticBytes(body []byte, maxBytes int, authenticatedSecrets ...string) ([]byte, CredentialProtectionUnavailableReason) {
	if len(body) == 0 {
		return nil, CredentialProtectionAvailable
	}
	if maxBytes <= 0 {
		return nil, CredentialProtectionInvalidBudget
	}
	if len(body) > maxBytes {
		return nil, CredentialProtectionBudgetExceeded
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		protected := p.Snapshot(string(body), maxBytes, authenticatedSecrets...)
		if protected.UnavailableReason != CredentialProtectionAvailable {
			return nil, protected.UnavailableReason
		}
		text, ok := protected.Value.(string)
		if !ok || len(text) > maxBytes {
			return nil, CredentialProtectionFormattingFailed
		}
		return []byte(text), CredentialProtectionAvailable
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, CredentialProtectionFormattingFailed
	}
	protected := p.Snapshot(value, maxBytes, authenticatedSecrets...)
	if protected.UnavailableReason != CredentialProtectionAvailable {
		return nil, protected.UnavailableReason
	}
	encoded, err := json.Marshal(protected.Value)
	if err != nil || len(encoded) > maxBytes {
		return nil, CredentialProtectionFormattingFailed
	}
	return encoded, CredentialProtectionAvailable
}
