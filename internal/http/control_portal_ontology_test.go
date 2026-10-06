package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/httperr"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/settings"
	"github.com/stretchr/testify/require"
)

func TestControlOntologyMaintenanceErrorResponses(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		cause  error
		status int
	}{
		{"invalid command", ontology.ErrInvalid, http.StatusUnprocessableEntity},
		{"invalid settings", settings.ErrInvalidAppConfig, http.StatusUnprocessableEntity},
		{"unauthorized", ontology.ErrUnauthorized, http.StatusForbidden},
		{"missing run", ontology.ErrNotFound, http.StatusNotFound},
		{"conflicting command", ontology.ErrConflict, http.StatusConflict},
		{"paused", ontology.ErrMaintenancePaused, http.StatusConflict},
		{"disabled", ontology.ErrMaintenanceDisabled, http.StatusConflict},
		{"budget deferred", ontology.ErrBudgetDeferred, http.StatusConflict},
		{"accounting unavailable", ontology.ErrAccounting, http.StatusServiceUnavailable},
		{"lease lost", ontology.ErrLeaseLost, http.StatusServiceUnavailable},
		{"unknown failure", errors.New("unknown internal failure"), http.StatusServiceUnavailable},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			router := echo.New()
			router.HTTPErrorHandler = httperr.ErrorHandler
			router.GET("/control/api/ontology/status", func(echo.Context) error {
				return ontologyControlError(fmt.Errorf("internal diagnostic details: %w", testCase.cause))
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/control/api/ontology/status", nil))
			require.Equal(t, testCase.status, response.Code)
			require.NotContains(t, response.Body.String(), "internal diagnostic details")
			require.NotContains(t, response.Body.String(), "unknown internal failure")
		})
	}
}
