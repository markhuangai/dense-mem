package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/requestctx"
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

type failingLogSink struct {
	err error
}

func (s failingLogSink) WriteLog(context.Context, LogRecord) error {
	return s.err
}

type testLogValuer struct{}

func (testLogValuer) LogValue() slog.Value {
	return slog.StringValue("resolved")
}

func TestLoggerPreservesAdmittedSecretLikeContentAndProtectsConfiguredSecrets(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := &Logger{
		logger: slog.New(newCredentialRedactingHandler(handler, NewCredentialProtector(
			"sensitive-hash-value", "sensitive-secret", "sk-1234567890", "raw-key-value",
			"my-secret", "my-password", "bearer-token-123", "secret-hash",
		))),
		root: &loggerRoot{sink: &sinkState{}},
	}

	t.Run("protects configured key_hash value without dropping the field", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("key_hash", "sensitive-hash-value"))
		output := buf.String()
		assert.Contains(t, output, "key_hash")
		assert.NotContains(t, output, "sensitive-hash-value")
	})

	t.Run("protects configured encrypted_secret value without dropping the field", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("encrypted_secret", "sensitive-secret"))
		output := buf.String()
		assert.Contains(t, output, "encrypted_secret")
		assert.NotContains(t, output, "sensitive-secret")
	})

	t.Run("protects configured api_key value without dropping the field", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("api_key", "sk-1234567890"))
		output := buf.String()
		assert.Contains(t, output, "api_key")
		assert.NotContains(t, output, "sk-1234567890")
	})

	t.Run("protects configured raw_key value without dropping the field", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("raw_key", "raw-key-value"))
		output := buf.String()
		assert.Contains(t, output, "raw_key")
		assert.NotContains(t, output, "raw-key-value")
	})

	t.Run("protects configured secret value without dropping the field", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("secret", "my-secret"))
		output := buf.String()
		assert.Contains(t, output, `"secret"`)
		assert.NotContains(t, output, "my-secret")
	})

	t.Run("protects configured password value without dropping the field", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("password", "my-password"))
		output := buf.String()
		assert.Contains(t, output, `"password"`)
		assert.NotContains(t, output, "my-password")
	})

	t.Run("protects configured token value without dropping the field", func(t *testing.T) {
		buf.Reset()
		logger.Info("test", String("token", "bearer-token-123"))
		output := buf.String()
		assert.Contains(t, output, `"token"`)
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

	t.Run("Error method protects configured values", func(t *testing.T) {
		buf.Reset()
		logger.Error("error occurred", assert.AnError,
			String("key_hash", "secret-hash"),
			String("safe_field", "safe-value"),
		)
		output := buf.String()
		assert.Contains(t, output, "key_hash")
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

func TestLegacyHandlerSanitizesNestedMetadataWhileContextualLoggingPreservesIt(t *testing.T) {
	var output bytes.Buffer
	logger := NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: LevelTrace}))
	sink := &recordingLogSink{}
	require.NoError(t, logger.AttachSink(sink))

	legacy := logger.Slog().WithGroup("request")
	legacy.Warn("password=legacy-password",
		slog.String("token", "legacy-token"),
		slog.Any("metadata", map[string]any{
			"safe":   "visible",
			"secret": "legacy-secret",
			"nested": map[string]any{"password": "nested-password", "name": "kept"},
		}),
		slog.Any("headers", map[string]string{
			"authorization": "Bearer legacy-bearer",
			"x-request":     "kept-header",
		}),
		slog.Any("items", []any{
			map[string]any{"token": "item-token", "name": "kept-item"},
			"cookie=item-cookie",
		}),
		slog.Group("claims", slog.String("token", "group-token"), slog.String("safe", "group-kept")),
	)

	legacyOutput := output.String()
	for _, secret := range []string{"legacy-password", "legacy-token", "legacy-secret", "nested-password", "legacy-bearer", "item-token", "item-cookie", "group-token"} {
		assert.NotContains(t, legacyOutput, secret)
	}
	assert.Contains(t, legacyOutput, "visible")
	assert.Contains(t, legacyOutput, "kept-header")
	assert.Contains(t, legacyOutput, "kept-item")
	assert.Contains(t, legacyOutput, "group-kept")
	require.Len(t, sink.records, 1)
	assert.NotContains(t, sink.records[0].Message, "legacy-password")
	assert.NotContains(t, sink.records[0].Attrs, "request.token")

	logger.InfoContext(context.Background(), "admitted password=visible-content", String("token", "admitted-token"))
	assert.Contains(t, output.String(), "visible-content")
	assert.Contains(t, output.String(), "admitted-token")
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

func TestParseLevelIsCaseInsensitiveAndStrict(t *testing.T) {
	for _, test := range []struct {
		input string
		want  slog.Level
	}{
		{"", LevelTrace},
		{"trace", LevelTrace},
		{"INFO", slog.LevelInfo},
		{"Debug", slog.LevelDebug},
		{"warning", slog.LevelWarn},
		{"ERROR", slog.LevelError},
		{"fatal", LevelFatal},
	} {
		got, err := ParseLevel(test.input)
		require.NoError(t, err)
		assert.Equal(t, test.want, got)
	}
	_, err := ParseLevel("verbose")
	assert.Error(t, err)
}

func TestRootAttachmentReachesPreexistingChildrenExactlyOnce(t *testing.T) {
	root := New(slog.LevelDebug)
	child := root.With(String("component", "early"))
	sink := &recordingLogSink{}
	child.Info("before attachment")
	require.NoError(t, root.AttachSink(sink))
	child.Info("after attachment", String("detail", "preserved"))
	assert.Len(t, sink.records, 1)
	assert.Equal(t, "after attachment", sink.records[0].Message)
	assert.Equal(t, "early", sink.records[0].Attrs["component"])
	assert.ErrorIs(t, root.AttachSink(&recordingLogSink{}), ErrLogSinkAlreadyAttached)
}

func TestRootAttachmentKeepsPreattachmentEventConsoleOnly(t *testing.T) {
	var buf bytes.Buffer
	root := NewWithHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: LevelTrace}))
	child := root.With(String("component", "early"))
	sink := &recordingLogSink{}

	child.Info("before attachment")
	assert.Contains(t, buf.String(), "before attachment")
	assert.Empty(t, sink.records)

	require.NoError(t, root.AttachSink(sink))
	assert.Empty(t, sink.records, "pre-attachment events must not be replayed")
}

