package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

// LevelTrace and LevelFatal extend slog's standard levels without changing
// the ordering used by its handlers.
const (
	LevelTrace slog.Level = slog.Level(-8)
	LevelFatal slog.Level = slog.Level(12)

	// MaxOperationMetadataBytes bounds a single operation-log metadata value.
	MaxOperationMetadataBytes = 16 << 10
)

var (
	ErrLogSinkNil             = fmt.Errorf("log sink is nil")
	ErrLogSinkAlreadyAttached = fmt.Errorf("log sink is already attached")
)

type sinkSuppressionKey struct{}

type legacyLoggingKey struct{}

// WithSinkSuppressed marks database work performed by a sink itself. Console
// output remains available while the operation-log fan-out is skipped.
func WithSinkSuppressed(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sinkSuppressionKey{}, true)
}

func SinkSuppressed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	suppressed, _ := ctx.Value(sinkSuppressionKey{}).(bool)
	return suppressed
}

// WithAuthenticationSecrets carries presented authentication material to the
// operator logger for redaction. The values are used transiently for
// protection and are never copied into LogRecord.
func WithAuthenticationSecrets(ctx context.Context, secrets ...string) context.Context {
	return requestctx.WithAuthenticationSecrets(ctx, secrets...)
}

func AuthenticationSecretsFromContext(ctx context.Context) []string {
	return requestctx.AuthenticationSecretsFromContext(ctx)
}

func withLegacyLogging(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, legacyLoggingKey{}, true)
}

func legacyLogging(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	legacy, _ := ctx.Value(legacyLoggingKey{}).(bool)
	return legacy
}

// Logger is a structured logger wrapper that includes correlation and request context.
type Logger struct {
	logger *slog.Logger
	root   *loggerRoot
}

type loggerRoot struct {
	sink *sinkState
}

type sinkState struct {
	mu       sync.RWMutex
	sink     LogSink
	attached bool
}

// ParseLevel parses the operator-facing LOG_LEVEL value. Empty values retain
// the TRACE production default; unknown values fail startup instead of
// silently changing the configured verbosity.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	case "fatal":
		return LevelFatal, nil
	default:
		return LevelTrace, fmt.Errorf("invalid LOG_LEVEL %q", value)
	}
}

// LogProvider is the companion interface for Logger.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type LogProvider interface {
	Info(msg string, attrs ...LogAttr)
	Error(msg string, err error, attrs ...LogAttr)
	Warn(msg string, attrs ...LogAttr)
	Debug(msg string, attrs ...LogAttr)
	With(attrs ...LogAttr) LogProvider
}

// ContextLogProvider is the context-aware extension used by migrated
// producers. LogProvider remains intentionally small for staged adoption.
type ContextLogProvider interface {
	LogProvider
	Trace(msg string, attrs ...LogAttr)
	Fatal(msg string, attrs ...LogAttr)
	TraceContext(context.Context, string, ...LogAttr)
	DebugContext(context.Context, string, ...LogAttr)
	InfoContext(context.Context, string, ...LogAttr)
	WarnContext(context.Context, string, ...LogAttr)
	ErrorContext(context.Context, string, error, ...LogAttr)
	FatalContext(context.Context, string, ...LogAttr)
	LogContext(context.Context, slog.Level, string, ...LogAttr)
}

// SinkAttacher is implemented by the process root. It is kept separate from
// LogProvider so staged callers cannot accidentally construct another root.
type SinkAttacher interface {
	AttachSink(LogSink) error
}

// Ensure Logger implements LogProvider
var _ LogProvider = (*Logger)(nil)
var _ ContextLogProvider = (*Logger)(nil)

// LogAttr represents a key-value pair for structured logging.
type LogAttr struct {
	Key   string
	Value interface{}
}

// LogRecord is the sanitized application log representation sent to secondary
// sinks such as Postgres.
type LogRecord struct {
	Timestamp     time.Time
	Severity      string
	SeverityRank  int
	Message       string
	Source        string
	Function      string
	TeamID        string
	ProfileID     string
	CorrelationID string
	Error         string
	Attrs         map[string]any
	Contextual    bool `json:"-"`
}

