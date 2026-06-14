package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingLogSink struct {
	records []LogRecord
}

func (s *recordingLogSink) WriteLog(_ context.Context, record LogRecord) error {
	s.records = append(s.records, record)
	return nil
}

type testLogValuer struct{}

func (testLogValuer) LogValue() slog.Value {
	return slog.StringValue("resolved")
}

func TestLoggerNeverEmitsSecrets(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := NewWithHandler(handler)

	t.Run("never logs key_hash", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("key_hash", "sensitive-hash-value"))
		output := buf.String()
		assert.NotContains(t, output, "key_hash")
		assert.NotContains(t, output, "sensitive-hash-value")
	})

	t.Run("never logs encrypted_secret", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("encrypted_secret", "sensitive-secret"))
		output := buf.String()
		assert.NotContains(t, output, "encrypted_secret")
		assert.NotContains(t, output, "sensitive-secret")
	})

	t.Run("never logs api_key", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("api_key", "sk-1234567890"))
		output := buf.String()
		assert.NotContains(t, output, "api_key")
		assert.NotContains(t, output, "sk-1234567890")
	})

	t.Run("never logs raw_key", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("raw_key", "raw-key-value"))
		output := buf.String()
		assert.NotContains(t, output, "raw_key")
		assert.NotContains(t, output, "raw-key-value")
	})

	t.Run("never logs secret", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("secret", "my-secret"))
		output := buf.String()
		assert.NotContains(t, output, "secret")
		assert.NotContains(t, output, "my-secret")
	})

	t.Run("never logs password", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("password", "my-password"))
		output := buf.String()
		assert.NotContains(t, output, "password")
		assert.NotContains(t, output, "my-password")
	})

	t.Run("never logs token", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("token", "bearer-token-123"))
		output := buf.String()
		assert.NotContains(t, output, "token")
		assert.NotContains(t, output, "bearer-token-123")
	})

	t.Run("never logs vector", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("vector", "[0.1, 0.2, 0.3]"))
		output := buf.String()
		assert.NotContains(t, output, "vector")
		assert.NotContains(t, output, "[0.1, 0.2, 0.3]")
	})

	t.Run("never logs embedding", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("embedding", "[0.1, 0.2]"))
		output := buf.String()
		assert.NotContains(t, output, "embedding")
	})

	t.Run("never logs embeddings", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("embeddings", "[[0.1, 0.2]]"))
		output := buf.String()
		assert.NotContains(t, output, "embeddings")
	})

	t.Run("logs safe fields normally", func(t *testing.T) {
		buf.Reset()
		logger.Info("test",
			String("correlation_id", "corr-123"),
			String("client_ip", "192.168.1.1"),
			String("profile_id", "profile-456"),
			String("key_id", "key-789"),
			String("key_prefix", "dm_"),
		)
		output := buf.String()
		assert.Contains(t, output, "correlation_id")
		assert.Contains(t, output, "corr-123")
		assert.Contains(t, output, "client_ip")
		assert.Contains(t, output, "192.168.1.1")
		assert.Contains(t, output, "profile_id")
		assert.Contains(t, output, "profile-456")
		assert.Contains(t, output, "key_id")
		assert.Contains(t, output, "key-789")
		assert.Contains(t, output, "key_prefix")
		assert.Contains(t, output, "dm_")
	})

	t.Run("Error method filters secrets", func(t *testing.T) {
		buf.Reset()
		logger.Error("error occurred", assert.AnError,
			String("key_hash", "secret-hash"),
			String("safe_field", "safe-value"),
		)
		output := buf.String()
		assert.NotContains(t, output, "key_hash")
		assert.NotContains(t, output, "secret-hash")
		assert.Contains(t, output, "safe_field")
		assert.Contains(t, output, "safe-value")
	})
}

