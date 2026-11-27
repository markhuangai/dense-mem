package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/settings"
	"github.com/markhuangai/dense-mem/internal/topics"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func TestControlTopicMaintenanceValidationUsesTopicNames(t *testing.T) {
	router := echo.New()
	router.HTTPErrorHandler = httperr.ErrorHandler
	registerTopicMaintenanceRoutes(router.Group("/control/api"), &controlConfigHandler{
		appConfig:        settings.NewAppConfigService(nil, nil),
		topicMaintenance: topics.NewMaintenanceService(topics.MaintenanceDependencies{}),
	})
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPatch, "/config/topic-maintenance", `{"items":[{}]}`},
		{http.MethodPost, "/topic-maintenance/runs", `{}`},
		{http.MethodPost, "/topic-maintenance/pause", `{}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/control/api"+tc.path, strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
			body := strings.ToLower(response.Body.String())
			require.Contains(t, body, "topicmaintenance")
			require.NotContains(t, body, "ontology")
		})
	}
}

func TestControlTopicMaintenanceReassessmentInputValidation(t *testing.T) {
	router := echo.New()
	router.HTTPErrorHandler = httperr.ErrorHandler
	registerTopicMaintenanceRoutes(router.Group("/control/api"), &controlConfigHandler{
		topicMaintenance: topics.NewMaintenanceService(topics.MaintenanceDependencies{}),
	})
	team, evidence := uuid.NewString(), uuid.NewString()
	tooMany := make([]string, 21)
	for i := range tooMany {
		tooMany[i] = `"` + uuid.NewString() + `"`
	}
	for _, tc := range []struct {
		name, team, body string
		status           int
	}{
		{"missing evidence", team, `{"operation_key":"op"}`, http.StatusUnprocessableEntity},
		{"invalid evidence", team, `{"operation_key":"op","evidence_ids":["invalid"]}`, http.StatusUnprocessableEntity},
		{"too many evidence IDs", team, `{"operation_key":"op","evidence_ids":[` + strings.Join(tooMany, ",") + `]}`, http.StatusUnprocessableEntity},
		{"unknown field", team, `{"operation_key":"op","evidence_ids":["` + evidence + `"],"generation":1}`, http.StatusUnprocessableEntity},
		{"invalid team", "invalid", `{"operation_key":"op","evidence_ids":["` + evidence + `"]}`, http.StatusBadRequest},
		{"valid shape reaches service", team, `{"operation_key":"op","evidence_ids":["` + evidence + `"]}`, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/control/api/teams/"+tc.team+"/topic-maintenance/reassess", strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, tc.status, response.Code, response.Body.String())
		})
	}
}

func TestControlOntologyMaintenanceErrorResponses(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		cause  error
		status int
	}{
		{"invalid command", topic.ErrInvalid, http.StatusUnprocessableEntity},
		{"invalid settings", settings.ErrInvalidAppConfig, http.StatusUnprocessableEntity},
		{"unauthorized", topic.ErrUnauthorized, http.StatusForbidden},
		{"missing run", topic.ErrNotFound, http.StatusNotFound},
		{"conflicting command", topic.ErrConflict, http.StatusConflict},
		{"paused", topic.ErrMaintenancePaused, http.StatusConflict},
		{"disabled", topic.ErrMaintenanceDisabled, http.StatusConflict},
		{"window closed", topic.ErrWindowClosed, http.StatusConflict},
		{"context configuration", topic.ErrConfiguration, http.StatusConflict},
		{"accounting unavailable", topic.ErrAccounting, http.StatusServiceUnavailable},
		{"lease lost", topic.ErrLeaseLost, http.StatusServiceUnavailable},
		{"unknown failure", errors.New("unknown internal failure"), http.StatusServiceUnavailable},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			router := echo.New()
			router.HTTPErrorHandler = httperr.ErrorHandler
			router.GET("/control/api/topic-maintenance/status", func(*echo.Context) error {
				return topicMaintenanceControlError(fmt.Errorf("internal diagnostic details: %w", testCase.cause))
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/control/api/topic-maintenance/status", nil))
			require.Equal(t, testCase.status, response.Code)
			require.NotContains(t, response.Body.String(), "internal diagnostic details")
			require.NotContains(t, response.Body.String(), "unknown internal failure")
		})
	}
}
