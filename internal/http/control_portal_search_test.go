package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

type controlSearchConvergenceReader struct {
	value *repository.SearchConvergence
	err   error
}

func (r controlSearchConvergenceReader) GetSearchConvergence(context.Context) (*repository.SearchConvergence, error) {
	return r.value, r.err
}

type controlSearchLogger struct{ warnings []string }

func (*controlSearchLogger) Info(string, ...observability.LogAttr)         {}
func (*controlSearchLogger) Error(string, error, ...observability.LogAttr) {}
func (l *controlSearchLogger) Warn(message string, _ ...observability.LogAttr) {
	l.warnings = append(l.warnings, message)
}
func (*controlSearchLogger) Debug(string, ...observability.LogAttr)                    {}
func (l *controlSearchLogger) With(...observability.LogAttr) observability.LogProvider { return l }

func TestControlPortalSearchConvergenceMapsCurrentDocumentProjection(t *testing.T) {
	now := time.Date(2026, time.August, 26, 1, 0, 0, 0, time.UTC)
	start, finish := now.Add(-time.Minute), now.Add(-time.Second)
	converted := toControlSearchConvergence(&repository.SearchConvergence{
		ObservedAt: now, Status: "attention_required", ExpectedDocuments: 10, CurrentDocuments: 7,
		DriftedDocuments: 3, AffectedTeamCount: 2, OldestDriftAge: 2 * time.Minute,
		Contract:     &repository.ActiveSearchContract{EmbeddingProvider: "openai", EmbeddingModel: "model", EmbeddingDimensions: 3, IndexGeneration: 4, IndexStrategy: "hnsw"},
		DriftClasses: []repository.SearchDocumentDriftCount{{Class: "missing_vector", Count: 3}},
		LatestRun:    &repository.SearchReconciliationRun{RunID: "run", LocalRunDate: now, Status: "failed", SelectedCount: 4, EmbeddedCount: 2, UpdatedCount: 1, DriftedCount: 3, LastError: "provider response contained private details", StartedAt: &start, CompletedAt: &finish, UpdatedAt: now},
	})
	require.Equal(t, "attention_required", converted.Status)
	require.Equal(t, int64(10), converted.ExpectedDocuments)
	require.Equal(t, 3, converted.Contract.Dimensions)
	require.Equal(t, []controlSearchDriftResponse{{Class: "missing_vector", Count: 3}}, converted.DriftClasses)
	require.Equal(t, "reconciliation operation failed", converted.LatestRun.LastError)
	require.Equal(t, start.Format(time.RFC3339), converted.LatestRun.StartedAt)
	require.Nil(t, toControlSearchConvergence(nil).Contract)
	require.Equal(t, "safe message", boundedControlSearchRunError("  safe   message  "))
	require.Equal(t, "reconciliation operation failed", boundedControlSearchRunError(strings.Repeat("x", 129)))
	require.Equal(t, "reconciliation operation failed", boundedControlSearchRunError("provider failed"))
	require.Empty(t, boundedControlSearchRunError(""))
}

func TestControlPortalSearchConvergenceHandlerBoundsFailures(t *testing.T) {
	baseConfig := &config.Config{ControlPortalToken: "secret"}
	for _, test := range []struct {
		name   string
		reader service.SearchConvergenceReader
		want   int
	}{
		{name: "unavailable dependency", reader: controlSearchConvergenceReader{err: service.ErrSearchConvergenceUnavailable}, want: http.StatusServiceUnavailable},
		{name: "backend failure", reader: controlSearchConvergenceReader{err: errors.New("pq: private details")}, want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger := &controlSearchLogger{}
			server, err := NewControlPortalServerWithMetricsAndTelemetry(baseConfig, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{Convergence: test.reader}, HealthConfig{}, logger)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodGet, "/control/api/search/convergence", nil)
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			require.Equal(t, test.want, rec.Code)
			require.NotContains(t, rec.Body.String(), "private details")
			if test.name == "backend failure" {
				require.Contains(t, logger.warnings, "control_search_convergence_failed")
			}
		})
	}
	server, err := NewControlPortalServerWithMetricsAndTelemetry(baseConfig, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{}, HealthConfig{}, nil)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/control/api/search/convergence", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}
