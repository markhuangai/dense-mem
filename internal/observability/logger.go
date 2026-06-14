package observability

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Logger is a structured logger wrapper that includes correlation and request context.
type Logger struct {
	logger *slog.Logger
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

// Ensure Logger implements LogProvider
var _ LogProvider = (*Logger)(nil)

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
	TeamID        string
	ProfileID     string
	CorrelationID string
	Error         string
	Attrs         map[string]any
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
	return NewWithSinks(level)
}

// NewWithSinks creates a logger that writes JSON to stdout and sanitized records
// to any additional sinks.
func NewWithSinks(level slog.Level, sinks ...LogSink) *Logger {
	handlers := []slog.Handler{
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}),
	}
	for _, sink := range sinks {
		if sink != nil {
			handlers = append(handlers, newOperationLogHandler(level, sink))
		}
	}
	handler := newTeeHandler(handlers...)
	return &Logger{
		logger: slog.New(handler),
	}
}

// NewWithHandler creates a new Logger with a custom handler.
func NewWithHandler(handler slog.Handler) *Logger {
	return &Logger{
		logger: slog.New(handler),
	}
}

// Slog returns the underlying slog logger for packages that require *slog.Logger.
func (l *Logger) Slog() *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l.logger
}

// toSlogAttrs converts LogAttr slice to slog.Attr slice.
// This function filters out sensitive fields.
func toSlogAttrs(attrs []LogAttr) []any {
	result := make([]any, 0, len(attrs)*2)
	for _, attr := range attrs {
		// Never log sensitive fields
		if isSensitiveField(attr.Key) {
			continue
		}
		result = append(result, slog.Any(attr.Key, attr.Value))
	}
	return result
}

// isSensitiveField returns true if the field should never be logged.
func isSensitiveField(key string) bool {
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
		"vector":           true,
		"embedding":        true,
		"embeddings":       true,
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

// Info logs an info message.
func (l *Logger) Info(msg string, attrs ...LogAttr) {
	l.logger.Info(msg, toSlogAttrs(attrs)...)
}

// Error logs an error message.
func (l *Logger) Error(msg string, err error, attrs ...LogAttr) {
	allAttrs := append([]LogAttr{{Key: "error", Value: err.Error()}}, attrs...)
	l.logger.Error(msg, toSlogAttrs(allAttrs)...)
}

// Warn logs a warning message.
func (l *Logger) Warn(msg string, attrs ...LogAttr) {
	l.logger.Warn(msg, toSlogAttrs(attrs)...)
}

// Debug logs a debug message.
func (l *Logger) Debug(msg string, attrs ...LogAttr) {
	l.logger.Debug(msg, toSlogAttrs(attrs)...)
}

// With returns a new LogProvider with the given attributes pre-set.
func (l *Logger) With(attrs ...LogAttr) LogProvider {
	return &Logger{
		logger: l.logger.With(toSlogAttrs(attrs)...),
	}
}

type teeHandler struct {
	handlers []slog.Handler
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
	level  slog.Level
	sink   LogSink
	attrs  []slog.Attr
	groups []string
}

func newOperationLogHandler(level slog.Level, sink LogSink) slog.Handler {
	return operationLogHandler{level: level, sink: sink}
}

func (h operationLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h operationLogHandler) Handle(ctx context.Context, record slog.Record) error {
	if h.sink == nil {
		return nil
	}
	attrs := map[string]any{}
	for _, attr := range h.attrs {
		h.appendAttr(attrs, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		h.appendAttr(attrs, attr)
		return true
	})

	logRecord := LogRecord{
		Timestamp:     record.Time.UTC(),
		Severity:      severityName(record.Level),
		SeverityRank:  severityRank(record.Level),
		Message:       record.Message,
		Source:        sourceFromPC(record.PC),
		TeamID:        stringAttr(attrs, "team_id"),
		ProfileID:     stringAttr(attrs, "profile_id"),
		CorrelationID: firstStringAttr(attrs, "correlation_id", "request_id"),
		Error:         stringAttr(attrs, "error"),
		Attrs:         attrs,
	}
	if logRecord.Timestamp.IsZero() {
		logRecord.Timestamp = time.Now().UTC()
	}
	return h.sink.WriteLog(ctx, logRecord)
}

func (h operationLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := operationLogHandler{
		level:  h.level,
		sink:   h.sink,
		attrs:  append([]slog.Attr{}, h.attrs...),
		groups: append([]string{}, h.groups...),
	}
	next.attrs = append(next.attrs, attrs...)
	return next
}

func (h operationLogHandler) WithGroup(name string) slog.Handler {
	next := operationLogHandler{
		level:  h.level,
		sink:   h.sink,
		attrs:  append([]slog.Attr{}, h.attrs...),
		groups: append([]string{}, h.groups...),
	}
	if strings.TrimSpace(name) != "" {
		next.groups = append(next.groups, name)
	}
	return next
}

func (h operationLogHandler) appendAttr(attrs map[string]any, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Key == "" {
		return
	}
	if isSensitiveField(attr.Key) {
		return
	}
	key := attr.Key
	if len(h.groups) > 0 {
		key = strings.Join(append(append([]string{}, h.groups...), attr.Key), ".")
	}
	if attr.Value.Kind() == slog.KindGroup {
		group := map[string]any{}
		for _, child := range attr.Value.Group() {
			if child.Key == "" || isSensitiveField(child.Key) {
				continue
			}
			group[child.Key] = sanitizeLogValue(child.Key, slogValueAny(child.Value.Resolve()))
		}
		attrs[key] = group
		return
	}
	attrs[key] = sanitizeLogValue(key, slogValueAny(attr.Value))
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
	if isSensitiveField(key) {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			if isSensitiveField(childKey) {
				continue
			}
			out[childKey] = sanitizeLogValue(childKey, childValue)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			if isSensitiveField(childKey) {
				continue
			}
			out[childKey] = childValue
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, sanitizeLogValue("", child))
		}
		return out
	default:
		return value
	}
}

func severityName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARN"
	case level <= slog.LevelDebug:
		return "DEBUG"
	default:
		return "INFO"
	}
}

func severityRank(level slog.Level) int {
	switch {
	case level >= slog.LevelError:
		return 40
	case level >= slog.LevelWarn:
		return 30
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
