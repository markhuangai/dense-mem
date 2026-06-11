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
	settings         *domain.SSOConfigSettings
	dreamingSettings *domain.DreamingConfigSettings
	values           map[string]string
	dreamingValues   map[string]string
	getErr           error
	updateErr        error
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

func (s *controlAppConfigSvc) GetDreamingSettings(context.Context) (*domain.DreamingConfigSettings, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.dreamingSettings, nil
}

func (s *controlAppConfigSvc) UpdateDreamingSettings(_ context.Context, values map[string]string, _, _, _ string) (*domain.DreamingConfigSettings, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.dreamingValues == nil {
		s.dreamingValues = make(map[string]string)
	}
	for key, value := range values {
		s.dreamingValues[key] = value
	}
	return s.dreamingSettings, nil
}

func (s *controlAppConfigSvc) DreamingRuntimeConfig(context.Context) (domain.DreamingRuntimeConfig, error) {
	return domain.DreamingRuntimeConfig{}, nil
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

func TestControlPortalDreamingConfigFlows(t *testing.T) {
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	appConfig := &controlAppConfigSvc{
		dreamingSettings: &domain.DreamingConfigSettings{
			UpdateTime: now.Format(time.RFC3339Nano),
			Items: []domain.DreamingConfigItem{{
				Key:            domain.AppConfigDreamingEnabled,
				Value:          "false",
				EffectiveValue: "false",
				UpdatedAt:      now,
			}, {
				Key:            domain.AppConfigDreamingStartTimeLocal,
				Value:          "03:00",
				EffectiveValue: "03:00",
				UpdatedAt:      now,
			}},
			Effective: domain.DreamingRuntimeConfig{
				StartTimeLocal: "03:00",
				Timezone:       "UTC",
				MaxOutputs:     5,
			},
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

	rec := do(http.MethodGet, "/control/api/config/dreaming", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"update_time":"2026-06-11T03:00:00Z"`)
	require.Contains(t, rec.Body.String(), `"start_time_local":"03:00"`)

	rec = do(http.MethodPatch, "/control/api/config/dreaming", `{"items":[{"key":"DREAMING_START_TIME_LOCAL","value":"02:30"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "02:30", appConfig.dreamingValues[domain.AppConfigDreamingStartTimeLocal])

	rec = do(http.MethodPatch, "/control/api/config/dreaming", "{")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	appConfig.updateErr = service.ErrInvalidAppConfig
	rec = do(http.MethodPatch, "/control/api/config/dreaming", `{"items":[{"key":"DREAMING_MAX_OUTPUTS","value":"0"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}