// LogSink receives structured application log records. Implementations must not
// call back into the application logger from WriteLog.
type LogSink interface {
	WriteLog(ctx context.Context, record LogRecord) error
}

// String returns a string LogAttr.
func String(key, value string) LogAttr {
	return LogAttr{Key: key, Value: value}
}

// Int returns an int LogAttr.
func Int(key string, value int) LogAttr {
	return LogAttr{Key: key, Value: value}
}

// Bool returns a bool LogAttr.
func Bool(key string, value bool) LogAttr {
	return LogAttr{Key: key, Value: value}
}

// CorrelationID returns a correlation_id LogAttr.
func CorrelationID(value string) LogAttr {
	return LogAttr{Key: "correlation_id", Value: value}
}

// ClientIP returns a client_ip LogAttr.
func ClientIP(value string) LogAttr {
	return LogAttr{Key: "client_ip", Value: value}
}

// ProfileID returns a profile_id LogAttr.
func ProfileID(value string) LogAttr {
	return LogAttr{Key: "profile_id", Value: value}
}

// KeyID returns a key_id LogAttr.
func KeyID(value string) LogAttr {
	return LogAttr{Key: "key_id", Value: value}
}

// KeyPrefix returns a key_prefix LogAttr.
func KeyPrefix(value string) LogAttr {
	return LogAttr{Key: "key_prefix", Value: value}
}

// New creates a new Logger with the given log level.
func New(level slog.Level) *Logger {
	return NewWithProtector(level, nil)
}

// NewWithSinks creates a logger that writes JSON to stdout and sanitized records
// to any additional sinks.
func NewWithSinks(level slog.Level, sinks ...LogSink) *Logger {
	logger := NewWithProtector(level, nil)
	filtered := make([]LogSink, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			filtered = append(filtered, sink)
		}
	}
	if len(filtered) > 0 {
		_ = logger.AttachSink(fanoutLogSink(filtered))
	}
	return logger
}

type fanoutLogSink []LogSink

func (s fanoutLogSink) WriteLog(ctx context.Context, record LogRecord) error {
	var firstErr error
	for _, sink := range s {
		if err := sink.WriteLog(ctx, record); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// NewWithProtector creates one process root with a mutable sink attachment
// point. Children returned by With share that attachment point.
func NewWithProtector(level slog.Level, protector *CredentialProtector) *Logger {
	if protector == nil {
		protector = NewCredentialProtector()
	}
	state := &sinkState{}
	root := &loggerRoot{sink: state}
	console := newCredentialRedactingHandler(
		newConsoleJSONHandler(os.Stdout, level),
		protector,
	)
	operation := newOperationLogHandlerWithState(level, state, protector)
	return &Logger{
		logger: slog.New(newTeeHandler(console, operation)),
		root:   root,
	}
}

func newConsoleJSONHandler(writer io.Writer, level slog.Level) slog.Handler {
	return slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) == 0 && attr.Key == slog.LevelKey {
				if level, ok := attr.Value.Any().(slog.Level); ok {
					attr.Value = slog.StringValue(severityName(level))
					return attr
				}
				switch strings.ToUpper(attr.Value.String()) {
				case "DEBUG-4":
					attr.Value = slog.StringValue("TRACE")
				case "ERROR+4":
					attr.Value = slog.StringValue("FATAL")
				}
			}
			return attr
		},
	})
}

// NewWithSecrets creates a root configured with exact operational secrets.
// Secret values are supplied by the owning composition boundary.
func NewWithSecrets(level slog.Level, secrets ...string) *Logger {
	return NewWithProtector(level, NewCredentialProtector(secrets...))
}

// NewConsoleWithHandler creates a root-backed console adapter for isolated
// support binaries and tests. It does not attach an operation-log sink.
func NewConsoleWithHandler(handler slog.Handler) *Logger {
	state := &sinkState{}
	protector := NewCredentialProtector()
	operation := newOperationLogHandlerWithState(LevelTrace, state, protector)
	operation.legacyCompatibility = true
	return &Logger{
		logger: slog.New(newTeeHandler(newLegacyCredentialRedactingHandler(handler, protector), operation)),
		root:   &loggerRoot{sink: state},
	}
}

