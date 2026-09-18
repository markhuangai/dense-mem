package observability

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxTrustedCorrelationIDRunes = 128

var (
	legacyBearerSecretPattern   = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`)
	legacyKeyValueSecretPattern = regexp.MustCompile(`(?i)((?:password|secret|token|api[_-]?key|authorization|cookie)\s*"?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
)

// legacySensitiveField preserves the field-name filtering required by the
// context-free compatibility logger. Contextual calls use exact configured or
// per-request secret matching and intentionally keep admitted content.
func legacySensitiveField(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	compact := strings.ReplaceAll(normalized, "_", "")
	sensitive := map[string]bool{
		"access_token":     true,
		"accesstoken":      true,
		"authorization":    true,
		"key_hash":         true,
		"encrypted_secret": true,
		"api_key":          true,
		"apikey":           true,
		"raw_key":          true,
		"refresh_token":    true,
		"refreshtoken":     true,
		"secret":           true,
		"password":         true,
		"token":            true,
	}
	return sensitive[normalized] ||
		sensitive[compact] ||
		strings.HasSuffix(normalized, "_api_key") ||
		strings.HasSuffix(normalized, "_password") ||
		strings.HasSuffix(normalized, "_secret") ||
		strings.HasSuffix(normalized, "_token") ||
		strings.HasSuffix(compact, "apikey") ||
		strings.HasSuffix(compact, "password") ||
		strings.HasSuffix(compact, "secret") ||
		strings.HasSuffix(compact, "token")
}

func legacyRedactSensitiveText(value string) string {
	value = legacyBearerSecretPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	return legacyKeyValueSecretPattern.ReplaceAllString(value, `${1}[REDACTED]`)
}

func legacySanitizeLogValue(key string, value any) any {
	return legacySanitizeLogValueDepth(key, value, 0)
}

func legacySanitizeLogValueDepth(key string, value any, depth int) any {
	if legacySensitiveField(key) {
		return nil
	}
	if depth >= MaxCredentialProtectionDepth {
		return CredentialProtectionRedacted
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			if legacySensitiveField(childKey) {
				continue
			}
			out[childKey] = legacySanitizeLogValueDepth(childKey, childValue, depth+1)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			if legacySensitiveField(childKey) {
				continue
			}
			out[childKey] = legacyRedactSensitiveText(childValue)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, legacySanitizeLogValueDepth("", child, depth+1))
		}
		return out
	case string:
		return legacyRedactSensitiveText(typed)
	default:
		return value
	}
}

func legacySanitizeSlogAttr(attr slog.Attr) (slog.Attr, bool) {
	attr.Value = attr.Value.Resolve()
	if attr.Key == "" || legacySensitiveField(attr.Key) {
		return slog.Attr{}, false
	}
	if attr.Value.Kind() == slog.KindGroup {
		children := make([]slog.Attr, 0)
		for _, child := range attr.Value.Group() {
			if safe, ok := legacySanitizeSlogAttr(child); ok {
				children = append(children, safe)
			}
		}
		return slog.Group(attr.Key, attrsToAny(children)...), true
	}
	return slog.Any(attr.Key, legacySanitizeLogValue(attr.Key, slogValueAny(attr.Value))), true
}

// credentialRedactingHandler applies exact configured-secret protection while
// preserving admitted content whose field names merely resemble credentials.
type credentialRedactingHandler struct {
	delegate            slog.Handler
	protector           *CredentialProtector
	attrs               []slog.Attr
	legacyCompatibility bool
}

func newCredentialRedactingHandler(delegate slog.Handler, protector *CredentialProtector) slog.Handler {
	if delegate == nil {
		return slog.NewTextHandler(io.Discard, nil)
	}
	if protector == nil {
		protector = NewCredentialProtector()
	}
	return credentialRedactingHandler{delegate: delegate, protector: protector}
}

func newLegacyCredentialRedactingHandler(delegate slog.Handler, protector *CredentialProtector) slog.Handler {
	handler := newCredentialRedactingHandler(delegate, protector)
	redactor, ok := handler.(credentialRedactingHandler)
	if !ok {
		return handler
	}
	redactor.legacyCompatibility = true
	return redactor
}

func (h credentialRedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.delegate.Enabled(ctx, level)
}