func TestContextLoggerUsesTrustedActorAndCorrelation(t *testing.T) {
	root := New(slog.LevelDebug)
	sink := &recordingLogSink{}
	require.NoError(t, root.AttachSink(sink))
	teamID, ownerID := uuid.New(), uuid.New()
	ctx := correlation.WithID(context.Background(), "trusted-correlation")
	ctx = requestctx.WithActor(ctx, requestctx.Actor{TeamID: teamID, OwnerID: ownerID})
	root.InfoContext(ctx, "contextual event", String("team_id", uuid.NewString()), String("profile_id", uuid.NewString()), String("correlation_id", "untrusted"))
	require.Len(t, sink.records, 1)
	assert.Equal(t, teamID.String(), sink.records[0].TeamID)
	assert.Equal(t, ownerID.String(), sink.records[0].ProfileID)
	assert.Equal(t, "trusted-correlation", sink.records[0].CorrelationID)
}

func TestContextLoggerUsesTrustedActorAndCorrelationForConsoleAndSink(t *testing.T) {
	var buf bytes.Buffer
	state := &sinkState{}
	sink := &recordingLogSink{}
	root := &Logger{
		logger: slog.New(newTeeHandler(
			newCredentialRedactingHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: LevelTrace}), nil),
			newOperationLogHandlerWithState(LevelTrace, state, nil),
		)),
		root: &loggerRoot{sink: state},
	}
	require.NoError(t, root.AttachSink(sink))
	teamID, ownerID := uuid.New(), uuid.New()
	ctx := correlation.WithID(context.Background(), "trusted-console-correlation")
	ctx = requestctx.WithActor(ctx, requestctx.Actor{TeamID: teamID, OwnerID: ownerID})
	root.InfoContext(ctx, "contextual event", String("team_id", uuid.NewString()), String("profile_id", uuid.NewString()), String("correlation_id", "untrusted"))

	require.Len(t, sink.records, 1)
	assert.Equal(t, teamID.String(), sink.records[0].TeamID)
	assert.Equal(t, ownerID.String(), sink.records[0].ProfileID)
	assert.Equal(t, "trusted-console-correlation", sink.records[0].CorrelationID)
	var console map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &console))
	assert.Equal(t, teamID.String(), console["team_id"])
	assert.Equal(t, ownerID.String(), console["profile_id"])
	assert.Equal(t, "trusted-console-correlation", console["correlation_id"])
	assert.NotContains(t, buf.String(), "untrusted")
}