// NewWithHandler is retained for existing test constructors. Production
// support binaries use NewConsoleWithHandler.
func NewWithHandler(handler slog.Handler) *Logger {
	return NewConsoleWithHandler(handler)
}

// AttachSink attaches the required secondary sink exactly once. The handler
// exists from root construction, so loggers captured before attachment see it.
func (l *Logger) AttachSink(sink LogSink) error {
	if l == nil || l.root == nil || l.root.sink == nil {
		return ErrLogSinkNil
	}
	if sink == nil {
		return ErrLogSinkNil
	}
	state := l.root.sink
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.attached {
		return ErrLogSinkAlreadyAttached
	}
	state.sink = sink
	state.attached = true
	return nil
}

// SinkAttached reports whether the root has a secondary sink.
func (l *Logger) SinkAttached() bool {
	if l == nil || l.root == nil || l.root.sink == nil {
		return false
	}
	l.root.sink.mu.RLock()
	defer l.root.sink.mu.RUnlock()
	return l.root.sink.attached
}

// Slog returns the underlying slog logger for packages that require *slog.Logger.
func (l *Logger) Slog() *slog.Logger {
	if l == nil {
		return nil
	}
	return slog.New(legacyBridgeHandler{delegate: l.logger.Handler()})
}

// toSlogAttrs converts LogAttr slice to slog.Attr slice.
// Derived vectors and embeddings are retained in their dedicated stores.
func toSlogAttrs(attrs []LogAttr) []any {
	result := make([]any, 0, len(attrs)*2)
	for _, attr := range attrs {
		if isRetentionField(attr.Key) {
			continue
		}
		result = append(result, slog.Any(attr.Key, attr.Value))
	}
	return result
}

// isRetentionField identifies derived or retention-sensitive values that do not
// belong in ordinary operation metadata. Credential values are protected by
// exact configured or per-request secret matching instead of field names.
func isRetentionField(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	compact := strings.ReplaceAll(normalized, "_", "")
	return normalized == "vector" || normalized == "embedding" || normalized == "embeddings" ||
		compact == "vector" || compact == "embedding" || compact == "embeddings"
}

// Info logs an info message.
func (l *Logger) Info(msg string, attrs ...LogAttr) {
	l.logContextWithPC(withLegacyLogging(context.Background()), slog.LevelInfo, msg, callerPC(2), attrs...)
}

// Error logs an error message.
func (l *Logger) Error(msg string, err error, attrs ...LogAttr) {
	if err != nil {
		attrs = append([]LogAttr{{Key: "error", Value: err.Error()}}, attrs...)
	}
	l.logContextWithPC(withLegacyLogging(context.Background()), slog.LevelError, msg, callerPC(2), attrs...)
}

// Warn logs a warning message.
func (l *Logger) Warn(msg string, attrs ...LogAttr) {
	l.logContextWithPC(withLegacyLogging(context.Background()), slog.LevelWarn, msg, callerPC(2), attrs...)
}

// Debug logs a debug message.
func (l *Logger) Debug(msg string, attrs ...LogAttr) {
	l.logContextWithPC(withLegacyLogging(context.Background()), slog.LevelDebug, msg, callerPC(2), attrs...)
}

// Trace logs at the lowest supported operator level.
func (l *Logger) Trace(msg string, attrs ...LogAttr) {
	l.logContextWithPC(withLegacyLogging(context.Background()), LevelTrace, msg, callerPC(2), attrs...)
}

// Fatal records fatal severity. It deliberately never exits the process.
func (l *Logger) Fatal(msg string, attrs ...LogAttr) {
	l.logContextWithPC(withLegacyLogging(context.Background()), LevelFatal, msg, callerPC(2), attrs...)
}

func (l *Logger) TraceContext(ctx context.Context, msg string, attrs ...LogAttr) {
	l.logContextWithPC(ctx, LevelTrace, msg, callerPC(2), attrs...)
}

