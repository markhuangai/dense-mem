package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

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
	require.False(t, value.Available)
	require.Equal(t, 0.0, value.Value)
	_, err = svc.queryInstant(context.Background(), "instant-error")
	require.EqualError(t, err, "telemetry backend query failed")
	require.NotContains(t, err.Error(), "bad instant")
	_, err = svc.queryInstant(context.Background(), "bad-json")
	require.Error(t, err)
	_, err = svc.queryInstant(context.Background(), "status-error")
	require.EqualError(t, err, "telemetry backend query failed")
	require.NotContains(t, err.Error(), "status 503")

	points, err := svc.queryRange(context.Background(), "range-empty", now.Add(-time.Minute), now, time.Minute)
	require.NoError(t, err)
	require.Empty(t, points)
	_, err = svc.queryRange(context.Background(), "range-error", now.Add(-time.Minute), now, time.Minute)
	require.EqualError(t, err, "telemetry backend query failed")
	require.NotContains(t, err.Error(), "bad range")

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m"})
	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, TelemetrySnapshotUnavailable, snapshot.Status)
	require.Equal(t, "telemetry sources are unavailable", snapshot.Message)
}

func TestPrometheusTelemetryService_LogsQueryFailure(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","error":"bad instant"}`))
	}))
	defer prom.Close()

	logger := &captureTelemetryLogger{}
	svc := NewPrometheusTelemetryServiceWithLogger(prom.URL, time.Second, logger)

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "system", Audience: TelemetryAudienceOperator})

	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, TelemetrySnapshotUnavailable, snapshot.Status)
	require.Equal(t, "telemetry sources are unavailable", snapshot.Message)
	require.Equal(t, "telemetry backend query failed", logger.message)
	require.EqualError(t, logger.err, "telemetry backend query failed")
	require.NotContains(t, logger.attrs, "prometheus_query=bad instant")
	require.Condition(t, func() bool {
		return hasTelemetryAttrPrefix(logger.attrs, "query_kinds=instant=") || hasTelemetryAttrPrefix(logger.attrs, "query_kinds=range=")
	})
	require.Condition(t, func() bool {
		return hasTelemetryAttrPrefix(logger.attrs, "query_failure_reasons=prometheus_api_error=")
	})
	require.Condition(t, func() bool { return hasTelemetryAttrPrefix(logger.attrs, "query_ids=") })
	require.Contains(t, logger.attrs, "window=15m")
	require.Contains(t, logger.attrs, "scope=system")
	require.False(t, hasTelemetryAttrPrefix(logger.attrs, "prometheus_query="))
}

func TestPrometheusTelemetryServiceAggregatesCanceledQueriesWithoutRawErrors(t *testing.T) {
	logger := &captureTelemetryLogger{}
	svc := NewPrometheusTelemetryServiceWithLogger("http://prometheus.invalid", time.Second, logger)
	svc.logQueryFailures("1h", TelemetryScope{Type: "system"}, []telemetryQueryFailure{
		{kind: "instant", id: "http_requests", err: context.Canceled},
		{kind: "range", id: "http_rps", err: context.DeadlineExceeded},
	})

	require.Equal(t, "telemetry backend query canceled", logger.warnMessage)
	require.Nil(t, logger.err)
	require.Contains(t, logger.warnAttrs, "failed_query_count=2")
	require.Contains(t, logger.warnAttrs, "query_ids=http_requests,http_rps")
	require.Contains(t, logger.warnAttrs, "query_kinds=instant=1,range=1")
	require.Contains(t, logger.warnAttrs, "query_failure_reasons=context_canceled=1,context_deadline_exceeded=1")
}

func TestTelemetryQueryErrorClassificationAndLogBounds(t *testing.T) {
	var nilQueryError *telemetryQueryError
	require.Nil(t, nilQueryError.Unwrap())
	require.Equal(t, "", errorString(nil))
	require.Equal(t, "http_status", telemetryQueryFailureReason(errors.New("prometheus returned status 503")))
	require.Equal(t, "prometheus_api_error", telemetryQueryFailureReason(errors.New("query failed")))
	require.Equal(t, "response_decode_failed", telemetryQueryFailureReason(errors.New("cannot unmarshal response")))
	require.Equal(t, "transport_failed", telemetryQueryFailureReason(errors.New("network unavailable")))
	require.Equal(t, "explicit", telemetryQueryFailureReason(&telemetryQueryError{reason: "explicit"}))

	logger := &captureTelemetryLogger{}
	teamID, profileID := uuid.New(), uuid.New()
	svc := NewPrometheusTelemetryServiceWithJobAndLogger("http://prometheus.invalid", time.Second, "dense-mem", logger)
	failures := make([]telemetryQueryFailure, 0, 65)
	for index := 0; index < 65; index++ {
		failures = append(failures, telemetryQueryFailure{kind: "range", id: fmt.Sprintf("query-%02d", index), err: errors.New("network unavailable")})
	}
	svc.logQueryFailures("1h", TelemetryScope{Type: "team", TeamID: &teamID, ProfileID: &profileID}, failures)
	require.Equal(t, "telemetry backend query failed", logger.message)
	require.Contains(t, logger.attrs, "query_ids_truncated=true")
	require.Contains(t, logger.attrs, "team_id="+teamID.String())
	require.Contains(t, logger.attrs, "profile_id="+profileID.String())
	require.Contains(t, logger.attrs, "prometheus_job=dense-mem")
}
