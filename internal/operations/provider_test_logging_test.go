package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestProviderTestRealProviderLogSeveritiesAndFiltering(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		t.Run(level.String(), func(t *testing.T) {
			var output bytes.Buffer
			var mode atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer configured-provider-test-key", r.Header.Get("Authorization"))
				switch mode.Load() {
				case 1:
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, `{"error":{"message":"configured-provider-test-key"}}`)
				case 2:
					w.Header().Set("Content-Length", "1000")
					_, _ = io.WriteString(w, `{"partial":`)
				default:
					_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[0.25,0.75]}]}`)
				}
			}))
			defer server.Close()
			logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level}))
			protector := observability.NewCredentialProtector("configured-provider-test-key")
			client := observability.DiagnosticHTTPClient(server.Client(), logger, protector)
			cfg := config.Config{AIAPIURL: server.URL, AIAPIKey: "configured-provider-test-key", AIEmbeddingModel: "fixture", AIEmbeddingDimensions: 2}
			provider := embedding.NewOpenAIEmbeddingProvider(&cfg, client)
			service := NewProviderTestService(func(context.Context) ([]ProviderTestBinding, error) {
				return []ProviderTestBinding{{Target: ProviderTestTarget{Endpoint: server.URL + "/embeddings", Model: "fixture", Operation: "embed", Available: true, TimeoutMS: 2000}, Execute: func(ctx context.Context) (*modelprovider.CallDiagnostics, error) {
					diagnostics := &modelprovider.CallDiagnostics{}
					_, _, err := provider.Embed(modelprovider.WithQueueDiagnostics(ctx, diagnostics), "Dense-Mem provider connectivity sample.")
					return diagnostics, err
				}}}, nil
			}, logger, protector, "fixture-server")
			targets, err := service.List(context.Background())
			require.NoError(t, err)
			target := targets[0]
			for _, state := range []string{"success", "rejection", "interrupted"} {
				output.Reset()
				mode.Store(0)
				if state == "rejection" {
					mode.Store(1)
				}
				if state == "interrupted" {
					mode.Store(2)
				}
				result, err := service.Test(context.Background(), target.ID, "control_portal:token")
				require.NoError(t, err)
				require.Equal(t, level == slog.LevelDebug, result.DetailedLogsAvailable)
				if state == "interrupted" {
					require.Equal(t, "response_read_failed", result.Reason)
					require.Equal(t, "incomplete", result.CaptureState)
				}
				require.NotContains(t, output.String(), "configured-provider-test-key")
				events := map[string][]string{}
				for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
					if len(line) == 0 {
						continue
					}
					var record struct {
						Message string `json:"msg"`
						Level   string `json:"level"`
					}
					require.NoError(t, json.Unmarshal(line, &record))
					events[record.Message] = append(events[record.Message], record.Level)
				}
				if level <= slog.LevelInfo {
					require.Equal(t, []string{"INFO"}, events["provider_test.started"])
				} else {
					require.Empty(t, events["provider_test.started"])
				}
				if state == "success" {
					if level <= slog.LevelInfo {
						require.Equal(t, []string{"INFO"}, events["provider_test.completed"])
					} else {
						require.Empty(t, events["provider_test.completed"])
					}
				} else {
					require.Equal(t, []string{"ERROR"}, events["provider_test.completed"])
				}
				if state == "interrupted" && level <= slog.LevelWarn {
					require.Equal(t, []string{"WARN"}, events["provider_test.capture_incomplete"])
				} else {
					require.Empty(t, events["provider_test.capture_incomplete"])
				}
				if level == slog.LevelDebug {
					require.Equal(t, []string{"DEBUG"}, events["provider_test.exchange"])
					require.Equal(t, []string{"DEBUG", "DEBUG"}, events["ai_exchange_payload"])
				} else {
					require.Empty(t, events["provider_test.exchange"])
					require.Empty(t, events["ai_exchange_payload"])
				}
			}
		})
	}
}