func (l *Logger) DebugContext(ctx context.Context, msg string, attrs ...LogAttr) {
	l.logContextWithPC(ctx, slog.LevelDebug, msg, callerPC(2), attrs...)
}

func (l *Logger) InfoContext(ctx context.Context, msg string, attrs ...LogAttr) {
	l.logContextWithPC(ctx, slog.LevelInfo, msg, callerPC(2), attrs...)
}

func (l *Logger) WarnContext(ctx context.Context, msg string, attrs ...LogAttr) {
	l.logContextWithPC(ctx, slog.LevelWarn, msg, callerPC(2), attrs...)
}

func (l *Logger) ErrorContext(ctx context.Context, msg string, err error, attrs ...LogAttr) {
	if err != nil {
		attrs = append([]LogAttr{{Key: "error", Value: err.Error()}}, attrs...)
	}
	l.logContextWithPC(ctx, slog.LevelError, msg, callerPC(2), attrs...)
}

func (l *Logger) FatalContext(ctx context.Context, msg string, attrs ...LogAttr) {
	l.logContextWithPC(ctx, LevelFatal, msg, callerPC(2), attrs...)
}

func (l *Logger) LogContext(ctx context.Context, level slog.Level, msg string, attrs ...LogAttr) {
	l.logContextWithPC(ctx, level, msg, callerPC(2), attrs...)
}

func (l *Logger) logContextWithPC(ctx context.Context, level slog.Level, msg string, pc uintptr, attrs ...LogAttr) {
	if ctx == nil {
		ctx = context.Background()
	}
	if l == nil || l.logger == nil {
		return
	}
	if !l.logger.Handler().Enabled(ctx, level) {
		return
	}
	returnErr := l.logger.Handler().Handle(ctx, slogRecord(time.Now(), level, msg, pc, attrs...))
	if returnErr != nil {
		// LogSink failures are recorded by the operation-log service and must not
		// change a completed business result or panic from a convenience call.
		return
	}
}

func slogRecord(timestamp time.Time, level slog.Level, msg string, pc uintptr, attrs ...LogAttr) slog.Record {
	record := slog.NewRecord(timestamp, level, msg, pc)
	for _, attr := range attrs {
		if isRetentionField(attr.Key) {
			continue
		}
		record.AddAttrs(slog.Any(attr.Key, attr.Value))
	}
	return record
}

func callerPC(skip int) uintptr {
	var pcs [1]uintptr
	runtime.Callers(skip+1, pcs[:])
	return pcs[0]
}

// With returns a new LogProvider with the given attributes pre-set.
func (l *Logger) With(attrs ...LogAttr) LogProvider {
	if l == nil || l.logger == nil {
		return New(slog.LevelInfo)
	}
	return &Logger{
		logger: l.logger.With(toSlogAttrs(attrs)...),
		root:   l.root,
	}
}

type teeHandler struct {
	handlers []slog.Handler
}

// legacyBridgeHandler marks records emitted through Slog (and therefore the
// process-wide slog.Default bridge) as compatibility traffic. Explicit root
// context APIs keep their bounded backpressure contract.
type legacyBridgeHandler struct {
	delegate slog.Handler
}

func (h legacyBridgeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.delegate.Enabled(ctx, level)
}

func (h legacyBridgeHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.delegate.Handle(withLegacyLogging(ctx), record)
}

func (h legacyBridgeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return legacyBridgeHandler{delegate: h.delegate.WithAttrs(attrs)}
}

func (h legacyBridgeHandler) WithGroup(name string) slog.Handler {
	return legacyBridgeHandler{delegate: h.delegate.WithGroup(name)}
}

func newTeeHandler(handlers ...slog.Handler) slog.Handler {
	return teeHandler{handlers: handlers}
}

func (h teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h teeHandler) Handle(ctx context.Context, record slog.Record) error {
	record = normalizeTrustedRecord(ctx, record)
	var firstErr error
	for _, handler := range h.handlers {
		if !handler.Enabled(ctx, record.Level) {
			continue
		}
		if err := handler.Handle(ctx, record.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		next = append(next, handler.WithAttrs(attrs))
	}
	return teeHandler{handlers: next}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		next = append(next, handler.WithGroup(name))
	}
	return teeHandler{handlers: next}
}