func TestTrustedCorrelationProtectionCoversCredentialsAndBounds(t *testing.T) {
	for _, test := range []struct {
		name          string
		correlationID string
		secret        string
	}{
		{name: "per request credential", correlationID: "per-call-secret", secret: "per-call-secret"},
		{name: "configured credential", correlationID: "configured-secret"},
		{name: "oversized header", correlationID: strings.Repeat("x", maxTrustedCorrelationIDRunes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var console bytes.Buffer
			state := &sinkState{}
			protector := NewCredentialProtector("configured-secret")
			sink := &recordingLogSink{}
			root := &Logger{
				logger: slog.New(newTeeHandler(
					newCredentialRedactingHandler(slog.NewJSONHandler(&console, &slog.HandlerOptions{Level: LevelTrace}), protector),
					newOperationLogHandlerWithState(LevelTrace, state, protector),
				)),
				root: &loggerRoot{sink: state},
			}
			require.NoError(t, root.AttachSink(sink))

			ctx := context.Background()
			if test.secret != "" {
				ctx = WithAuthenticationSecrets(ctx, test.secret)
			}
			ctx = correlation.WithID(ctx, test.correlationID)
			root.InfoContext(ctx, "correlation protection")

			require.Len(t, sink.records, 1)
			assert.Equal(t, CredentialProtectionRedacted, sink.records[0].CorrelationID)
			assert.NotContains(t, console.String(), test.correlationID)
		})
	}
}

func TestClientProvidedCorrelationIsSuppressedBeforeAuthentication(t *testing.T) {
	root := New(slog.LevelDebug)
	sink := &recordingLogSink{}
	require.NoError(t, root.AttachSink(sink))
	ctx := correlation.WithClientProvidedID(context.Background(), "presented-bearer")
	root.InfoContext(ctx, "pre-auth rejection")
	require.Len(t, sink.records, 1)
	assert.Empty(t, sink.records[0].CorrelationID)
}

func TestClientProvidedCorrelationIsRetainedAfterAuthenticationBinding(t *testing.T) {
	root := New(slog.LevelDebug)
	sink := &recordingLogSink{}
	require.NoError(t, root.AttachSink(sink))
	correlationID := "123e4567-e89b-12d3-a456-426614174000"
	ctx := correlation.WithClientProvidedID(context.Background(), correlationID)
	ctx = requestctx.WithAuthenticationSecrets(ctx, "presented-bearer")
	ctx = requestctx.WithAuthenticationVerified(ctx)
	root.InfoContext(ctx, "authenticated request")
	require.Len(t, sink.records, 1)
	assert.Equal(t, correlationID, sink.records[0].CorrelationID)
}

func TestClientProvidedCredentialShapedCorrelationIsSuppressedAfterAuthenticationBinding(t *testing.T) {
	root := New(slog.LevelDebug)
	sink := &recordingLogSink{}
	require.NoError(t, root.AttachSink(sink))
	ctx := correlation.WithClientProvidedID(context.Background(), "dm_live_client-b")
	ctx = requestctx.WithAuthenticationSecrets(ctx, "dm_live_client-a")
	ctx = requestctx.WithAuthenticationVerified(ctx)
	root.InfoContext(ctx, "authenticated request")
	require.Len(t, sink.records, 1)
	assert.Empty(t, sink.records[0].CorrelationID)
}

