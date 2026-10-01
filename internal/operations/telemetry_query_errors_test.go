package operations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	operationsprometheus "github.com/markhuangai/dense-mem/internal/operations/prometheus"
	"github.com/stretchr/testify/require"
)

func TestPrometheusTelemetryService_UnavailableWhenAllQueriesFail(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer prom.Close()

	svc := NewPrometheusTelemetryService(operationsprometheus.NewClient(prom.URL, time.Second), time.Second, "", nil)

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
	svc := NewPrometheusTelemetryService(operationsprometheus.NewClient(prom.URL, time.Second), time.Second, "", logger)

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
	svc := NewPrometheusTelemetryService(operationsprometheus.NewClient("http://prometheus.invalid", time.Second), time.Second, "", logger)
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

func TestTelemetryQueryFailureLogBounds(t *testing.T) {
	logger := &captureTelemetryLogger{}
	teamID, profileID := uuid.New(), uuid.New()
	svc := NewPrometheusTelemetryService(operationsprometheus.NewClient("http://prometheus.invalid", time.Second), time.Second, "dense-mem", logger)
	failures := make([]telemetryQueryFailure, 0, 65)
	for index := 0; index < 65; index++ {
		failures = append(failures, telemetryQueryFailure{kind: "range", id: fmt.Sprintf("query-%02d", index), err: &operationscontract.TelemetryQueryError{Reason: "transport_failed", Cause: errors.New("network unavailable")}})
	}
	svc.logQueryFailures("1h", TelemetryScope{Type: "team", TeamID: &teamID, ProfileID: &profileID}, failures)
	require.Equal(t, "telemetry backend query failed", logger.message)
	require.Contains(t, logger.attrs, "query_ids_truncated=true")
	var queryIDs string
	for _, attr := range logger.attrs {
		if strings.HasPrefix(attr, "query_ids=") {
			queryIDs = strings.TrimPrefix(attr, "query_ids=")
			break
		}
	}
	require.Len(t, strings.Split(queryIDs, ","), 64)
	require.NotContains(t, queryIDs, "query-64")
	require.Contains(t, logger.attrs, "team_id="+teamID.String())
	require.Contains(t, logger.attrs, "profile_id="+profileID.String())
	require.Contains(t, logger.attrs, "prometheus_job=dense-mem")
}