type operationLogHandler struct {
	level               slog.Level
	sink                LogSink
	state               *sinkState
	protector           *CredentialProtector
	attrs               []slog.Attr
	groups              []string
	legacyCompatibility bool
}

func newOperationLogHandler(level slog.Level, sink LogSink) slog.Handler {
	return operationLogHandler{level: level, sink: sink, protector: NewCredentialProtector()}
}

func newOperationLogHandlerWithState(level slog.Level, state *sinkState, protector *CredentialProtector) operationLogHandler {
	if protector == nil {
		protector = NewCredentialProtector()
	}
	return operationLogHandler{level: level, state: state, protector: protector}
}

func (h operationLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h operationLogHandler) Handle(ctx context.Context, record slog.Record) error {
	sink := h.sink
	if h.state != nil {
		h.state.mu.RLock()
		sink = h.state.sink
		h.state.mu.RUnlock()
	}
	if sink == nil || SinkSuppressed(ctx) {
		return nil
	}
	attrs := map[string]any{}
	secrets := AuthenticationSecretsFromContext(ctx)
	legacy := h.legacyCompatibility && legacyLogging(ctx)
	for _, attr := range h.attrs {
		h.appendAttr(attrs, attr, secrets, legacy)
	}
	record.Attrs(func(attr slog.Attr) bool {
		h.appendAttr(attrs, attr, secrets, legacy)
		return true
	})
	applyTrustedContext(ctx, attrs, h.protector)
	message := record.Message
	if legacy {
		message = legacyRedactSensitiveText(message)
	}
	if h.protector != nil {
		protectedMessage := h.protector.Snapshot(message, MaxOperationMetadataBytes, secrets...)
		if protectedMessage.UnavailableReason != CredentialProtectionAvailable {
			attrs["diagnostic_unavailable_reason"] = int(protectedMessage.UnavailableReason)
			message = "[diagnostic unavailable]"
		} else if protectedText, ok := protectedMessage.Value.(string); ok {
			message = protectedText
		}
	}
	teamID := stringAttr(attrs, "team_id")
	profileID := stringAttr(attrs, "profile_id")
	correlationID := firstStringAttr(attrs, "correlation_id", "request_id")
	errorText := stringAttr(attrs, "error")
	attrs = BoundOperationAttrs(attrs)

	logRecord := LogRecord{
		Timestamp:     record.Time.UTC(),
		Severity:      severityName(record.Level),
		SeverityRank:  severityRank(record.Level),
		Message:       message,
		Source:        sourceFromPC(record.PC),
		Function:      functionFromPC(record.PC),
		TeamID:        teamID,
		ProfileID:     profileID,
		CorrelationID: correlationID,
		Error:         errorText,
		Attrs:         attrs,
		Contextual:    !legacyLogging(ctx),
	}
	if logRecord.Timestamp.IsZero() {
		logRecord.Timestamp = time.Now().UTC()
	}
	return sink.WriteLog(ctx, logRecord)
}

// BoundOperationAttrs keeps ordinary operation metadata below the persisted
// budget while retaining trusted attribution fields for operator filtering.
func BoundOperationAttrs(attrs map[string]any) map[string]any {
	if attrs == nil {
		return map[string]any{}
	}
	encoded, err := json.Marshal(attrs)
	if err != nil {
		return map[string]any{"diagnostic_unavailable_reason": int(CredentialProtectionFormattingFailed)}
	}
	if len(encoded) > MaxOperationMetadataBytes {
		bounded := make(map[string]any, 6)
		for _, key := range []string{"team_id", "profile_id", "correlation_id", "request_id", "caller_function"} {
			if value, ok := attrs[key]; ok {
				candidate := make(map[string]any, len(bounded)+1)
				for existingKey, existingValue := range bounded {
					candidate[existingKey] = existingValue
				}
				candidate[key] = value
				candidate["diagnostic_unavailable_reason"] = int(CredentialProtectionBudgetExceeded)
				candidateJSON, candidateErr := json.Marshal(candidate)
				if candidateErr == nil && len(candidateJSON) <= MaxOperationMetadataBytes {
					bounded[key] = value
				}
			}
		}
		bounded["diagnostic_unavailable_reason"] = int(CredentialProtectionBudgetExceeded)
		return bounded
	}
	return attrs
}