func TestClientProvidedCorrelationRejectsUnverifiedPresentedSecret(t *testing.T) {
	root := New(slog.LevelDebug)
	sink := &recordingLogSink{}
	require.NoError(t, root.AttachSink(sink))
	ctx := correlation.WithClientProvidedID(context.Background(), "different-credential")
	ctx = requestctx.WithAuthenticationSecrets(ctx, "presented-credential")
	root.InfoContext(ctx, "authentication rejected")
	require.Len(t, sink.records, 1)
	assert.Empty(t, sink.records[0].CorrelationID)
}

func TestOversizedPresentedSecretMakesDiagnosticUnavailable(t *testing.T) {
	root := New(slog.LevelDebug)
	sink := &recordingLogSink{}
	require.NoError(t, root.AttachSink(sink))
	secret := strings.Repeat("x", MaxCredentialSecretBytes+1)
	ctx := requestctx.WithAuthenticationSecrets(context.Background(), secret)
	root.InfoContext(ctx, "authentication rejected", String("detail", "safe"))
	require.Len(t, sink.records, 1)
	assert.Equal(t, "[diagnostic unavailable]", sink.records[0].Message)
	assert.NotContains(t, sink.records[0].Message, secret)
}

func TestLoggerPersistsExternalCallerFunction(t *testing.T) {
	root := New(LevelTrace)
	sink := &recordingLogSink{}
	require.NoError(t, root.AttachSink(sink))
	root.InfoContext(context.Background(), "caller attribution")
	require.Len(t, sink.records, 1)
	assert.Contains(t, sink.records[0].Function, "TestLoggerPersistsExternalCallerFunction")
}

func TestTraceAndFatalArePersistedWithDistinctSeverity(t *testing.T) {
	root := New(LevelTrace)
	sink := &recordingLogSink{}
	require.NoError(t, root.AttachSink(sink))
	root.Trace("trace event")
	root.Fatal("fatal event")
	require.Len(t, sink.records, 2)
	assert.Equal(t, "TRACE", sink.records[0].Severity)
	assert.Equal(t, "FATAL", sink.records[1].Severity)
}

func TestConsoleUsesSharedTraceAndFatalSeverityNames(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newConsoleJSONHandler(&buf, LevelTrace))
	logger.Log(context.Background(), LevelTrace, "trace console event")
	logger.Log(context.Background(), LevelFatal, "fatal console event")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], `"level":"TRACE"`)
	assert.Contains(t, lines[1], `"level":"FATAL"`)
}

func TestSlogDefaultProtectsExactSecretsAndPreservesAdmittedContent(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newCredentialRedactingHandler(
		slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
		NewCredentialProtector("hunter2", "json-secret", "bearer-secret", "hidden"),
	))
	logger.Info(`request failed password=hunter2 metadata={"token":"json-secret"}`, slog.Group("request",
		slog.String("path", "/control/api/logs?token=hunter2"),
		slog.String("authorization", "Bearer bearer-secret"),
		slog.Any("metadata", map[string]any{"safe": "visible", "nested": map[string]any{"secret": "hidden"}}),
	))
	output := buf.String()
	assert.Contains(t, output, "visible")
	assert.NotContains(t, output, "hunter2")
	assert.NotContains(t, output, "json-secret")
	assert.NotContains(t, output, "bearer-secret")
	assert.Contains(t, output, "authorization")
	assert.Contains(t, output, "token")
	assert.Contains(t, output, "secret")
}

func TestLoggerWith(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := &Logger{
		logger: slog.New(newCredentialRedactingHandler(handler, NewCredentialProtector("secret"))),
		root:   &loggerRoot{sink: &sinkState{}},
	}

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

	t.Run("With preserves fields and protects exact values", func(t *testing.T) {
		childLogger := logger.With(
			String("key_hash", "secret"),
			String("safe", "value"),
		)

		buf.Reset()
		childLogger.Info("test")
		output := buf.String()

		assert.Contains(t, output, "key_hash")
		assert.NotContains(t, output, "secret")
		assert.Contains(t, output, "safe")
		assert.Contains(t, output, "value")
	})
}

