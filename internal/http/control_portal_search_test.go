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

type controlSearchConvergenceReaderStub struct {
	value *repository.SearchConvergence
	err   error
}

func (s controlSearchConvergenceReaderStub) GetSearchConvergence(context.Context) (*repository.SearchConvergence, error) {
	return s.value, s.err
}

type controlSearchLoggerStub struct {
	warnings []string
	errors   []string
}

func (*controlSearchLoggerStub) Info(string, ...observability.LogAttr) {}
func (l *controlSearchLoggerStub) Error(_ string, err error, _ ...observability.LogAttr) {
	l.errors = append(l.errors, err.Error())
}
func (l *controlSearchLoggerStub) Warn(message string, _ ...observability.LogAttr) {
	l.warnings = append(l.warnings, message)
}
func (*controlSearchLoggerStub) Debug(string, ...observability.LogAttr) {}
func (l *controlSearchLoggerStub) With(...observability.LogAttr) observability.LogProvider {
	return l
}

func TestControlPortalSearchConvergenceIsBoundedAndReadOnly(t *testing.T) {
	now := time.Date(2026, 8, 10, 4, 30, 0, 0, time.UTC)
	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{
		Convergence: controlSearchConvergenceReaderStub{value: &repository.SearchConvergence{
			ObservedAt: now,
			Status:     "attention_required",
			Failures: []repository.EmbeddingFailureCount{{
				SourceKind: "evidence", FailureClass: "provider_action_required",
				FailureCode: "provider_quota_exhausted", Count: 2,
			}},
			FailureGroups: []repository.EmbeddingFailureGroup{{
				TeamID: "team-1", TeamName: "Payments", FailureCode: "provider_quota_exhausted",
				Status: "attention_required", FailedJobCount: 2, AffectedJobCount: 2,
				Guidance:      "Add provider credit or repair billing before the next daily canary.",
				FirstFailedAt: now, LastFailedAt: now, Age: time.Minute,
			}},
			FailureGroupCount:      101,
			FailureGroupsTruncated: true,
			LatestRun: &repository.EmbeddingReconciliationRun{
				RunID: "run-1", LocalRunDate: now, Status: "deferred",
				CanaryFailureCode: "provider_quota_exhausted",
				LastError:         "provider response contained a secret and should never cross this boundary",
				UpdatedAt:         now,
			},
		}},
	}, HealthConfig{}, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/control/api/search/convergence", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.Contains(t, body, "provider_quota_exhausted")
	require.Contains(t, body, "Payments")
	require.Contains(t, body, "Add provider credit")
	require.Contains(t, body, `"failure_group_count":101`)
	require.Contains(t, body, `"failure_groups_truncated":true`)
	require.Contains(t, body, "reconciliation failed: provider_quota_exhausted")
	require.NotContains(t, body, "provider response contained a secret")
}

func TestControlSearchConvergenceConversionAndRunErrorsAreBounded(t *testing.T) {
	empty := toControlSearchConvergence(nil)
	require.Empty(t, empty.Status)
	require.Empty(t, empty.Failures)
	require.Empty(t, empty.FailureGroups)

	for _, test := range []struct {
		message, code, want string
	}{
		{"canary selection failed", "", "canary selection failed"},
		{"daily embedding canary failed", "", "daily embedding canary failed"},
		{"provider response leaked", "", "reconciliation operation failed"},
		{strings.Repeat("x", 129), "", "reconciliation operation failed"},
		{"provider response leaked", "provider_quota_exhausted", "reconciliation failed: provider_quota_exhausted"},
	} {
		require.Equal(t, test.want, boundedControlSearchRunError(test.message, test.code))
	}

	now := time.Date(2026, 8, 10, 4, 30, 0, 0, time.UTC)
	converted := toControlSearchConvergence(&repository.SearchConvergence{
		ObservedAt: now,
		Status:     "recovering",
		Contract:   &repository.ActiveSearchContract{EmbeddingProvider: "openai", EmbeddingModel: "model", EmbeddingDimensions: 3, IndexGeneration: 2, IndexStrategy: "exact"},
		Queued:     1, Processing: 2, Failed: 3, ExpiredLeases: 4, AffectedTeamCount: 5,
		FailureGroupCount: 7, FailureGroupsTruncated: true,
		OldestPendingAge: time.Minute, OldestFailureAge: 2 * time.Minute,
		LatestRun: &repository.EmbeddingReconciliationRun{RunID: "run", LocalRunDate: now, Status: "completed", CanaryAttemptedAt: &now, CanaryOutcome: "succeeded", UpdatedAt: now},
	})
	require.NotNil(t, converted.Contract)
	require.Equal(t, "model", converted.Contract.Model)
	require.Equal(t, int64(3), converted.Queue.Failed)
	require.Equal(t, int64(7), converted.FailureGroupCount)
	require.True(t, converted.FailureGroupsTruncated)
	require.NotNil(t, converted.LatestRun)
	require.Equal(t, now.Format(time.RFC3339), converted.LatestRun.CanaryAttemptedAt)
}

func TestControlPortalSearchConvergenceBoundsRepositoryErrors(t *testing.T) {
	const backendMessage = "pq: relation secret_embedding_table does not exist"
	logger := &controlSearchLoggerStub{}
	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{
		Convergence: controlSearchConvergenceReaderStub{err: errors.New(backendMessage)},
	}, HealthConfig{}, logger)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/control/api/search/convergence", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "failed to load search convergence")
	require.NotContains(t, rec.Body.String(), backendMessage)
	require.Contains(t, logger.warnings, "control_search_convergence_failed")
	for _, loggedError := range logger.errors {
		require.NotContains(t, loggedError, backendMessage)
	}
}

func TestControlPortalSearchConvergenceMapsUnavailableServiceTo503(t *testing.T) {
	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr: "127.0.0.1:8090", ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{
		Convergence: controlSearchConvergenceReaderStub{err: service.ErrSearchConvergenceUnavailable},
	}, HealthConfig{}, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/control/api/search/convergence", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "search convergence unavailable")
}