func (h credentialRedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	secrets := AuthenticationSecretsFromContext(ctx)
	trusted := trustedContextAttrs(ctx, h.protector)
	legacy := h.legacyCompatibility && legacyLogging(ctx)
	sanitized := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	message := sanitized.Message
	if legacy {
		message = legacyRedactSensitiveText(message)
	}
	if protected := h.protector.Snapshot(message, MaxOperationMetadataBytes, secrets...); protected.UnavailableReason == CredentialProtectionAvailable {
		if message, ok := protected.Value.(string); ok {
			sanitized = slog.NewRecord(record.Time, record.Level, message, record.PC)
		}
	} else {
		sanitized = slog.NewRecord(record.Time, record.Level, "[diagnostic unavailable]", record.PC)
	}
	for _, attr := range h.attrs {
		if _, ok := trusted[attr.Key]; ok {
			continue
		}
		if safe, ok := sanitizeSlogAttrWithProtector(attr, h.protector, secrets, legacy); ok {
			sanitized.AddAttrs(safe)
		}
	}
	record.Attrs(func(attr slog.Attr) bool {
		if _, ok := trusted[attr.Key]; ok {
			return true
		}
		if safe, ok := sanitizeSlogAttrWithProtector(attr, h.protector, secrets, legacy); ok {
			sanitized.AddAttrs(safe)
		}
		return true
	})
	for key, value := range trusted {
		sanitized.AddAttrs(slog.String(key, value))
	}
	return h.delegate.Handle(ctx, sanitized)
}

func protectTrustedCorrelationID(id string, protector *CredentialProtector, secrets []string) string {
	if utf8.RuneCountInString(id) > maxTrustedCorrelationIDRunes {
		return CredentialProtectionRedacted
	}
	if protector == nil {
		return id
	}
	protected := protector.Snapshot(id, MaxOperationMetadataBytes, secrets...)
	if protected.UnavailableReason != CredentialProtectionAvailable {
		return CredentialProtectionRedacted
	}
	value, ok := protected.Value.(string)
	if !ok {
		return CredentialProtectionRedacted
	}
	return value
}

func (h credentialRedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	bound := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	bound = append(bound, h.attrs...)
	bound = append(bound, attrs...)
	return credentialRedactingHandler{delegate: h.delegate, protector: h.protector, attrs: bound, legacyCompatibility: h.legacyCompatibility}
}

func (h credentialRedactingHandler) WithGroup(name string) slog.Handler {
	return credentialRedactingHandler{
		delegate:            h.delegate.WithGroup(name),
		protector:           h.protector,
		attrs:               append([]slog.Attr(nil), h.attrs...),
		legacyCompatibility: h.legacyCompatibility,
	}
}

func sanitizeSlogAttrWithProtector(attr slog.Attr, protector *CredentialProtector, secrets []string, legacy bool) (slog.Attr, bool) {
	safe, ok := sanitizeSlogAttr(attr)
	if legacy {
		safe, ok = legacySanitizeSlogAttr(attr)
	}
	if !ok || protector == nil {
		return safe, ok
	}
	protected := protector.Snapshot(slogValueAny(safe.Value.Resolve()), MaxOperationMetadataBytes, secrets...)
	if protected.UnavailableReason != CredentialProtectionAvailable {
		return slog.String(safe.Key, CredentialProtectionRedacted), true
	}
	return slog.Any(safe.Key, protected.Value), true
}

func sanitizeSlogAttr(attr slog.Attr) (slog.Attr, bool) {
	attr.Value = attr.Value.Resolve()
	if attr.Key == "" || isRetentionField(attr.Key) {
		return slog.Attr{}, false
	}
	if attr.Value.Kind() == slog.KindGroup {
		children := make([]slog.Attr, 0)
		for _, child := range attr.Value.Group() {
			if safe, ok := sanitizeSlogAttr(child); ok {
				children = append(children, safe)
			}
		}
		return slog.Group(attr.Key, attrsToAny(children)...), true
	}
	return slog.Any(attr.Key, sanitizeLogValue(attr.Key, slogValueAny(attr.Value))), true
}

func attrsToAny(attrs []slog.Attr) []any {
	out := make([]any, 0, len(attrs)*2)
	for _, attr := range attrs {
		out = append(out, attr)
	}
	return out
}