func TestLoggerConvenienceFunctions(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := NewWithHandler(handler)

	t.Run("CorrelationID helper", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", CorrelationID("test-correlation-id"))
		output := buf.String()
		assert.Contains(t, output, "correlation_id")
		assert.Contains(t, output, "test-correlation-id")
	})

	t.Run("ClientIP helper", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", ClientIP("10.0.0.1"))
		output := buf.String()
		assert.Contains(t, output, "client_ip")
		assert.Contains(t, output, "10.0.0.1")
	})

	t.Run("ProfileID helper", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", ProfileID("profile-abc"))
		output := buf.String()
		assert.Contains(t, output, "profile_id")
		assert.Contains(t, output, "profile-abc")
	})

	t.Run("KeyID helper", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", KeyID("key-xyz"))
		output := buf.String()
		assert.Contains(t, output, "key_id")
		assert.Contains(t, output, "key-xyz")
	})

	t.Run("KeyPrefix helper", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", KeyPrefix("dm_prod_"))
		output := buf.String()
		assert.Contains(t, output, "key_prefix")
		assert.Contains(t, output, "dm_prod_")
	})
}

func TestLoggerLogLevels(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := NewWithHandler(handler)

	t.Run("Info logs at info level", func(t *testing.T) {
		buf.Reset()
		logger.Info("info message", String("key", "value"))
		output := buf.String()
		assert.Contains(t, output, "info message")
		assert.Contains(t, output, `"level":"INFO"`)
	})

	t.Run("Warn logs at warn level", func(t *testing.T) {
		buf.Reset()
		logger.Warn("warn message", String("key", "value"))
		output := buf.String()
		assert.Contains(t, output, "warn message")
		assert.Contains(t, output, `"level":"WARN"`)
	})

	t.Run("Debug logs at debug level", func(t *testing.T) {
		buf.Reset()
		logger.Debug("debug message", String("key", "value"))
		output := buf.String()
		assert.Contains(t, output, "debug message")
		assert.Contains(t, output, `"level":"DEBUG"`)
	})

	t.Run("Error logs at error level", func(t *testing.T) {
		buf.Reset()
		logger.Error("error message", assert.AnError, String("key", "value"))
		output := buf.String()
		assert.Contains(t, output, "error message")
		assert.Contains(t, output, `"level":"ERROR"`)
	})
}

func TestLoggerWith(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := NewWithHandler(handler)

	t.Run("With creates logger with preset fields", func(t *testing.T) {
		childLogger := logger.With(
			String("correlation_id", "corr-123"),
			String("profile_id", "profile-456"),
		)

		buf.Reset()
		childLogger.Info("test message")
		output := buf.String()

		assert.Contains(t, output, "correlation_id")
		assert.Contains(t, output, "corr-123")
		assert.Contains(t, output, "profile_id")
		assert.Contains(t, output, "profile-456")
		assert.Contains(t, output, "test message")
	})

	t.Run("With filters secrets", func(t *testing.T) {
		childLogger := logger.With(
			String("key_hash", "secret"),
			String("safe", "value"),
		)

		buf.Reset()
		childLogger.Info("test")
		output := buf.String()

		assert.NotContains(t, output, "key_hash")
		assert.NotContains(t, output, "secret")
		assert.Contains(t, output, "safe")
		assert.Contains(t, output, "value")
	})
}

func TestOperationLogHandlerBuildsSanitizedRecords(t *testing.T) {
	sink := &recordingLogSink{}
	handler := newOperationLogHandler(slog.LevelDebug, sink).WithAttrs([]slog.Attr{
		slog.String("team_id", "team-1"),
		slog.String("api_key", "secret-key"),
	})
	pc, _, _, ok := runtime.Caller(0)
	require.True(t, ok)
	record := slog.NewRecord(time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC), slog.LevelError, "request failed", pc)
	record.AddAttrs(
		slog.String("profile_id", "profile-1"),
		slog.String("request_id", "request-1"),
		slog.Any("error", errors.New("boom")),
		slog.Group("http",
			slog.String("path", "/control/api/logs"),
			slog.String("authorization", "Bearer secret"),
		),
		slog.Any("metadata", map[string]string{
			"safe":  "visible",
			"token": "hidden",
		}),
		slog.Any("items", []any{map[string]any{"password": "hidden", "name": "visible"}}),
	)

	require.NoError(t, handler.Handle(context.Background(), record))
	require.Len(t, sink.records, 1)
	got := sink.records[0]
	assert.Equal(t, "ERROR", got.Severity)
	assert.Equal(t, 40, got.SeverityRank)
	assert.Equal(t, "request failed", got.Message)
	assert.Equal(t, "team-1", got.TeamID)
	assert.Equal(t, "profile-1", got.ProfileID)
	assert.Equal(t, "request-1", got.CorrelationID)
	assert.Equal(t, "boom", got.Error)
	assert.NotEmpty(t, got.Source)
	assert.NotContains(t, got.Attrs, "api_key")
	assert.Equal(t, map[string]any{"path": "/control/api/logs"}, got.Attrs["http"])
	assert.Equal(t, map[string]any{"safe": "visible"}, got.Attrs["metadata"])
	items, ok := got.Attrs["items"].([]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"name": "visible"}, items[0])
}