func TestOperationLogHandlerBuildsSanitizedRecords(t *testing.T) {
	sink := &recordingLogSink{}
	handler := (operationLogHandler{
		level:     slog.LevelDebug,
		sink:      sink,
		protector: NewCredentialProtector("secret-key", "Bearer secret", "hidden"),
	}).WithAttrs([]slog.Attr{
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
	assert.NotEmpty(t, got.Function)
	assert.Equal(t, "boom", got.Error)
	assert.NotEmpty(t, got.Source)
	assert.Equal(t, CredentialProtectionRedacted, got.Attrs["api_key"])
	assert.Equal(t, map[string]any{"path": "/control/api/logs", "authorization": CredentialProtectionRedacted}, got.Attrs["http"])
	assert.Equal(t, map[string]any{"safe": "visible", "token": CredentialProtectionRedacted}, got.Attrs["metadata"])
	items, ok := got.Attrs["items"].([]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"name": "visible", "password": CredentialProtectionRedacted}, items[0])
}

func TestOperationLogMetadataIsBoundAsOnePayload(t *testing.T) {
	sink := &recordingLogSink{}
	root := NewWithProtector(slog.LevelDebug, nil)
	require.NoError(t, root.AttachSink(sink))
	root.Info("large metadata", String("a", strings.Repeat("x", MaxOperationMetadataBytes/2)), String("b", strings.Repeat("y", MaxOperationMetadataBytes/2)))
	require.Len(t, sink.records, 1)
	assert.Equal(t, int(CredentialProtectionBudgetExceeded), sink.records[0].Attrs["diagnostic_unavailable_reason"])
}

func TestOperationLogMetadataBoundsRetainTrustedAttribution(t *testing.T) {
	sink := &recordingLogSink{}
	root := New(LevelTrace)
	require.NoError(t, root.AttachSink(sink))
	teamID, ownerID := uuid.New(), uuid.New()
	ctx := correlation.WithID(context.Background(), "trusted-correlation")
	ctx = requestctx.WithActor(ctx, requestctx.Actor{TeamID: teamID, OwnerID: ownerID})
	root.InfoContext(ctx, "large metadata", String("ordinary", strings.Repeat("x", MaxOperationMetadataBytes*2)))
	require.Len(t, sink.records, 1)
	assert.Equal(t, teamID.String(), sink.records[0].TeamID)
	assert.Equal(t, ownerID.String(), sink.records[0].ProfileID)
	assert.Equal(t, "trusted-correlation", sink.records[0].CorrelationID)
	encoded, err := json.Marshal(sink.records[0].Attrs)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), MaxOperationMetadataBytes)
}

func TestContextAuthenticationSecretsAreProtectedAtSink(t *testing.T) {
	sink := &recordingLogSink{}
	root := NewWithSecrets(slog.LevelDebug, "configured-secret")
	require.NoError(t, root.AttachSink(sink))
	ctx := WithAuthenticationSecrets(context.Background(), "per-call-secret")
	root.InfoContext(ctx, "provider failed per-call-secret", String("detail", "per-call-secret"))
	require.Len(t, sink.records, 1)
	assert.NotContains(t, sink.records[0].Message, "per-call-secret")
	assert.NotContains(t, sink.records[0].Attrs["detail"], "per-call-secret")
}

func TestCredentialRedactingHandlerProtectsPerCallSecretInBoundAttributes(t *testing.T) {
	var buf bytes.Buffer
	handler := newCredentialRedactingHandler(
		slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: LevelTrace}),
		NewCredentialProtector("configured-secret"),
	)
	bound := slog.New(legacyBridgeHandler{delegate: handler}).With(slog.String("detail", "per-call-secret"))
	ctx := WithAuthenticationSecrets(context.Background(), "per-call-secret")
	bound.InfoContext(ctx, "bound attribute")

	output := buf.String()
	assert.NotContains(t, output, "per-call-secret")
	assert.Contains(t, output, CredentialProtectionRedacted)
}

func TestOperationLogHandlerReturnsSinkErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	handler := newOperationLogHandler(slog.LevelDebug, failingLogSink{err: writeErr})

	err := handler.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "persist me", 0))

	require.ErrorIs(t, err, writeErr)
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

	secondSink := &recordingLogSink{}
	withSink := NewWithSinks(slog.LevelInfo, sink, secondSink)
	assert.NotNil(t, withSink.Slog())
	withSink.Warn("warning", String("team_id", "team-2"))
	require.Len(t, sink.records, 4)
	require.Len(t, secondSink.records, 1)
	assert.Equal(t, "WARN", sink.records[3].Severity)
	assert.Equal(t, "team-2", sink.records[3].TeamID)
	assert.Equal(t, "WARN", secondSink.records[0].Severity)

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

