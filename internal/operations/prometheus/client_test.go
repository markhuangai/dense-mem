package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	"github.com/stretchr/testify/require"
)

func TestDecodePrometheusSamples(t *testing.T) {
	points, err := decodePrometheusPoints([][]json.RawMessage{
		{json.RawMessage(`1770000000.5`), json.RawMessage(`"2.5"`)},
		{json.RawMessage(`1770000001`)},
		{json.RawMessage(`1770000002`), json.RawMessage(`"-1"`)},
		{json.RawMessage(`1770000003`), json.RawMessage(`"NaN"`)},
	})
	require.NoError(t, err)
	require.Len(t, points, 2)
	require.Equal(t, 2.5, points[0].Value)
	require.Equal(t, 0.0, points[1].Value)

	value, err := decodePrometheusValue(nil)
	require.NoError(t, err)
	require.False(t, value.Available)
	require.Equal(t, 0.0, value.Value)

	_, _, err = decodePrometheusPair([]json.RawMessage{json.RawMessage(`"bad"`), json.RawMessage(`"1"`)})
	require.Error(t, err)
	_, _, err = decodePrometheusPair([]json.RawMessage{json.RawMessage(`1`), json.RawMessage(`1`)})
	require.Error(t, err)
	_, value, err = decodePrometheusPair([]json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"NaN"`)})
	require.NoError(t, err)
	require.False(t, value.Available)
	require.Equal(t, 0.0, value.Value)
}

func TestClientNormalizesConfiguration(t *testing.T) {
	for _, base := range []string{"", "  ", " / ", "///"} {
		require.Nil(t, NewClient(base, 0))
	}
	client := NewClient(" https://prom.example.test/proxy/ ", 0).(*Client)
	require.Equal(t, "https://prom.example.test/proxy", client.baseURL)
	require.Equal(t, 5*time.Second, client.client.Timeout)
	require.Equal(t, 7*time.Second, NewClient("https://prom.example.test", 7*time.Second).(*Client).client.Timeout)
}

func TestClientEncodesQueriesAndSelectsFirstSeries(t *testing.T) {
	query := `sum(rate(metric{team_id="A",label=~"one|two + three"}[1m])) / 2`
	from := time.Unix(1770000000, 900).UTC()
	to := from.Add(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, query, r.URL.Query().Get("query"))
		if r.URL.Path == "/proxy/api/v1/query_range" {
			require.Equal(t, "1770000000", r.URL.Query().Get("start"))
			require.Equal(t, "1770000060", r.URL.Query().Get("end"))
			require.Equal(t, "15", r.URL.Query().Get("step"))
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"values":[[1770000000.5,"2.5"],[1770000060,"-2"]]},{"values":[[1770000000,"99"]]}]}}`))
			return
		}
		require.Equal(t, "/proxy/api/v1/query", r.URL.Path)
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"reason":"missing_price","owner":"first"},"value":[1770000000,"2.5"]},{"metric":{"owner":"second"},"value":[1770000000,"99"]}]}}`))
	}))
	defer server.Close()
	client := NewClient(" "+server.URL+"/proxy/ ", time.Second)
	scalar, err := client.Instant(context.Background(), query)
	require.NoError(t, err)
	require.Equal(t, operationscontract.TelemetryScalar{Value: 2.5, Available: true, Labels: map[string]string{"reason": "missing_price", "owner": "first"}}, scalar)
	points, err := client.Range(context.Background(), query, from, to, 15*time.Second)
	require.NoError(t, err)
	require.Equal(t, []operationscontract.TelemetryPoint{{Timestamp: "2026-02-02T02:40:00Z", Value: 2.5}, {Timestamp: "2026-02-02T02:41:00Z", Value: 0}}, points)
}

func TestClientPreservesEmptyAndInvalidNumericResults(t *testing.T) {
	for _, value := range []string{"empty", "short", "NaN", "+Inf", "-Inf", "invalid", "-3"} {
		t.Run(value, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sample := `[1770000000,"` + value + `"]`
				if value == "short" {
					sample = `[1770000000]`
				}
				result := `[{"metric":{"owner":"first"},"value":` + sample + `,"values":[` + sample + `]}]`
				if value == "empty" {
					result = `[]`
				}
				_, _ = w.Write([]byte(`{"status":"success","data":{"result":` + result + `}}`))
			}))
			defer server.Close()
			client := NewClient(server.URL, time.Second)
			scalar, err := client.Instant(context.Background(), "native")
			require.NoError(t, err)
			require.Equal(t, value == "-3", scalar.Available)
			require.Zero(t, scalar.Value)
			if value != "empty" {
				require.Equal(t, "first", scalar.Labels["owner"])
			}
			points, err := client.Range(context.Background(), "native", time.Unix(1770000000, 0), time.Unix(1770000060, 0), time.Minute)
			require.NoError(t, err)
			require.NotNil(t, points)
			if value == "-3" {
				require.Len(t, points, 1)
				require.Zero(t, points[0].Value)
			} else {
				require.Empty(t, points)
			}
		})
	}
}

func TestClientBoundsFailureMessagesAndReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{"http_status", 503, "private provider body", "http_status"},
		{"api_error", 200, `{"status":"error","error":"private provider body"}`, "prometheus_api_error"},
		{"invalid_json", 200, `not json`, "response_decode_failed"},
		{"truncated_json", 200, `{`, "transport_failed"},
		{"empty_body", 200, ``, "transport_failed"},
		{"wrong_envelope_type", 200, `{"status":123}`, "response_decode_failed"},
		{"wrong_timestamp", 200, `{"status":"success","data":{"result":[{"value":["bad","1"],"values":[["bad","1"]]}]}}`, "response_decode_failed"},
		{"wrong_value_type", 200, `{"status":"success","data":{"result":[{"value":[1,1],"values":[[1,1]]}]}}`, "response_decode_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewClient(server.URL, time.Second)
			_, instantErr := client.Instant(context.Background(), "private-query")
			_, rangeErr := client.Range(context.Background(), "private-query", time.Now(), time.Now(), time.Minute)
			for _, err := range []error{instantErr, rangeErr} {
				require.EqualError(t, err, "telemetry backend query failed")
				require.Equal(t, tc.reason, operationscontract.TelemetryQueryFailureReason(err))
				var typed *operationscontract.TelemetryQueryError
				require.ErrorAs(t, err, &typed)
				require.NotContains(t, err.Error(), server.URL)
				require.NotContains(t, err.Error(), "private")
			}
		})
	}
	for _, base := range []string{"http://%", "http://127.0.0.1:1"} {
		client := NewClient(base, time.Second)
		_, err := client.Instant(context.Background(), "secret")
		require.EqualError(t, err, "telemetry backend query failed")
		reason := "transport_failed"
		if base == "http://%" {
			reason = "request_url_invalid"
		}
		require.Equal(t, reason, operationscontract.TelemetryQueryFailureReason(err))
		_, err = client.Range(context.Background(), "secret", time.Now(), time.Now(), time.Minute)
		require.EqualError(t, err, "telemetry backend query failed")
		require.Equal(t, reason, operationscontract.TelemetryQueryFailureReason(err))
	}
}

func TestClientPreservesCancellationAndRequestTimeout(t *testing.T) {
	for _, kind := range []string{"instant", "range"} {
		t.Run(kind, func(t *testing.T) {
			var requests atomic.Int32
			entered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				select {
				case entered <- struct{}{}:
				default:
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			call := func(ctx context.Context, timeout time.Duration) error {
				client := NewClient(server.URL, timeout)
				if kind == "instant" {
					_, err := client.Instant(ctx, "bounded")
					return err
				}
				_, err := client.Range(ctx, "bounded", time.Now(), time.Now(), time.Minute)
				return err
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := call(ctx, time.Second)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, "context_canceled", operationscontract.TelemetryQueryFailureReason(err))
			require.Zero(t, requests.Load())
			ctx, cancel = context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- call(ctx, time.Second) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("request did not reach fixture")
			}
			cancel()
			err = <-done
			require.ErrorIs(t, err, context.Canceled)
			err = call(context.Background(), 25*time.Millisecond)
			require.True(t, errors.Is(err, context.DeadlineExceeded))
			require.Equal(t, "context_deadline_exceeded", operationscontract.TelemetryQueryFailureReason(err))
			require.Equal(t, int32(2), requests.Load())
		})
	}
}
