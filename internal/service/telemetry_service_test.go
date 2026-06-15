package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestPrometheusTelemetryService_UnconfiguredReturnsUnavailableSnapshot(t *testing.T) {
	svc := NewPrometheusTelemetryService("", time.Second)
	svc.now = func() time.Time { return time.Date(2026, 5, 2, 13, 0, 0, 0, time.UTC) }

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "30m", Scope: "system"})

	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, "30m", snapshot.Window.Key)
	require.Equal(t, "telemetry backend is not configured", snapshot.Message)
	require.NotEmpty(t, snapshot.Cards)
	require.NotEmpty(t, snapshot.Series)

	payload, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.Contains(t, string(payload), `"points":[]`)
	require.NotContains(t, string(payload), `"points":null`)
}

func TestPrometheusTelemetryService_QueriesTypedScope(t *testing.T) {
	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	var queries []string

	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("query"))
		switch r.URL.Path {
		case "/api/v1/query":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"value":[1770000000,"42"]}]}}`))
		case "/api/v1/query_range":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"values":[[1770000000,"0.5"],[1770000060,"1.25"]]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prom.Close()

	svc := NewPrometheusTelemetryServiceWithJobAndLogger(prom.URL, time.Second, "dense-mem-demo", nil)
	svc.now = func() time.Time { return time.Unix(1770000060, 0).UTC() }

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{
		Window:    "1h",
		Scope:     "profile",
		TeamID:    &teamID,
		ProfileID: &profileID,
	})

	require.NoError(t, err)
	require.True(t, snapshot.Available)
	require.Equal(t, 42.0, snapshot.Cards[0].Value)
	require.Equal(t, 2, len(snapshot.Series[0].Points))
	require.Condition(t, func() bool {
		for _, query := range queries {
			if strings.Contains(query, `team_id="`+teamID.String()+`"`) && strings.Contains(query, `profile_id="`+profileID.String()+`"`) {
				return true
			}
		}
		return false
	})
	require.Condition(t, func() bool {
		for _, query := range queries {
			if !strings.Contains(query, `job="dense-mem-demo"`) {
				return false
			}
		}
		return len(queries) > 0
	})
	var httpErrorCardQuery string
	for _, query := range queries {
		if strings.Contains(query, `sum(increase(densemem_http_requests_total`) && strings.Contains(query, "status_class") {
			httpErrorCardQuery = query
			break
		}
	}
	require.NotEmpty(t, httpErrorCardQuery)
	require.Contains(t, httpErrorCardQuery, `status_class=~"4xx|5xx"`)
	require.NotContains(t, httpErrorCardQuery, `status_class="4xx|5xx"`)
	require.Condition(t, func() bool {
		for _, query := range queries {
			if strings.Contains(query, `densemem_recall_feedback_total`) &&
				strings.Contains(query, `used="true"`) {
				return true
			}
		}
		return false
	})
	require.Condition(t, func() bool {
		for _, query := range queries {
			if strings.Contains(query, `histogram_quantile(0.95`) &&
				strings.Contains(query, `densemem_recall_duration_seconds_bucket`) {
				return true
			}
		}
		return false
	})
}

func TestPrometheusTelemetryService_RejectsInvalidWindow(t *testing.T) {
	svc := NewPrometheusTelemetryService("", time.Second)

	_, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "2h", Scope: "system"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "window must be one of")
}

func TestPrometheusTelemetryService_SnapshotTimeoutBoundsSequentialQueries(t *testing.T) {
	var requests atomic.Int32
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(25 * time.Millisecond)
		switch r.URL.Path {
		case "/api/v1/query":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"value":[1770000000,"42"]}]}}`))
		case "/api/v1/query_range":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"values":[[1770000000,"0.5"]]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prom.Close()

	svc := NewPrometheusTelemetryService(prom.URL, 60*time.Millisecond)
	svc.now = func() time.Time { return time.Unix(1770000060, 0).UTC() }

	start := time.Now()
	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "system"})
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, "telemetry backend query failed", snapshot.Message)
	require.Less(t, elapsed, 250*time.Millisecond)
	require.Less(t, requests.Load(), int32(len(telemetryCardSpecs(TelemetryScope{Type: "system"}, nil, "15m"))))
}