func TestLoggerContextLevelsAndSinkGuards(t *testing.T) {
	root := New(LevelTrace)
	sink := &recordingLogSink{}
	require.NoError(t, root.AttachSink(sink))
	ctx := context.Background()
	root.TraceContext(ctx, "trace")
	root.DebugContext(ctx, "debug")
	root.InfoContext(ctx, "info")
	root.WarnContext(ctx, "warn")
	root.ErrorContext(ctx, "error", errors.New("failure"))
	root.FatalContext(ctx, "fatal")
	root.LogContext(ctx, slog.LevelInfo+1, "custom")
	require.Len(t, sink.records, 7)
	assert.Equal(t, []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL", "INFO"}, []string{
		sink.records[0].Severity,
		sink.records[1].Severity,
		sink.records[2].Severity,
		sink.records[3].Severity,
		sink.records[4].Severity,
		sink.records[5].Severity,
		sink.records[6].Severity,
	})
	assert.Equal(t, "failure", sink.records[4].Error)

	var nilLogger *Logger
	assert.ErrorIs(t, nilLogger.AttachSink(sink), ErrLogSinkNil)
	fresh := New(LevelTrace)
	assert.ErrorIs(t, fresh.AttachSink(nil), ErrLogSinkNil)
	assert.ErrorIs(t, root.AttachSink(&recordingLogSink{}), ErrLogSinkAlreadyAttached)
	assert.False(t, nilLogger.SinkAttached())
	assert.NotNil(t, nilLogger.With(String("ignored", "value")))
	nilLogger.Info("nil logger remains non-panicking")

	quiet := New(slog.LevelInfo)
	quiet.DebugContext(ctx, "filtered")
	quiet.InfoContext(ctx, "accepted")
	assert.False(t, quiet.SinkAttached())
}

func TestLoggerContextAndHandlerCompatibilityBranches(t *testing.T) {
	var nilCtx context.Context
	assert.False(t, SinkSuppressed(nilCtx))
	assert.True(t, SinkSuppressed(WithSinkSuppressed(nilCtx)))
	assert.False(t, legacyLogging(nilCtx))
	assert.True(t, legacyLogging(withLegacyLogging(nilCtx)))

	sink := &recordingLogSink{}
	root := NewWithSecrets(LevelTrace, "configured-secret")
	require.NoError(t, root.AttachSink(sink))
	compat := root.Slog().With(slog.String("safe", "visible"), slog.String("token", "hidden")).WithGroup("request")
	compat.Info("password=configured-secret")
	require.Len(t, sink.records, 1)
	assert.Equal(t, "visible", sink.records[0].Attrs["request.safe"])
	assert.NotContains(t, sink.records[0].Message, "configured-secret")

	var nilHandler slog.Handler
	assert.NotNil(t, newRedactingHandler(nilHandler))
	assert.NotNil(t, newCredentialRedactingHandler(nilHandler, nil))
	assert.Nil(t, legacySanitizeLogValue("token", "hidden"))
	assert.Equal(t, 42, legacySanitizeLogValue("count", 42))
	assert.Equal(t, CredentialProtectionRedacted, sanitizeLogValueDepth("detail", "hidden", MaxCredentialProtectionDepth))
	assert.Equal(t, CredentialProtectionRedacted, legacySanitizeLogValueDepth("detail", "hidden", MaxCredentialProtectionDepth))
	assert.NotNil(t, newLegacyCredentialRedactingHandler(nilHandler, nil))
	_, ok := sanitizeSlogAttr(slog.Attr{})
	assert.False(t, ok)
	_, ok = sanitizeSlogAttr(slog.String("vector", "[0.1]"))
	assert.False(t, ok)
	var compatibilityOutput bytes.Buffer
	compatibilityHandler := newRedactingHandler(slog.NewJSONHandler(&compatibilityOutput, &slog.HandlerOptions{Level: LevelTrace}))
	assert.True(t, compatibilityHandler.Enabled(context.Background(), LevelTrace))
	require.NoError(t, compatibilityHandler.Handle(context.Background(), slog.NewRecord(time.Now(), LevelTrace, "compatibility", 0)))
	groupedCompatibility := compatibilityHandler.WithAttrs([]slog.Attr{slog.String("safe", "value")}).WithGroup("compatibility")
	require.NoError(t, groupedCompatibility.Handle(context.Background(), slog.NewRecord(time.Now(), LevelTrace, "grouped", 0)))
	assert.Contains(t, compatibilityOutput.String(), "value")

	state := &sinkState{}
	handler := newOperationLogHandlerWithState(LevelTrace, state, NewCredentialProtector("secret"))
	assert.NoError(t, handler.Handle(WithSinkSuppressed(context.Background()), slog.NewRecord(time.Time{}, slog.LevelInfo, "suppressed", 0)))
	assert.NoError(t, handler.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelInfo, "no sink", 0)))

	bounded := BoundOperationAttrs(map[string]any{"unsupported": func() {}})
	assert.Equal(t, int(CredentialProtectionFormattingFailed), bounded["diagnostic_unavailable_reason"])
	largeSink := &recordingLogSink{}
	largeRoot := NewWithSecrets(LevelTrace, "secret")
	require.NoError(t, largeRoot.AttachSink(largeSink))
	largeRoot.Info("large", String("detail", strings.Repeat("x", MaxOperationMetadataBytes*2)))
	require.Len(t, largeSink.records, 1)
	assert.Equal(t, int(CredentialProtectionBudgetExceeded), largeSink.records[0].Attrs["diagnostic_unavailable_reason"])
}

