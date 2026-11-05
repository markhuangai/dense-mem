package observability

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"time"
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
	ReasonCode    string `json:"reason_code,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	LimitUnit     string `json:"limit_unit,omitempty"`
	OriginalBytes int    `json:"original_bytes"`
}

func (c DiagnosticBodyChunk) LogValue() slog.Value {
	return slog.GroupValue(slog.Int("index", c.Index), slog.Int("count", c.Count),
		slog.String("body", c.Body), slog.String("sha256", c.SHA256),
		slog.String("state", c.State), slog.Int("reason", c.Reason), slog.Int("original_bytes", c.OriginalBytes),
		slog.String("reason_code", c.ReasonCode), slog.Int("limit", c.Limit), slog.String("limit_unit", c.LimitUnit))
}

func CaptureDiagnosticBody(ctx context.Context, protector DiagnosticProtector, body []byte, maxBytes int) []DiagnosticBodyChunk {
	chunk := DiagnosticBodyChunk{Count: 1, State: "captured", OriginalBytes: len(body)}
	protected, reason := ProtectDiagnosticText(ctx, protector, body, maxBytes)
	if reason != CredentialProtectionAvailable {
		chunk.State, chunk.Reason = "unavailable", int(reason)
		chunk.ReasonCode = reason.Code()
		chunk.LimitUnit, chunk.Limit = reason.diagnosticLimit(maxBytes)
		return []DiagnosticBodyChunk{chunk}
	}
	return chunkDiagnosticBody(protected, chunk)
}

// ChunkProtectedDiagnosticBody formats recorder-protected bytes; callers must separately protect the accompanying metadata.
func ChunkProtectedDiagnosticBody(body []byte, maxBytes int) []DiagnosticBodyChunk {
	chunk := DiagnosticBodyChunk{Count: 1, State: "captured", OriginalBytes: len(body)}
	if len(body) > 0 {
		if maxBytes <= 0 || len(body) > maxBytes {
			chunk.State, chunk.Reason = "unavailable", int(CredentialProtectionBodyBudgetExceeded)
			chunk.ReasonCode = CredentialProtectionBodyBudgetExceeded.Code()
			chunk.LimitUnit, chunk.Limit = "body_bytes", max(0, maxBytes)
			return []DiagnosticBodyChunk{chunk}
		}
		if !utf8.Valid(body) {
			chunk.State, chunk.Reason = "unavailable", int(CredentialProtectionInvalidEncoding)
			chunk.ReasonCode = CredentialProtectionInvalidEncoding.Code()
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
		return nil, CredentialProtectionBodyBudgetExceeded
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
		return nil, CredentialProtectionBodyBudgetExceeded
	}
	return []byte(text), CredentialProtectionAvailable
}

func LogDiagnosticBody(ctx context.Context, logger *slog.Logger, protector DiagnosticProtector, event string, body []byte, maxBytes int, attrs ...slog.Attr) {
	if logger == nil || !logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	ctx = WithSinkSuppressed(ctx)
	started := time.Now()
	chunks := CaptureDiagnosticBody(ctx, protector, body, maxBytes)
	for _, chunk := range chunks {
		fields := append(append([]slog.Attr{}, attrs...), slog.Any("payload_chunk", chunk))
		logger.LogAttrs(ctx, slog.LevelDebug, event, fields...)
		if chunk.State == "unavailable" {
			fields := append(append([]slog.Attr{}, attrs...), slog.String("capture_event", event),
				slog.String("reason_code", chunk.ReasonCode), slog.Int("reason", chunk.Reason),
				slog.Int("original_bytes", chunk.OriginalBytes), slog.Int("limit", chunk.Limit),
				slog.String("limit_unit", chunk.LimitUnit), slog.Float64("protection_ms", float64(time.Since(started))/float64(time.Millisecond)))
			logger.LogAttrs(ctx, slog.LevelWarn, "diagnostic_body_unavailable", fields...)
		}
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
		return nil, CredentialProtectionEncodedBudgetExceeded
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