func TestPrometheusTelemetryService_ValidationAndDecodeBranches(t *testing.T) {
	svc := NewPrometheusTelemetryService(" https://prom.example.test/ ", 0)
	require.Equal(t, "https://prom.example.test", svc.baseURL)
	require.Equal(t, 5*time.Second, svc.client.Timeout)
	require.Equal(t, 5*time.Second, svc.timeout)

	snapshot, err := (*PrometheusTelemetryService)(nil).Snapshot(context.Background(), TelemetryFilter{})
	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, "1h", snapshot.Window.Key)
	require.Equal(t, "system", snapshot.Scope.Type)

	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	scope, err := normalizeTelemetryScope(TelemetryFilter{Scope: "team", TeamID: &teamID})
	require.NoError(t, err)
	require.Equal(t, `{"team_id":"11111111-1111-4111-8111-111111111111"}`, mustMarshalStringMap(t, scopeLabels(scope)))
	require.Equal(t, `{kind="total"}`, telemetrySelectorWithRaw("", `kind="total"`))
	require.Equal(t, `{status_class=~"4xx|5xx"}`, telemetrySelector(TelemetryScope{}, map[string]string{"status_class": `~"4xx|5xx"`}))
	require.Equal(t, `{job="dense-mem-demo",status_class=~"4xx|5xx"}`, telemetrySelector(TelemetryScope{}, mergeTelemetryLabels(map[string]string{"job": "dense-mem-demo"}, map[string]string{"status_class": `~"4xx|5xx"`})))
	require.Equal(t, `100 * (sum(increase(densemem_recall_feedback_total{used="true"}[1h]))) / (sum(increase(densemem_recall_feedback_total[1h])))`, telemetryRecallFeedbackRate(TelemetryScope{}, nil, map[string]string{"used": "true"}, "1h"))
	require.Equal(t, `1000 * histogram_quantile(0.95, sum(rate(densemem_recall_duration_seconds_bucket[1m])) by (le))`, telemetryRangeHistogramQuantile("densemem_recall_duration_seconds", "", "", "1m", 0.95, 1000))

	scope, err = normalizeTelemetryScope(TelemetryFilter{Scope: "self", TeamID: &teamID, ProfileID: &profileID})
	require.NoError(t, err)
	require.Equal(t, "self", scope.Type)
	require.Equal(t, profileID, *scope.ProfileID)
	require.Equal(t, `{profile_id="22222222-2222-4222-8222-222222222222",team_id="11111111-1111-4111-8111-111111111111"}`, telemetrySelector(scope, nil))

	_, err = normalizeTelemetryScope(TelemetryFilter{Scope: "team"})
	require.ErrorContains(t, err, "team_id is required")
	_, err = normalizeTelemetryScope(TelemetryFilter{Scope: "profile"})
	require.ErrorContains(t, err, "profile_id is required")
	_, err = normalizeTelemetryScope(TelemetryFilter{Scope: "other"})
	require.ErrorContains(t, err, "scope must be one of")

	points, err := decodePrometheusPoints([][]json.RawMessage{
		{json.RawMessage(`1770000000.5`), json.RawMessage(`"2.5"`)},
		{json.RawMessage(`1770000001`)},
		{json.RawMessage(`1770000002`), json.RawMessage(`"-1"`)},
	})
	require.NoError(t, err)
	require.Len(t, points, 2)
	require.Equal(t, 2.5, points[0].Value)
	require.Equal(t, 0.0, points[1].Value)

	value, err := decodePrometheusValue(nil)
	require.NoError(t, err)
	require.Equal(t, 0.0, value)

	_, _, err = decodePrometheusPair([]json.RawMessage{json.RawMessage(`"bad"`), json.RawMessage(`"1"`)})
	require.Error(t, err)
	_, _, err = decodePrometheusPair([]json.RawMessage{json.RawMessage(`1`), json.RawMessage(`1`)})
	require.Error(t, err)
	_, value, err = decodePrometheusPair([]json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"NaN"`)})
	require.NoError(t, err)
	require.Equal(t, 0.0, value)
}

func TestPrometheusTelemetryService_QueryFailureBranches(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		switch query {
		case "status-error":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "bad-json":
			_, _ = w.Write([]byte(`{`))
		case "instant-error":
			_, _ = w.Write([]byte(`{"status":"error","error":"bad instant"}`))
		case "instant-empty":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
		case "range-error":
			_, _ = w.Write([]byte(`{"status":"error","error":"bad range"}`))
		case "range-empty":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer prom.Close()

	svc := NewPrometheusTelemetryService(prom.URL, time.Second)
	now := time.Unix(1770000000, 0).UTC()

	value, err := svc.queryInstant(context.Background(), "instant-empty")
	require.NoError(t, err)
	require.Equal(t, 0.0, value)
	_, err = svc.queryInstant(context.Background(), "instant-error")
	require.ErrorContains(t, err, "bad instant")
	_, err = svc.queryInstant(context.Background(), "bad-json")
	require.Error(t, err)
	_, err = svc.queryInstant(context.Background(), "status-error")
	require.ErrorContains(t, err, "status 503")

	points, err := svc.queryRange(context.Background(), "range-empty", now.Add(-time.Minute), now, time.Minute)
	require.NoError(t, err)
	require.Empty(t, points)
	_, err = svc.queryRange(context.Background(), "range-error", now.Add(-time.Minute), now, time.Minute)
	require.ErrorContains(t, err, "bad range")

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m"})
	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, "telemetry backend query failed", snapshot.Message)
}

func TestPrometheusTelemetryService_LogsQueryFailure(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","error":"bad instant"}`))
	}))
	defer prom.Close()

	logger := &captureTelemetryLogger{}
	svc := NewPrometheusTelemetryServiceWithLogger(prom.URL, time.Second, logger)

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "system"})

	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, "telemetry backend query failed", snapshot.Message)
	require.Equal(t, "telemetry backend query failed", logger.message)
	require.ErrorContains(t, logger.err, "bad instant")
	require.Contains(t, logger.attrs, "query_kind=instant")
	require.Contains(t, logger.attrs, "query_id=http_requests")
	require.Contains(t, logger.attrs, "window=15m")
	require.Contains(t, logger.attrs, "scope=system")
	require.Contains(t, logger.attrs, "prometheus_query=sum(increase(densemem_http_requests_total[15m]))")
}

