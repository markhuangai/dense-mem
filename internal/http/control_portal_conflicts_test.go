package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/conflictqueue"
)

type conflictQueueReaderFunc func(context.Context, string, conflictqueue.ListOptions) (*domain.ConflictQueuePage, error)

func (f conflictQueueReaderFunc) List(ctx context.Context, teamID string, options conflictqueue.ListOptions) (*domain.ConflictQueuePage, error) {
	return f(ctx, teamID, options)
}

func TestControlPortalConflictQueue(t *testing.T) {
	teamID := uuid.New()
	var observed conflictqueue.ListOptions
	reader := conflictQueueReaderFunc(func(_ context.Context, gotTeamID string, options conflictqueue.ListOptions) (*domain.ConflictQueuePage, error) {
		require.Equal(t, teamID.String(), gotTeamID)
		observed = options
		return &domain.ConflictQueuePage{Summary: domain.ConflictQueueSummary{}, Items: []domain.ConflictQueueItem{}}, nil
	})
	e, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{ConflictQueue: reader}, HealthConfig{}, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/control/api/teams/"+teamID.String()+"/conflicts/queue?status=overdue&limit=50&cursor=abc", nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"next_cursor":null`)
	require.Equal(t, "overdue", observed.Status)
	require.Equal(t, 50, observed.Limit)
	require.Equal(t, "abc", observed.Cursor)
}

func TestControlPortalConflictQueueRejectsNonGetAndInvalidStatus(t *testing.T) {
	reader := conflictQueueReaderFunc(func(context.Context, string, conflictqueue.ListOptions) (*domain.ConflictQueuePage, error) {
		return nil, conflictqueue.ErrInvalidStatus
	})
	e, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{ConflictQueue: reader}, HealthConfig{}, nil)
	require.NoError(t, err)
	teamID := uuid.NewString()

	req := httptest.NewRequest(http.MethodGet, "/control/api/teams/"+teamID+"/conflicts/queue?status=closed", nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)

	req = httptest.NewRequest(http.MethodPost, "/control/api/teams/"+teamID+"/conflicts/queue", nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder = httptest.NewRecorder()
	e.ServeHTTP(recorder, req)
	require.NotEqual(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "stack")
}