func (h operationLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := operationLogHandler{
		level:               h.level,
		sink:                h.sink,
		state:               h.state,
		protector:           h.protector,
		attrs:               append([]slog.Attr{}, h.attrs...),
		groups:              append([]string{}, h.groups...),
		legacyCompatibility: h.legacyCompatibility,
	}
	next.attrs = append(next.attrs, attrs...)
	return next
}

func (h operationLogHandler) WithGroup(name string) slog.Handler {
	next := operationLogHandler{
		level:               h.level,
		sink:                h.sink,
		state:               h.state,
		protector:           h.protector,
		attrs:               append([]slog.Attr{}, h.attrs...),
		groups:              append([]string{}, h.groups...),
		legacyCompatibility: h.legacyCompatibility,
	}
	if strings.TrimSpace(name) != "" {
		next.groups = append(next.groups, name)
	}
	return next
}

func (h operationLogHandler) appendAttr(attrs map[string]any, attr slog.Attr, secrets []string, legacy bool) {
	attr.Value = attr.Value.Resolve()
	if attr.Key == "" {
		return
	}
	if isRetentionField(attr.Key) {
		return
	}
	key := attr.Key
	if len(h.groups) > 0 {
		key = strings.Join(append(append([]string{}, h.groups...), attr.Key), ".")
	}
	if legacy {
		safe, ok := legacySanitizeSlogAttr(attr)
		if !ok {
			return
		}
		attrs[key] = h.protectValue(slogValueAny(safe.Value), attrs, secrets)
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		group := map[string]any{}
		for _, child := range attr.Value.Group() {
			if child.Key == "" || isRetentionField(child.Key) {
				continue
			}
			group[child.Key] = sanitizeLogValue(child.Key, slogValueAny(child.Value.Resolve()))
		}
		attrs[key] = h.protectValue(group, attrs, secrets)
		return
	}
	attrs[key] = h.protectValue(sanitizeLogValue(key, slogValueAny(attr.Value)), attrs, secrets)
}

func (h operationLogHandler) protectValue(value any, attrs map[string]any, secrets []string) any {
	if h.protector == nil {
		return value
	}
	protected := h.protector.Snapshot(value, MaxOperationMetadataBytes, secrets...)
	if protected.UnavailableReason != CredentialProtectionAvailable {
		if _, exists := attrs["diagnostic_unavailable_reason"]; !exists {
			attrs["diagnostic_unavailable_reason"] = int(protected.UnavailableReason)
		}
		return nil
	}
	return protected.Value
}

func applyTrustedContext(ctx context.Context, attrs map[string]any, protector *CredentialProtector) {
	for key, value := range trustedContextAttrs(ctx, protector) {
		attrs[key] = value
	}
}

func trustedContextAttrs(ctx context.Context, protector *CredentialProtector) map[string]string {
	attrs := make(map[string]string, 3)
	if ctx == nil {
		return attrs
	}
	secrets := AuthenticationSecretsFromContext(ctx)
	_, hasActor := requestctx.ActorFromContext(ctx)
	if id := correlation.FromContext(ctx); id != "" &&
		(!correlation.IsClientProvided(ctx) || correlation.IsSafeClientID(id)) &&
		(!correlation.IsClientProvided(ctx) || hasActor || requestctx.AuthenticationVerifiedFromContext(ctx)) {
		attrs["correlation_id"] = protectTrustedCorrelationID(id, protector, secrets)
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok {
		if actor.TeamID != uuid.Nil {
			attrs["team_id"] = actor.TeamID.String()
		}
		if actor.OwnerID != uuid.Nil {
			attrs["profile_id"] = actor.OwnerID.String()
		}
	}
	return attrs
}

func normalizeTrustedRecord(ctx context.Context, record slog.Record) slog.Record {
	trusted := trustedContextAttrs(ctx, nil)
	if len(trusted) == 0 {
		return record
	}
	normalized := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		if _, ok := trusted[attr.Key]; ok {
			return true
		}
		normalized.AddAttrs(attr)
		return true
	})
	for key, value := range trusted {
		normalized.AddAttrs(slog.String(key, value))
	}
	return normalized
}

