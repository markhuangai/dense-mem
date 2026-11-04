package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/markhuangai/dense-mem/internal/observability"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/stretchr/testify/require"
)

type requestPayloadSink struct{ records []observability.LogRecord }

func (s *requestPayloadSink) WriteLog(_ context.Context, record observability.LogRecord) error {
	s.records = append(s.records, record)
	return nil
}

func TestRememberRequestLoggingStartsAfterInitialSecurityAdmission(t *testing.T) {
	for _, rejected := range []bool{true, false} {
		sink := &requestPayloadSink{}
		protector := observability.NewCredentialProtector("actual-key")
		var output bytes.Buffer
		logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
		require.NoError(t, logger.AttachSink(sink))
		processor := &rememberSynchronousProcessor{logger: logger, protector: protector}
		processor.logAdmittedRememberRequest(context.Background(), remember.RememberProcessRequest{InitialSecurityRejected: rejected, OriginalRequest: []byte(`{"secret":"admitted fact","api_key":"actual-key"}`)})
		if rejected {
			require.Empty(t, sink.records)
			require.Empty(t, output.String())
			continue
		}
		require.Empty(t, sink.records, "successful request payloads must not enter the portal log store")
		var record map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &record))
		encoded, err := json.Marshal(record)
		require.NoError(t, err)
		require.Contains(t, string(encoded), "admitted fact")
		require.NotContains(t, string(encoded), "actual-key")
		require.NotContains(t, string(encoded), "diagnostic_unavailable_reason")
	}
}