type captureTelemetryLogger struct {
	message string
	err     error
	attrs   []string
}

func (l *captureTelemetryLogger) Info(string, ...observability.LogAttr) {}

func (l *captureTelemetryLogger) Error(msg string, err error, attrs ...observability.LogAttr) {
	l.message = msg
	l.err = err
	l.attrs = logAttrStrings(attrs)
}

func (l *captureTelemetryLogger) Warn(string, ...observability.LogAttr)  {}
func (l *captureTelemetryLogger) Debug(string, ...observability.LogAttr) {}

func (l *captureTelemetryLogger) With(...observability.LogAttr) observability.LogProvider {
	return l
}

func logAttrStrings(attrs []observability.LogAttr) []string {
	out := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		out = append(out, attr.Key+"="+toLogString(attr.Value))
	}
	return out
}

func toLogString(value any) string {
	if err, ok := value.(error); ok {
		return err.Error()
	}
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func scopeLabels(scope TelemetryScope) map[string]string {
	labels := map[string]string{}
	if scope.TeamID != nil {
		labels["team_id"] = scope.TeamID.String()
	}
	if scope.ProfileID != nil {
		labels["profile_id"] = scope.ProfileID.String()
	}
	return labels
}

func mustMarshalStringMap(t *testing.T, value map[string]string) string {
	t.Helper()

	out, err := json.Marshal(value)
	require.NoError(t, err)
	return string(out)
}