func slogValueAny(value slog.Value) any {
	switch value.Kind() {
	case slog.KindString:
		return value.String()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindUint64:
		return value.Uint64()
	case slog.KindFloat64:
		return value.Float64()
	case slog.KindBool:
		return value.Bool()
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindTime:
		return value.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindLogValuer:
		return slogValueAny(value.Resolve())
	case slog.KindGroup:
		group := make(map[string]any)
		for _, attr := range value.Group() {
			if attr.Key == "" {
				continue
			}
			group[attr.Key] = slogValueAny(attr.Value.Resolve())
		}
		return group
	case slog.KindAny:
		if err, ok := value.Any().(error); ok {
			return err.Error()
		}
		return value.Any()
	default:
		return value.String()
	}
}

func sanitizeLogValue(key string, value any) any {
	return sanitizeLogValueDepth(key, value, 0)
}

func sanitizeLogValueDepth(key string, value any, depth int) any {
	if isRetentionField(key) {
		return nil
	}
	if depth >= MaxCredentialProtectionDepth {
		return CredentialProtectionRedacted
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			if isRetentionField(childKey) {
				continue
			}
			out[childKey] = sanitizeLogValueDepth(childKey, childValue, depth+1)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			if isRetentionField(childKey) {
				continue
			}
			out[childKey] = childValue
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, sanitizeLogValueDepth("", child, depth+1))
		}
		return out
	case string:
		return typed
	default:
		return value
	}
}

type redactingHandler struct {
	delegate slog.Handler
}

func newRedactingHandler(delegate slog.Handler) slog.Handler {
	if delegate == nil {
		return slog.NewTextHandler(io.Discard, nil)
	}
	return redactingHandler{delegate: delegate}
}

func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.delegate.Enabled(ctx, level)
}

func (h redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	sanitized := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		if safe, ok := sanitizeSlogAttr(attr); ok {
			sanitized.AddAttrs(safe)
		}
		return true
	})
	return h.delegate.Handle(ctx, sanitized)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		if sanitized, ok := sanitizeSlogAttr(attr); ok {
			safe = append(safe, sanitized)
		}
	}
	return redactingHandler{delegate: h.delegate.WithAttrs(safe)}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{delegate: h.delegate.WithGroup(name)}
}

func severityName(level slog.Level) string {
	switch {
	case level >= LevelFatal:
		return "FATAL"
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARN"
	case level <= LevelTrace:
		return "TRACE"
	case level <= slog.LevelDebug:
		return "DEBUG"
	default:
		return "INFO"
	}
}

func severityRank(level slog.Level) int {
	switch {
	case level >= LevelFatal:
		return 50
	case level >= slog.LevelError:
		return 40
	case level >= slog.LevelWarn:
		return 30
	case level <= LevelTrace:
		return 0
	case level <= slog.LevelDebug:
		return 10
	default:
		return 20
	}
}

func sourceFromPC(pc uintptr) string {
	if pc == 0 {
		return ""
	}
	frames := runtime.CallersFrames([]uintptr{pc})
	frame, _ := frames.Next()
	if frame.File == "" {
		return ""
	}
	return frame.File + ":" + strconv.Itoa(frame.Line)
}

func functionFromPC(pc uintptr) string {
	if pc == 0 {
		return ""
	}
	frames := runtime.CallersFrames([]uintptr{pc})
	frame, _ := frames.Next()
	return frame.Function
}

func stringAttr(attrs map[string]any, key string) string {
	if value, ok := attrs[key]; ok {
		if text, ok := value.(string); ok {
			return text
		}
	}
	return ""
}

func firstStringAttr(attrs map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringAttr(attrs, key); value != "" {
			return value
		}
	}
	return ""
}
