package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

type Client struct {
	baseURL string
	client  *http.Client
}

func NewClient(baseURL string, timeout time.Duration) operationscontract.TelemetryQuerier {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	return &Client{baseURL: baseURL, client: &http.Client{Timeout: operationscontract.NormalizeTelemetryQueryTimeout(timeout)}}
}

func wrapTelemetryQueryError(reason string, cause error) error {
	return &operationscontract.TelemetryQueryError{Reason: reason, Cause: cause}
}

func queryFailureReason(err error) string {
	if reason := operationscontract.TelemetryQueryFailureReason(err); reason != "transport_failed" {
		return reason
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "returned status"):
		return "http_status"
	case strings.Contains(message, "query_range failed"), strings.Contains(message, "query failed"):
		return "prometheus_api_error"
	case strings.Contains(message, "invalid character"), strings.Contains(message, "cannot unmarshal"), strings.Contains(message, "decode"):
		return "response_decode_failed"
	default:
		return "transport_failed"
	}
}

func (s *Client) Range(ctx context.Context, query string, from, to time.Time, step time.Duration) ([]operationscontract.TelemetryPoint, error) {
	endpoint, err := url.Parse(s.baseURL + "/api/v1/query_range")
	if err != nil {
		return nil, wrapTelemetryQueryError("request_url_invalid", err)
	}
	params := endpoint.Query()
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(from.Unix(), 10))
	params.Set("end", strconv.FormatInt(to.Unix(), 10))
	params.Set("step", strconv.FormatInt(int64(step.Seconds()), 10))
	endpoint.RawQuery = params.Encode()

	var resp prometheusRangeResponse
	if err := s.get(ctx, endpoint.String(), &resp); err != nil {
		return nil, wrapTelemetryQueryError(queryFailureReason(err), err)
	}
	if resp.Status != "success" {
		return nil, wrapTelemetryQueryError("prometheus_api_error", nil)
	}
	if len(resp.Data.Result) == 0 {
		return []operationscontract.TelemetryPoint{}, nil
	}
	points, err := decodePrometheusPoints(resp.Data.Result[0].Values)
	if err != nil {
		return nil, wrapTelemetryQueryError("response_decode_failed", err)
	}
	return points, nil
}

func (s *Client) Instant(ctx context.Context, query string) (operationscontract.TelemetryScalar, error) {
	endpoint, err := url.Parse(s.baseURL + "/api/v1/query")
	if err != nil {
		return operationscontract.TelemetryScalar{}, wrapTelemetryQueryError("request_url_invalid", err)
	}
	params := endpoint.Query()
	params.Set("query", query)
	endpoint.RawQuery = params.Encode()

	var resp prometheusInstantResponse
	if err := s.get(ctx, endpoint.String(), &resp); err != nil {
		return operationscontract.TelemetryScalar{}, wrapTelemetryQueryError(queryFailureReason(err), err)
	}
	if resp.Status != "success" {
		return operationscontract.TelemetryScalar{}, wrapTelemetryQueryError("prometheus_api_error", nil)
	}
	if len(resp.Data.Result) == 0 {
		return operationscontract.TelemetryScalar{}, nil
	}
	scalar, err := decodePrometheusValue(resp.Data.Result[0].Value)
	if err != nil {
		return operationscontract.TelemetryScalar{}, wrapTelemetryQueryError("response_decode_failed", err)
	}
	scalar.Labels = resp.Data.Result[0].Metric
	return scalar, nil
}

func (s *Client) get(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("prometheus returned status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type prometheusRangeResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

type prometheusInstantResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func decodePrometheusPoints(values [][]json.RawMessage) ([]operationscontract.TelemetryPoint, error) {
	points := make([]operationscontract.TelemetryPoint, 0, len(values))
	for _, value := range values {
		if len(value) < 2 {
			continue
		}
		ts, scalar, err := decodePrometheusPair(value)
		if err != nil {
			return nil, err
		}
		if !scalar.Available {
			continue
		}
		points = append(points, operationscontract.TelemetryPoint{Timestamp: ts.Format(time.RFC3339), Value: scalar.Value})
	}
	return points, nil
}

func decodePrometheusValue(value []json.RawMessage) (operationscontract.TelemetryScalar, error) {
	if len(value) < 2 {
		return operationscontract.TelemetryScalar{}, nil
	}
	_, scalar, err := decodePrometheusPair(value)
	return scalar, err
}

func decodePrometheusPair(value []json.RawMessage) (time.Time, operationscontract.TelemetryScalar, error) {
	var unixSeconds float64
	if err := json.Unmarshal(value[0], &unixSeconds); err != nil {
		return time.Time{}, operationscontract.TelemetryScalar{}, err
	}
	var raw string
	if err := json.Unmarshal(value[1], &raw); err != nil {
		return time.Time{}, operationscontract.TelemetryScalar{}, err
	}
	metricValue, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(metricValue) || math.IsInf(metricValue, 0) {
		secs, frac := math.Modf(unixSeconds)
		return time.Unix(int64(secs), int64(frac*1e9)).UTC(), operationscontract.TelemetryScalar{}, nil
	}
	secs, frac := math.Modf(unixSeconds)
	return time.Unix(int64(secs), int64(frac*1e9)).UTC(), operationscontract.TelemetryScalar{Value: nilIfNegative(metricValue), Available: true}, nil
}

func nilIfNegative(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}
