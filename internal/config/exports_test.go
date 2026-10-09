package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestExportConfigurationDefaultsAndDestinations(t *testing.T) {
	for _, name := range []string{"OTLP_ENABLED", "AUDIT_EXPORT_ENABLED", "DIAGNOSTIC_BUNDLE_ENABLED", "OTLP_TRACE_ENDPOINT", "OTLP_METRIC_ENDPOINT", "OTLP_TRACE_HEADERS_FILE", "OTLP_METRIC_HEADERS_FILE"} {
		t.Setenv(name, "")
	}
	var cfg Config
	require.NoError(t, loadExports(&cfg))
	require.False(t, cfg.OTLPEnabled)
	require.False(t, cfg.AuditExportEnabled)
	require.False(t, cfg.DiagnosticBundleEnabled)
	t.Setenv("OTLP_ENABLED", "true")
	require.ErrorContains(t, loadExports(&cfg), "TELEMETRY_ENABLED")
	cfg.TelemetryEnabled = true
	require.Error(t, loadExports(&cfg))
	for _, endpoint := range []string{"file:///tmp/export", "https://user:secret@example.com/v1/traces", "https://example.com/v1/traces?token=secret", "https://example.com/#secret", "relative/path"} {
		t.Setenv("OTLP_TRACE_ENDPOINT", endpoint)
		err := loadExports(&cfg)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
	t.Setenv("OTLP_TRACE_ENDPOINT", "https://example.com/v1/traces")
	require.NoError(t, loadExports(&cfg))
}

func TestExportHeadersAreBoundedAndExcludedFromSerialization(t *testing.T) {
	file := filepath.Join(t.TempDir(), "headers.json")
	for _, raw := range []string{`null`, `{"Authorization":"secret\r\nInjected: true"}`, `{"Host":"example.com"}`, `{"Authorization":"one","authorization":"two"}`, `{"Bad Header":"bad"}`, strings.Repeat("a", 8193), `{"Authorization":"one"} {}`} {
		require.NoError(t, os.WriteFile(file, []byte(raw), 0600))
		_, err := readExportHeaders(file, "OTLP_TRACE_HEADERS_FILE")
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
	require.NoError(t, os.WriteFile(file, []byte(`{"Authorization":"Bearer export-secret"}`), 0600))
	headers, err := readExportHeaders(file, "OTLP_TRACE_HEADERS_FILE")
	require.NoError(t, err)
	cfg := Config{OTLPTraceHeaders: headers, OTLPTraceHeadersFile: file}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "export-secret")
	require.NotContains(t, string(raw), file)
	require.Contains(t, cfg.ExportHeaderSecrets(), "Bearer export-secret")
}

func TestExportHeaderCredentialsProtectSerializedForms(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("export-user:basic-password-canary"))
	cfg := Config{
		OTLPTraceHeaders:  map[string]string{"Authorization": "Basic " + encoded},
		OTLPMetricHeaders: map[string]string{"Cookie": "session=cookie-canary; companion=second-cookie-canary"},
	}
	protector := observability.NewCredentialProtector(cfg.ExportHeaderSecrets()...)
	for _, value := range []string{encoded, "export-user:basic-password-canary", "basic-password-canary", "cookie-canary", "second-cookie-canary"} {
		protected := protector.Snapshot(value, 1024)
		require.NotContains(t, protected.Value, value)
	}
}

func TestExportConfigurationRejectsUnavailableHeadersAndMissingDestinations(t *testing.T) {
	for _, name := range []string{"OTLP_ENABLED", "AUDIT_EXPORT_ENABLED", "DIAGNOSTIC_BUNDLE_ENABLED", "OTLP_TRACE_ENDPOINT", "OTLP_METRIC_ENDPOINT", "OTLP_TRACE_HEADERS_FILE", "OTLP_METRIC_HEADERS_FILE"} {
		t.Setenv(name, "")
	}
	t.Setenv("OTLP_ENABLED", "true")
	t.Setenv("OTLP_TRACE_ENDPOINT", "https://example.com/v1/traces")
	t.Setenv("OTLP_TRACE_HEADERS_FILE", filepath.Join(t.TempDir(), "missing.json"))
	cfg := Config{TelemetryEnabled: true}
	require.ErrorContains(t, loadExports(&cfg), "header file is unavailable")
	t.Setenv("OTLP_TRACE_HEADERS_FILE", "")
	t.Setenv("OTLP_METRIC_HEADERS_FILE", "metric-headers.json")
	require.ErrorContains(t, loadExports(&cfg), "required when a headers file is configured")
	t.Setenv("OTLP_METRIC_HEADERS_FILE", "")
	t.Setenv("AUDIT_EXPORT_ENABLED", "invalid")
	require.Error(t, loadExports(&cfg))
}