func TestLoggerBoundsUnsupportedDiagnosticsAndNilProtectorSink(t *testing.T) {
	var output bytes.Buffer
	handler := newCredentialRedactingHandler(
		slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: LevelTrace}),
		NewCredentialProtector("secret"),
	)
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	record := slog.NewRecord(time.Now(), slog.LevelInfo, strings.Repeat("x", MaxOperationMetadataBytes*2), 0)
	record.AddAttrs(slog.Any("cycle", cyclic))
	require.NoError(t, handler.Handle(context.Background(), record))
	assert.Contains(t, output.String(), CredentialProtectionRedacted)

	sink := &recordingLogSink{}
	operation := operationLogHandler{level: LevelTrace, sink: sink}
	require.NoError(t, operation.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "without protector", 0)))
	require.Len(t, sink.records, 1)
	assert.Equal(t, "without protector", sink.records[0].Message)
	legacySink := &recordingLogSink{}
	legacyOperation := operationLogHandler{level: LevelTrace, sink: legacySink, legacyCompatibility: true}
	legacyRecord := slog.NewRecord(time.Now(), slog.LevelWarn, "password=legacy", 0)
	legacyRecord.AddAttrs(slog.String("token", "legacy-token"), slog.String("safe", "kept"))
	require.NoError(t, legacyOperation.Handle(withLegacyLogging(context.Background()), legacyRecord))
	require.Len(t, legacySink.records, 1)
	assert.NotContains(t, legacySink.records[0].Message, "legacy")
	assert.Equal(t, "kept", legacySink.records[0].Attrs["safe"])
}

func TestSeverityHelpersIncludeExtendedLevels(t *testing.T) {
	for _, test := range []struct {
		level slog.Level
		name  string
		rank  int
	}{
		{LevelTrace, "TRACE", 0},
		{slog.LevelDebug, "DEBUG", 10},
		{slog.LevelInfo, "INFO", 20},
		{slog.LevelWarn, "WARN", 30},
		{slog.LevelError, "ERROR", 40},
		{LevelFatal, "FATAL", 50},
	} {
		assert.Equal(t, test.name, severityName(test.level))
		assert.Equal(t, test.rank, severityRank(test.level))
	}
	assert.Equal(t, "INFO", severityName(slog.LevelInfo+1))
	assert.Equal(t, 20, severityRank(slog.LevelInfo+1))
}
