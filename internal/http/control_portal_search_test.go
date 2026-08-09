package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type controlSearchConvergenceReaderStub struct {
	value *repository.SearchConvergence
}

func (s controlSearchConvergenceReaderStub) GetSearchConvergence(context.Context) (*repository.SearchConvergence, error) {
	return s.value, nil
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
			Incidents: []repository.EmbeddingFailureIncident{{
				TeamID: "team-1", TeamName: "Payments", FailureCode: "provider_quota_exhausted",
				Guidance:    "Add provider credit or repair billing before the next daily canary.",
				FirstSeenAt: now, LastSeenAt: now, Age: time.Minute,
			}},
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
	require.Contains(t, body, "reconciliation failed: provider_quota_exhausted")
	require.NotContains(t, body, "provider response contained a secret")
}

func TestControlSearchConvergenceConversionAndRunErrorsAreBounded(t *testing.T) {
	empty := toControlSearchConvergence(nil)
	require.Empty(t, empty.Status)
	require.Empty(t, empty.Failures)
	require.Empty(t, empty.Incidents)

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
		OldestPendingAge: time.Minute, OldestFailureAge: 2 * time.Minute,
		LatestRun: &repository.EmbeddingReconciliationRun{RunID: "run", LocalRunDate: now, Status: "completed", CanaryAttemptedAt: &now, CanaryOutcome: "succeeded", UpdatedAt: now},
	})
	require.NotNil(t, converted.Contract)
	require.Equal(t, "model", converted.Contract.Model)
	require.Equal(t, int64(3), converted.Queue.Failed)
	require.NotNil(t, converted.LatestRun)
	require.Equal(t, now.Format(time.RFC3339), converted.LatestRun.CanaryAttemptedAt)
}
