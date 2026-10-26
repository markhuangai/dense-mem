package decisionprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecordedLogsIdentifyFailureWithoutTelemetryOrProbe(t *testing.T) {
	for _, test := range []struct {
		name, response, reason, warning string
		status, calls                   int
	}{
		{name: "authentication", status: 401, response: `{"error":"rejected"}`, reason: "authentication", calls: 1},
		{name: "rate_limit", status: 429, response: `{}`, reason: "rate_limited", warning: "decision.provider.rejected", calls: 1},
		{name: "overload", status: 529, response: `{}`, reason: "overloaded", warning: "decision.provider.rejected", calls: 1},
		{name: "context", status: 413, response: `{}`, reason: "context_rejected", warning: "decision.provider.rejected", calls: 1},
		{name: "refusal", status: 200, response: `{"model":"resolved","answers":[{"name":"safe","type":"refusal"},{"name":"position","type":"refusal"},{"name":"relevance","type":"refusal"}]}`, reason: "refusal", warning: "decision.refused", calls: 1},
		{name: "malformed_exhausted", status: 200, response: `{"model":"resolved","answers":[]}`, reason: "malformed_response", warning: "decision.validation.regenerate", calls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, "/v1/decisions", r.URL.Path)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			var logs bytes.Buffer
			_, err := fixtureProvider(t, "openai", server.URL, &logs).Decide(context.Background(), fixtureRequest())
			require.Error(t, err)
			require.EqualValues(t, test.calls, calls.Load(), "only operation exchanges are permitted")
			require.Contains(t, logs.String(), test.reason)
			decoder := json.NewDecoder(&logs)
			completed, warned := false, test.warning == ""
			for decoder.More() {
				var record map[string]any
				require.NoError(t, decoder.Decode(&record))
				event := fmt.Sprint(record["event"])
				if event == test.warning {
					require.Equal(t, "WARN", record["level"])
					warned = true
				}
				if event != "decision.exchange.completed" {
					continue
				}
				completed = true
				require.Equal(t, "ERROR", record["level"])
				require.EqualValues(t, test.status, record["status"])
				require.Equal(t, "operation-1", record["operation_id"])
				require.Equal(t, "revision-1", record["settings_revision"])
				require.Equal(t, "configured-model", record["configured_model"])
				require.Equal(t, "complete_exchange", record["action"])
				diagnostics := record["diagnostics"].(map[string]any)
				require.NotEmpty(t, diagnostics["failure_phase"])
				require.Contains(t, diagnostics, "queue_ms")
				require.Contains(t, diagnostics, "http_ms")
			}
			require.True(t, completed)
			require.True(t, warned)
		})
	}
}
