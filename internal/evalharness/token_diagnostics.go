package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/markhuangai/dense-mem/internal/observability"
)

const (
	tokenDiagnosticsResponseLimit = 4 << 20
	tokenDiagnosticsOutputLimit   = 256 << 10
	tokenDiagnosticsTimeout       = 20 * time.Second
)

var tokenDiagnosticMetricNames = []string{
	"densemem_operation_provider_tokens_total",
	"densemem_ai_operation_tokens_total",
	"densemem_ai_operation_unpriced_total",
	"densemem_embedding_tokens_total",
	"densemem_verifier_tokens_total",
}

type TokenDiagnosticsOptions struct {
	MetricsURL  string
	ScrapeToken string
	Protector   *observability.CredentialProtector
}

type tokenDiagnosticSnapshot struct {
	Status     string                  `json:"status"`
	ObservedAt time.Time               `json:"observed_at"`
	Metrics    []tokenDiagnosticMetric `json:"metrics"`
}

type tokenDiagnosticMetric struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// CollectTokenDiagnostics returns only a complete, credential-protected snapshot; failures never contain upstream text.
func CollectTokenDiagnostics(ctx context.Context, opts TokenDiagnosticsOptions) ([]byte, error) {
	if opts.Protector == nil {
		return nil, errors.New("token diagnostics: credential protection is required")
	}
	if strings.TrimSpace(opts.ScrapeToken) == "" {
		return nil, errors.New("token diagnostics: telemetry scrape token is required")
	}
	endpoint, err := url.Parse(opts.MetricsURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return nil, errors.New("token diagnostics: metrics URL must be absolute HTTP(S) without credentials, query, or fragment")
	}
	protectedURL := opts.Protector.Snapshot(opts.MetricsURL, tokenDiagnosticsOutputLimit, opts.ScrapeToken)
	if protectedURL.UnavailableReason != observability.CredentialProtectionAvailable {
		return nil, errors.New("token diagnostics: credential protection unavailable")
	}
	if protectedURL.Value != opts.MetricsURL {
		return nil, errors.New("token diagnostics: metrics URL contains a configured credential")
	}
	ctx, cancel := context.WithTimeout(ctx, tokenDiagnosticsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.MetricsURL, nil)
	if err != nil {
		return nil, errors.New("token diagnostics: metrics request is invalid")
	}
	req.Header.Set("Authorization", "Bearer "+opts.ScrapeToken)
	req.Header.Set("Accept", "text/plain; version=0.0.4")
	client := &http.Client{
		Timeout:       tokenDiagnosticsTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, tokenDiagnosticsRequestError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token diagnostics: metrics request returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > tokenDiagnosticsResponseLimit {
		return nil, errors.New("token diagnostics: metrics response limit exceeded")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, tokenDiagnosticsResponseLimit+1))
	if err != nil {
		return nil, tokenDiagnosticsRequestError(err)
	}
	if len(payload) > tokenDiagnosticsResponseLimit {
		return nil, errors.New("token diagnostics: metrics response limit exceeded")
	}
	metrics, err := selectTokenDiagnosticMetrics(payload)
	if err != nil {
		return nil, err
	}
	snapshot := tokenDiagnosticSnapshot{Status: "observed", ObservedAt: time.Now().UTC(), Metrics: metrics}
	if len(metrics) == 0 {
		snapshot.Status = "empty"
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, errors.New("token diagnostics: metrics cannot be serialized")
	}
	if len(encoded) >= tokenDiagnosticsOutputLimit {
		return nil, errors.New("token diagnostics: protected output limit exceeded")
	}
	protected, reason := opts.Protector.ProtectDiagnosticBytes(encoded, tokenDiagnosticsOutputLimit-1, opts.ScrapeToken)
	if reason != observability.CredentialProtectionAvailable {
		return nil, errors.New("token diagnostics: credential protection unavailable")
	}
	// Redaction must not turn a metric name, numeric value, or label key into a different measurement.
	var checked tokenDiagnosticSnapshot
	decoder := json.NewDecoder(bytes.NewReader(protected))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checked); err != nil || checked.Status != snapshot.Status || !checked.ObservedAt.Equal(snapshot.ObservedAt) || len(checked.Metrics) != len(metrics) {
		return nil, errors.New("token diagnostics: credential protection unavailable for metric structure")
	}
	for index, metric := range checked.Metrics {
		original := metrics[index]
		if metric.Name != original.Name || metric.Value != original.Value || len(metric.Labels) != len(original.Labels) {
			return nil, errors.New("token diagnostics: credential protection unavailable for metric structure")
		}
		for key := range metric.Labels {
			if _, exists := original.Labels[key]; !exists {
				return nil, errors.New("token diagnostics: credential protection unavailable for metric structure")
			}
		}
	}
	return append(protected, '\n'), nil
}

func tokenDiagnosticsRequestError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("token diagnostics: metrics request timed out")
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("token diagnostics: metrics request cancelled")
	}
	return errors.New("token diagnostics: metrics request failed; check endpoint availability and TLS configuration")
}

func selectTokenDiagnosticMetrics(payload []byte) ([]tokenDiagnosticMetric, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("token diagnostics: malformed Prometheus metrics")
	}
	metrics := make([]tokenDiagnosticMetric, 0)
	for _, name := range tokenDiagnosticMetricNames {
		family := families[name]
		for _, sample := range family.GetMetric() {
			var value float64
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				value = sample.GetCounter().GetValue()
			case dto.MetricType_UNTYPED:
				value = sample.GetUntyped().GetValue()
			default:
				return nil, errors.New("token diagnostics: selected metric must be a counter")
			}
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, errors.New("token diagnostics: selected metric must have a finite counter value")
			}
			labels := make(map[string]string)
			for _, label := range sample.GetLabel() {
				key := label.GetName()
				switch key {
				case "component", "model", "operation", "kind", "source", "reason":
					labels[key] = label.GetValue()
				}
			}
			metrics = append(metrics, tokenDiagnosticMetric{Name: name, Labels: labels, Value: value})
		}
	}
	return metrics, nil
}
