package observability

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"unicode/utf8"
)

// DiagnosticProtector is the narrow root-owned capture seam used by
// application capabilities. It keeps retained payload policy out of services.
type DiagnosticProtector interface {
	ProtectDiagnosticBytes([]byte, int, ...string) ([]byte, CredentialProtectionUnavailableReason)
}

// DiagnosticBodyChunk fits the existing operation-log metadata budget; its hash
// and count describe the complete protected body, not the original wire bytes.
type DiagnosticBodyChunk struct {
	Index         int    `json:"index"`
	Count         int    `json:"count"`
	Body          string `json:"body"`
	SHA256        string `json:"sha256"`
	State         string `json:"state"`
	Reason        int    `json:"reason,omitempty"`
	OriginalBytes int    `json:"original_bytes"`
}

func (c DiagnosticBodyChunk) LogValue() slog.Value {
	return slog.GroupValue(slog.Int("index", c.Index), slog.Int("count", c.Count),
		slog.String("body", c.Body), slog.String("sha256", c.SHA256),
		slog.String("state", c.State), slog.Int("reason", c.Reason), slog.Int("original_bytes", c.OriginalBytes))
}

func CaptureDiagnosticBody(ctx context.Context, protector DiagnosticProtector, body []byte, maxBytes int) []DiagnosticBodyChunk {
	chunk := DiagnosticBodyChunk{Count: 1, State: "captured", OriginalBytes: len(body)}
	protected, reason := ProtectDiagnosticText(ctx, protector, body, maxBytes)
	if reason != CredentialProtectionAvailable {
		chunk.State, chunk.Reason = "unavailable", int(reason)
		return []DiagnosticBodyChunk{chunk}
	}
	return chunkDiagnosticBody(protected, chunk)
}

// ChunkProtectedDiagnosticBody formats recorder-protected bytes; callers must separately protect the accompanying metadata.
func ChunkProtectedDiagnosticBody(body []byte, maxBytes int) []DiagnosticBodyChunk {
	chunk := DiagnosticBodyChunk{Count: 1, State: "captured", OriginalBytes: len(body)}
	if len(body) > 0 {
		if maxBytes <= 0 || len(body) > maxBytes {
			chunk.State, chunk.Reason = "unavailable", int(CredentialProtectionBudgetExceeded)
			return []DiagnosticBodyChunk{chunk}
		}
		if !utf8.Valid(body) {
			chunk.State, chunk.Reason = "unavailable", int(CredentialProtectionInvalidEncoding)
			return []DiagnosticBodyChunk{chunk}
		}
	}
	return chunkDiagnosticBody(body, chunk)
}

func chunkDiagnosticBody(protected []byte, chunk DiagnosticBodyChunk) []DiagnosticBodyChunk {
	digest := sha256.Sum256(protected)
	chunk.SHA256 = hex.EncodeToString(digest[:])
	chunks := []DiagnosticBodyChunk{}
	for len(protected) > 0 {
		// JSON escaping can expand each byte sixfold; leave room for attribution.
		end := min(2048, len(protected))
		for end < len(protected) && !utf8.RuneStart(protected[end]) {
			end--
		}
		if end == 0 {
			end = min(2048, len(protected))
		}
		item := chunk
		item.Index, item.Body = len(chunks), string(protected[:end])
		chunks = append(chunks, item)
		protected = protected[end:]
	}
	if len(chunks) == 0 {
		chunks = append(chunks, chunk)
	}
	for i := range chunks {
		chunks[i].Count = len(chunks)
	}
	return chunks
}

func ProtectDiagnosticText(ctx context.Context, protector DiagnosticProtector, body []byte, maxBytes int) ([]byte, CredentialProtectionUnavailableReason) {
	if len(body) == 0 {
		return nil, CredentialProtectionAvailable
	}
	if protector == nil {
		return nil, CredentialProtectionUnsupported
	}
	if maxBytes <= 0 || len(body) > maxBytes {
		return nil, CredentialProtectionBudgetExceeded
	}
	if !utf8.Valid(body) {
		return nil, CredentialProtectionInvalidEncoding
	}
	// A JSON string preserves malformed JSON and original wire formatting.
	encoded, err := json.Marshal(string(body))
	if err != nil {
		return nil, CredentialProtectionFormattingFailed
	}
	protected, reason := protector.ProtectDiagnosticBytes(encoded, maxBytes+2, AuthenticationSecretsFromContext(ctx)...)
	if reason != CredentialProtectionAvailable {
		return nil, reason
	}
	var text string
	if err := json.Unmarshal(protected, &text); err != nil {
		return nil, CredentialProtectionFormattingFailed
	}
	if len(text) > maxBytes {
		return nil, CredentialProtectionBudgetExceeded
	}
	return []byte(text), CredentialProtectionAvailable
}

func LogDiagnosticBody(ctx context.Context, logger *slog.Logger, protector DiagnosticProtector, event string, body []byte, maxBytes int, attrs ...slog.Attr) {
	if logger == nil || !logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	ctx = WithSinkSuppressed(ctx)
	for _, chunk := range CaptureDiagnosticBody(ctx, protector, body, maxBytes) {
		fields := append(append([]slog.Attr{}, attrs...), slog.Any("payload_chunk", chunk))
		logger.LogAttrs(ctx, slog.LevelDebug, event, fields...)
	}
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
