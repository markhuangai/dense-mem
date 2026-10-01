package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	operationsprometheus "github.com/markhuangai/dense-mem/internal/operations/prometheus"
	"github.com/stretchr/testify/require"
)

type telemetryTransportOwnershipFixture struct {
	service  *PrometheusTelemetryService
	logger   *captureTelemetryLogger
	mu       sync.Mutex
	requests map[string]int
}

func newTelemetryTransportOwnershipService(baseURL string, timeout time.Duration, logger observability.LogProvider) *PrometheusTelemetryService {
	return NewPrometheusTelemetryService(operationsprometheus.NewClient(baseURL, timeout), timeout, "fixture-job", logger)
}

func newTelemetryTransportOwnershipFixture(t testing.TB, mode string) *telemetryTransportOwnershipFixture {
	t.Helper()
	f := &telemetryTransportOwnershipFixture{logger: &captureTelemetryLogger{}, requests: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests[r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery]++
		f.mu.Unlock()
		query := r.URL.Query().Get("query")
		if mode == "http_failure" || (mode == "partial_failure" && strings.Contains(query, "status_class")) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("private provider body"))
			return
		}
		switch mode {
		case "api_failure":
			_, _ = w.Write([]byte(`{"status":"error","error":"private provider body"}`))
			return
		case "invalid_json":
			_, _ = w.Write([]byte(`not json`))
			return
		case "truncated_json":
			_, _ = w.Write([]byte(`{`))
			return
		}
		labels := `{"series":"first"}`
		if mode == "pricing_missing" && strings.Contains(query, "densemem_ai_operation_cost_usd_total") {
			labels = `{"reason":"missing_price"}`
		}
		value := `[1770000000.5,"2.5"]`
		values := `[[1770000000.5,"0.5"],[1770000060,"1.25"]]`
		switch mode {
		case "wrong_timestamp":
			value = `["bad","1"]`
			values = `[["bad","1"]]`
		case "wrong_value_type":
			value = `[1770000000,1]`
			values = `[[1770000000,1]]`
		case "invalid_numeric":
			value = `[1770000000,"invalid"]`
			values = `[[1770000000,"invalid"]]`
		case "nan":
			value = `[1770000000,"NaN"]`
			values = `[[1770000000,"NaN"]]`
		case "positive_inf":
			value = `[1770000000,"+Inf"]`
			values = `[[1770000000,"+Inf"]]`
		case "negative_inf":
			value = `[1770000000,"-Inf"]`
			values = `[[1770000000,"-Inf"]]`
		case "negative":
			value = `[1770000000,"-1"]`
			values = `[[1770000000,"-1"]]`
		case "short":
			value = `[1770000000]`
			values = `[[1770000000],[1770000060,"2"]]`
		case "sparse":
			values = `[[1770000000,"NaN"],[1770000001,"+Inf"],[1770000002,"invalid"],[1770000003],[1770000004,"-1"],[1770000060.75,"2.5"]]`
		}
		result := `[{"metric":` + labels + `,"value":` + value + `,"values":` + values + `},{"metric":{"series":"second"},"value":[1770000000,"999"],"values":[[1770000000,"999"]]}]`
		if mode == "empty" {
			result = `[]`
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":` + result + `}}`))
	}))
	t.Cleanup(server.Close)
	f.service = newTelemetryTransportOwnershipService(server.URL, time.Second, f.logger)
	f.service.now = func() time.Time { return time.Unix(1770000060, 750000000).UTC() }
	return f
}

func (f *telemetryTransportOwnershipFixture) resetRequests() {
	f.mu.Lock()
	defer f.mu.Unlock()
	clear(f.requests)
}

func (f *telemetryTransportOwnershipFixture) requestContract(t testing.TB, operations int) (string, int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	normalized := make(map[string]int, len(f.requests))
	total := 0
	for request, count := range f.requests {
		require.Zero(t, count%operations, "inconsistent query count: %s", request)
		normalized[request] = count / operations
		total += count / operations
	}
	return telemetryTransportOwnershipHash(t, normalized), total
}

func telemetryTransportOwnershipHash(t testing.TB, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (f *telemetryTransportOwnershipFixture) snapshotSignature(t testing.TB, snapshot *TelemetrySnapshot, err error) string {
	t.Helper()
	message := ""
	if err != nil {
		message = err.Error()
	}
	return telemetryTransportOwnershipHash(t, struct {
		Snapshot          *TelemetrySnapshot
		Error             string
		Log               string
		LogAttributes     []string
		Warning           string
		WarningAttributes []string
	}{snapshot, message, f.logger.message, f.logger.attrs, f.logger.warnMessage, f.logger.warnAttrs})
}

func (f *telemetryTransportOwnershipFixture) snapshot(ctx context.Context, filter TelemetryFilter) (*TelemetrySnapshot, error) {
	return f.service.Snapshot(ctx, filter)
}
