package recall

import (
	"net/http/httptest"
	"testing"

	"github.com/markhuangai/dense-mem/internal/observability"
)

func recallMetricsText(t testing.TB, metrics *observability.PrometheusMetrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	return recorder.Body.String()
}
