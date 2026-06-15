package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

type controlAppConfigSvc struct {
	settings             *domain.SSOConfigSettings
	dreamingSettings     *domain.DreamingConfigSettings
	communitySettings    *domain.CommunityDetectionConfigSettings
	operationLogSettings *domain.OperationLogConfigSettings
	values               map[string]string
	dreamingValues       map[string]string
	communityValues      map[string]string
	operationLogValues   map[string]string
	getErr               error
	updateErr            error
	dreamingRuntime      domain.DreamingRuntimeConfig
	dreamingRuntimeErr   error
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
	return s.dreamingRuntime, s.dreamingRuntimeErr
}

func (s *controlAppConfigSvc) GetCommunityDetectionSettings(context.Context) (*domain.CommunityDetectionConfigSettings, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.communitySettings, nil
}

func (s *controlAppConfigSvc) UpdateCommunityDetectionSettings(_ context.Context, values map[string]string, _, _, _ string) (*domain.CommunityDetectionConfigSettings, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.communityValues == nil {
		s.communityValues = make(map[string]string)
	}
	for key, value := range values {
		s.communityValues[key] = value
	}
	return s.communitySettings, nil
}

func (s *controlAppConfigSvc) CommunityDetectionRuntimeConfig(context.Context) (domain.CommunityDetectionRuntimeConfig, error) {
	return domain.CommunityDetectionRuntimeConfig{}, nil
}

func (s *controlAppConfigSvc) GetOperationLogSettings(context.Context) (*domain.OperationLogConfigSettings, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.operationLogSettings, nil
}

func (s *controlAppConfigSvc) UpdateOperationLogSettings(_ context.Context, values map[string]string, _, _, _ string) (*domain.OperationLogConfigSettings, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.operationLogValues == nil {
		s.operationLogValues = make(map[string]string)
	}
	for key, value := range values {
		s.operationLogValues[key] = value
	}
	return s.operationLogSettings, nil
}

func (s *controlAppConfigSvc) OperationLogRuntimeConfig(context.Context) (domain.OperationLogRuntimeConfig, error) {
	return domain.OperationLogRuntimeConfig{}, nil
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

func TestControlPortalCommunityDetectionConfigFlows(t *testing.T) {
	now := time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC)
	appConfig := &controlAppConfigSvc{
		communitySettings: &domain.CommunityDetectionConfigSettings{
			UpdateTime: now.Format(time.RFC3339Nano),
			Items: []domain.CommunityDetectionConfigItem{{
				Key:            domain.AppConfigCommunityDetectionEnabled,
				Value:          "false",
				EffectiveValue: "false",
				UpdatedAt:      now,
			}, {
				Key:            domain.AppConfigCommunityDetectionStartTimeLocal,
				Value:          "03:30",
				EffectiveValue: "03:30",
				UpdatedAt:      now,
			}},
			Effective: domain.CommunityDetectionRuntimeConfig{
				StartTimeLocal: "03:30",
				Timezone:       "Local",
				MaxConcurrency: 1,
				JitterSeconds:  600,
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

	rec := do(http.MethodGet, "/control/api/config/community-detection", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"update_time":"2026-06-15T03:30:00Z"`)
	require.Contains(t, rec.Body.String(), `"start_time_local":"03:30"`)

	rec = do(http.MethodPatch, "/control/api/config/community-detection", `{"items":[{"key":"COMMUNITY_DETECTION_ENABLED","value":"true"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", appConfig.communityValues[domain.AppConfigCommunityDetectionEnabled])

	rec = do(http.MethodPatch, "/control/api/config/community-detection", "{")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	appConfig.updateErr = service.ErrInvalidAppConfig
	rec = do(http.MethodPatch, "/control/api/config/community-detection", `{"items":[{"key":"COMMUNITY_DETECTION_MAX_CONCURRENCY","value":"0"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestControlPortalOperationLogConfigFlows(t *testing.T) {
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	appConfig := &controlAppConfigSvc{
		operationLogSettings: &domain.OperationLogConfigSettings{
			UpdateTime: now.Format(time.RFC3339Nano),
			Items: []domain.OperationLogConfigItem{{
				Key:            domain.AppConfigOperationLogRetentionDays,
				Value:          "30",
				EffectiveValue: "30",
				UpdatedAt:      now,
			}},
			Effective: domain.OperationLogRuntimeConfig{RetentionDays: 30},
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

	rec := do(http.MethodGet, "/control/api/config/operation-logs", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"update_time":"2026-06-14T12:00:00Z"`)
	require.Contains(t, rec.Body.String(), `"retention_days":30`)

	rec = do(http.MethodPatch, "/control/api/config/operation-logs", `{"items":[{"key":"OPERATION_LOG_RETENTION_DAYS","value":"45"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "45", appConfig.operationLogValues[domain.AppConfigOperationLogRetentionDays])

	rec = do(http.MethodPatch, "/control/api/config/operation-logs", "{")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	appConfig.updateErr = service.ErrInvalidAppConfig
	rec = do(http.MethodPatch, "/control/api/config/operation-logs", `{"items":[{"key":"OPERATION_LOG_RETENTION_DAYS","value":"0"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestControlConfigNilResponses(t *testing.T) {
	require.Empty(t, toControlSSOConfig(nil).Items)
	require.Empty(t, toControlDreamingConfig(nil).Items)
	require.Empty(t, toControlCommunityDetectionConfig(nil).Items)
	require.Empty(t, toControlOperationLogConfig(nil).Items)
}

func TestControlPortalOperationLogConfigUnavailable(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	h := &controlPortalHandler{}

	require.ErrorContains(t, h.getOperationLogConfig(c), "app config service unavailable")
	require.ErrorContains(t, h.updateOperationLogConfig(c), "app config service unavailable")
}