func TestNewWithSinksAndTeeHandler(t *testing.T) {
	sink := &recordingLogSink{}
	handler := newTeeHandler(
		slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}),
		newOperationLogHandler(slog.LevelDebug, sink),
	)
	logger := slog.New(handler)

	assert.True(t, handler.Enabled(context.Background(), slog.LevelError))
	assert.NoError(t, handler.WithGroup("control").Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "grouped", 0)))
	assert.False(t, newTeeHandler(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})).Enabled(context.Background(), slog.LevelDebug))
	withAttrs := handler.WithAttrs([]slog.Attr{slog.String("team_id", "team-3")})
	assert.NoError(t, withAttrs.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "with attrs", 0)))
	logger.Debug("debug message", slog.String("correlation_id", "corr-1"))

	require.Len(t, sink.records, 3)
	assert.Equal(t, "INFO", sink.records[0].Severity)
	assert.Equal(t, "team-3", sink.records[1].TeamID)
	assert.Equal(t, "DEBUG", sink.records[2].Severity)
	assert.Equal(t, "corr-1", sink.records[2].CorrelationID)

	withSink := NewWithSinks(slog.LevelInfo, sink)
	assert.NotNil(t, withSink.Slog())
	withSink.Warn("warning", String("team_id", "team-2"))
	require.Len(t, sink.records, 4)
	assert.Equal(t, "WARN", sink.records[3].Severity)
	assert.Equal(t, "team-2", sink.records[3].TeamID)

	var nilLogger *Logger
	assert.NotNil(t, nilLogger.Slog())
}

func TestLogSeverityHelpers(t *testing.T) {
	tests := []struct {
		level slog.Level
		name  string
		rank  int
	}{
		{level: slog.LevelDebug, name: "DEBUG", rank: 10},
		{level: slog.LevelInfo, name: "INFO", rank: 20},
		{level: slog.LevelWarn, name: "WARN", rank: 30},
		{level: slog.LevelError, name: "ERROR", rank: 40},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.name, severityName(tt.level))
		assert.Equal(t, tt.rank, severityRank(tt.level))
	}
	assert.Empty(t, sourceFromPC(0))
	assert.Empty(t, stringAttr(map[string]any{"team_id": 12}, "team_id"))
	assert.Empty(t, firstStringAttr(map[string]any{"a": 1}, "a", "b"))

	now := time.Date(2026, 6, 14, 12, 0, 0, 123, time.UTC)
	assert.Equal(t, int64(12), slogValueAny(slog.Int64Value(12)))
	assert.Equal(t, uint64(12), slogValueAny(slog.Uint64Value(12)))
	assert.Equal(t, 1.5, slogValueAny(slog.Float64Value(1.5)))
	assert.Equal(t, true, slogValueAny(slog.BoolValue(true)))
	assert.Equal(t, "2s", slogValueAny(slog.DurationValue(2*time.Second)))
	assert.Equal(t, now.Format(time.RFC3339Nano), slogValueAny(slog.TimeValue(now)))
	assert.Equal(t, "resolved", slogValueAny(slog.AnyValue(testLogValuer{})))
	assert.Equal(t, "boom", slogValueAny(slog.AnyValue(errors.New("boom"))))
	assert.Equal(t, []string{"a"}, slogValueAny(slog.AnyValue([]string{"a"})))
}

func TestLogProviderInterface(t *testing.T) {
	// Verify Logger implements LogProvider
	var _ LogProvider = (*Logger)(nil)
}

func TestLoggerJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := NewWithHandler(handler)

	logger.Info("test", String("correlation_id", "abc123"))

	output := buf.String()
	var result map[string]interface{}
	err := json.Unmarshal([]byte(output), &result)
	require.NoError(t, err)

	assert.Equal(t, "test", result["msg"])
	assert.Equal(t, "abc123", result["correlation_id"])
}
