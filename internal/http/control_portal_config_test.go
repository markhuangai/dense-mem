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
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

type controlAppConfigSvc struct {
	settings  *domain.SSOConfigSettings
	values    map[string]string
	getErr    error
	updateErr error
}

func (s *controlAppConfigSvc) GetSSOSettings(context.Context) (*domain.SSOConfigSettings, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.settings, nil
}

func (s *controlAppConfigSvc) UpdateSSOSettings(_ context.Context, values map[string]string, _, _, _ string) (*domain.SSOConfigSettings, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	for key, value := range values {
		s.values[key] = value
	}
	return s.settings, nil
}

func (s *controlAppConfigSvc) SSORuntimeConfig(context.Context) (service.SSORuntimeConfig, error) {
	return service.SSORuntimeConfig{}, nil
}

func TestControlPortalSSOConfigFlows(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	appConfig := &controlAppConfigSvc{
		settings: &domain.SSOConfigSettings{
			UpdateTime: now.Format(time.RFC3339Nano),
			Items: []domain.SSOConfigItem{{
				Key:            domain.AppConfigSSOPublicBaseURL,
				Value:          "",
				EffectiveValue: "",
				UpdatedAt:      now,
			}, {
				Key:            domain.AppConfigSSOSessionTTLSeconds,
				Value:          "",
				EffectiveValue: "28800",
				UpdatedAt:      now,
			}},
		},
	}
	e, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{
		Config: appConfig,
	}, HealthConfig{}, nil)
	require.NoError(t, err)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodGet, "/control/api/config/sso", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"update_time":"2026-06-09T12:00:00Z"`)
	require.Contains(t, rec.Body.String(), `"effective_value":"28800"`)

	rec = do(http.MethodPatch, "/control/api/config/sso", `{"items":[{"key":"SSO_PUBLIC_BASE_URL","value":"https://portal.example.com"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "https://portal.example.com", appConfig.values[domain.AppConfigSSOPublicBaseURL])

	rec = do(http.MethodPatch, "/control/api/config/sso", "{")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	appConfig.updateErr = service.ErrInvalidAppConfig
	rec = do(http.MethodPatch, "/control/api/config/sso", `{"items":[{"key":"SSO_SESSION_TTL_SECONDS","value":"0"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	appConfig.updateErr = errors.New("db failed")
	rec = do(http.MethodPatch, "/control/api/config/sso", `{"items":[{"key":"SSO_SESSION_TTL_SECONDS","value":"3600"}]}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
