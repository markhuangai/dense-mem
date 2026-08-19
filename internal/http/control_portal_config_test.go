package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

type controlAppConfigSvc struct {
	generalSettings       *domain.GeneralConfigSettings
	settings              *domain.SSOConfigSettings
	dreamingSettings      *domain.DreamingConfigSettings
	communitySettings     *domain.CommunityDetectionConfigSettings
	operationLogSettings  *domain.OperationLogConfigSettings
	privateMemorySettings *domain.PrivateMemoryConfigSettings
	recallSettings        *domain.RecallFeedbackConfigSettings
	telemetrySettings     *domain.TelemetryPricingConfigSettings
	generalValues         map[string]string
	values                map[string]string
	dreamingValues        map[string]string
	communityValues       map[string]string
	operationLogValues    map[string]string
	privateMemoryValues   map[string]string
	recallValues          map[string]string
	telemetryValues       map[string]string
	getErr                error
	updateErr             error
	dreamingRuntime       domain.DreamingRuntimeConfig
	dreamingRuntimeErr    error
	recallRuntime         domain.RecallFeedbackRuntimeConfig
	recallRuntimeErr      error
	telemetryRuntime      domain.TelemetryPricingRuntimeConfig
	telemetryRuntimeErr   error
	privateMemoryRuntime  domain.PrivateMemoryRuntimeConfig
}

func (s *controlAppConfigSvc) GetGeneralSettings(context.Context) (*domain.GeneralConfigSettings, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.generalSettings, nil
}

func (s *controlAppConfigSvc) UpdateGeneralSettings(_ context.Context, values map[string]string, _, _, _ string) (*domain.GeneralConfigSettings, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.generalValues == nil {
		s.generalValues = make(map[string]string)
	}
	for key, value := range values {
		s.generalValues[key] = value
	}
	return s.generalSettings, nil
}

func (s *controlAppConfigSvc) GeneralRuntimeConfig(context.Context) (domain.GeneralRuntimeConfig, error) {
	return domain.GeneralRuntimeConfig{}, nil
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

func (s *controlAppConfigSvc) GetPrivateMemorySettings(context.Context) (*domain.PrivateMemoryConfigSettings, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.privateMemorySettings, nil
}

func (s *controlAppConfigSvc) UpdatePrivateMemorySettings(_ context.Context, values map[string]string, _, _, _ string) (*domain.PrivateMemoryConfigSettings, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.privateMemoryValues == nil {
		s.privateMemoryValues = make(map[string]string)
	}
	for key, value := range values {
		s.privateMemoryValues[key] = value
	}
	return s.privateMemorySettings, nil
}

func (s *controlAppConfigSvc) PrivateMemoryRuntimeConfig(context.Context) (domain.PrivateMemoryRuntimeConfig, error) {
	return s.privateMemoryRuntime, nil
}

func (s *controlAppConfigSvc) GetRecallFeedbackSettings(context.Context) (*domain.RecallFeedbackConfigSettings, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.recallSettings, nil
}

func (s *controlAppConfigSvc) UpdateRecallFeedbackSettings(_ context.Context, values map[string]string, _, _, _ string) (*domain.RecallFeedbackConfigSettings, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.recallValues == nil {
		s.recallValues = make(map[string]string)
	}
	for key, value := range values {
		s.recallValues[key] = value
	}
	if raw, ok := values[domain.AppConfigRecallFeedbackEnabled]; ok {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, service.ErrInvalidAppConfig
		}
		s.recallRuntime.Enabled = enabled
		if s.recallSettings != nil {
			s.recallSettings.Effective.Enabled = enabled
			for i := range s.recallSettings.Items {
				if s.recallSettings.Items[i].Key == domain.AppConfigRecallFeedbackEnabled {
					s.recallSettings.Items[i].Value = raw
					s.recallSettings.Items[i].EffectiveValue = strconv.FormatBool(enabled)
				}
			}
		}
	}
	if raw, ok := values[domain.AppConfigRecallFeedbackRetentionDays]; ok {
		retentionDays, err := strconv.Atoi(raw)
		if err != nil || retentionDays < 1 || retentionDays > 365 {
			return nil, service.ErrInvalidAppConfig
		}
		s.recallRuntime.RetentionDays = retentionDays
		if s.recallSettings != nil {
			s.recallSettings.Effective.RetentionDays = retentionDays
			for i := range s.recallSettings.Items {
				if s.recallSettings.Items[i].Key == domain.AppConfigRecallFeedbackRetentionDays {
					s.recallSettings.Items[i].Value = raw
					s.recallSettings.Items[i].EffectiveValue = strconv.Itoa(retentionDays)
				}
			}
		}
	}
	return s.recallSettings, nil
}

func (s *controlAppConfigSvc) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return s.recallRuntime, s.recallRuntimeErr
}

func (s *controlAppConfigSvc) GetTelemetryPricingSettings(context.Context) (*domain.TelemetryPricingConfigSettings, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.telemetrySettings, nil
}

func (s *controlAppConfigSvc) UpdateTelemetryPricingSettings(_ context.Context, values map[string]string, _, _, _ string) (*domain.TelemetryPricingConfigSettings, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.telemetryValues == nil {
		s.telemetryValues = make(map[string]string)
	}
	for key, value := range values {
		s.telemetryValues[key] = value
	}
	return s.telemetrySettings, nil
}

func (s *controlAppConfigSvc) TelemetryPricingRuntimeConfig(context.Context) (domain.TelemetryPricingRuntimeConfig, error) {
	return s.telemetryRuntime, s.telemetryRuntimeErr
}

func TestControlPortalGeneralConfigFlows(t *testing.T) {
	now := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	appConfig := &controlAppConfigSvc{
		generalSettings: &domain.GeneralConfigSettings{
			UpdateTime: now.Format(time.RFC3339Nano),
			Items: []domain.GeneralConfigItem{{
				Key:            domain.AppConfigTimezone,
				Value:          "Local",
				EffectiveValue: "Local",
				UpdatedAt:      now,
			}},
			Effective: domain.GeneralRuntimeConfig{Timezone: "Local"},
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

	rec := do(http.MethodGet, "/control/api/config/general", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"timezone":"Local"`)

	rec = do(http.MethodPatch, "/control/api/config/general", `{"items":[{"key":"APP_TIMEZONE","value":"America/New_York"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "America/New_York", appConfig.generalValues[domain.AppConfigTimezone])

	rec = do(http.MethodPatch, "/control/api/config/general", "{")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	appConfig.updateErr = service.ErrInvalidAppConfig
	rec = do(http.MethodPatch, "/control/api/config/general", `{"items":[{"key":"APP_TIMEZONE","value":"Nope/Zone"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
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

func TestControlPortalPrivateMemoryConfigFlows(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	appConfig := &controlAppConfigSvc{
		privateMemorySettings: &domain.PrivateMemoryConfigSettings{
			UpdateTime: now.Format(time.RFC3339Nano),
			Items: []domain.PrivateMemoryConfigItem{{
				Key:            domain.AppConfigPrivateMemoryRetentionDays,
				Value:          "0",
				EffectiveValue: "0",
				UpdatedAt:      now,
			}},
			Effective: domain.PrivateMemoryRuntimeConfig{RetentionDays: 0},
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

	rec := do(http.MethodGet, "/control/api/config/private-memory", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"update_time":"2026-08-18T12:00:00Z"`)
	require.Contains(t, rec.Body.String(), `"retention_days":0`)

	rec = do(http.MethodPatch, "/control/api/config/private-memory", `{"items":[{"key":"PRIVATE_MEMORY_RETENTION_DAYS","value":"30"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "30", appConfig.privateMemoryValues[domain.AppConfigPrivateMemoryRetentionDays])

	rec = do(http.MethodPatch, "/control/api/config/private-memory", "{")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec = do(http.MethodPatch, "/control/api/config/private-memory", `{"items":[{"key":"PRIVATE_MEMORY_RETENTION_DAYS","value":"14","unknown":true}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "30", appConfig.privateMemoryValues[domain.AppConfigPrivateMemoryRetentionDays])

	rec = do(http.MethodPatch, "/control/api/config/private-memory", `{"items":[{"key":"PRIVATE_MEMORY_RETENTION_DAYS","value":"14"},{"key":"PRIVATE_MEMORY_RETENTION_DAYS","value":"7"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "30", appConfig.privateMemoryValues[domain.AppConfigPrivateMemoryRetentionDays])

	appConfig.updateErr = service.ErrInvalidAppConfig
	rec = do(http.MethodPatch, "/control/api/config/private-memory", `{"items":[{"key":"PRIVATE_MEMORY_RETENTION_DAYS","value":"-1"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
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

func TestControlPortalRecallFeedbackConfigFlows(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	appConfig := &controlAppConfigSvc{
		recallSettings: &domain.RecallFeedbackConfigSettings{
			UpdateTime: now.Format(time.RFC3339Nano),
			Items: []domain.RecallFeedbackConfigItem{{
				Key:            domain.AppConfigRecallFeedbackEnabled,
				Value:          "false",
				EffectiveValue: "false",
				UpdatedAt:      now,
			}, {
				Key:            domain.AppConfigRecallFeedbackRetentionDays,
				Value:          "30",
				EffectiveValue: "30",
				UpdatedAt:      now,
			}},
			Effective: domain.RecallFeedbackRuntimeConfig{Enabled: false, RetentionDays: 30},
		},
		recallRuntime: domain.RecallFeedbackRuntimeConfig{Enabled: false, RetentionDays: 30},
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

	rec := do(http.MethodGet, "/control/api/config/recall-feedback", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"enabled":false`)
	require.Contains(t, rec.Body.String(), `"retention_days":30`)

	rec = do(http.MethodPatch, "/control/api/config/recall-feedback", `{"items":[{"key":"RECALL_FEEDBACK_ENABLED","value":"true"},{"key":"RECALL_FEEDBACK_RETENTION_DAYS","value":"45"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", appConfig.recallValues[domain.AppConfigRecallFeedbackEnabled])
	require.Equal(t, "45", appConfig.recallValues[domain.AppConfigRecallFeedbackRetentionDays])
	require.Contains(t, rec.Body.String(), `"enabled":true`)
	require.Contains(t, rec.Body.String(), `"retention_days":45`)

	rec = do(http.MethodPatch, "/control/api/config/recall-feedback", "{")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	appConfig.updateErr = service.ErrInvalidAppConfig
	rec = do(http.MethodPatch, "/control/api/config/recall-feedback", `{"items":[{"key":"RECALL_FEEDBACK_ENABLED","value":"maybe"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestControlPortalTelemetryPricingConfigFlows(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	verifierInput, verifierOutput, embeddingInput := 1.25, 2.5, 0.1
	appConfig := &controlAppConfigSvc{
		telemetrySettings: &domain.TelemetryPricingConfigSettings{
			UpdateTime: now.Format(time.RFC3339Nano),
			Items: []domain.TelemetryPricingConfigItem{{
				Key:             domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens,
				Value:           "1.25",
				EffectiveValue:  "1.25",
				ValidationError: "",
				UpdatedAt:       now,
			}},
			Effective: domain.TelemetryPricingRuntimeConfig{
				VerifierInputUSDPerMillionTokens:  &verifierInput,
				VerifierOutputUSDPerMillionTokens: &verifierOutput,
				EmbeddingInputUSDPerMillionTokens: &embeddingInput,
			},
		},
	}
	e, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
		AIVerifierModel:    "configured-verifier",
		AIEmbeddingModel:   "configured-embedding",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{Config: appConfig}, HealthConfig{}, nil)
	require.NoError(t, err)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodGet, "/control/api/config/telemetry-pricing", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"verifier_model":"configured-verifier"`)
	require.Contains(t, rec.Body.String(), `"embedding_model":"configured-embedding"`)
	require.Contains(t, rec.Body.String(), `"verifier_input_usd_per_million_tokens":1.25`)
	require.NotContains(t, rec.Body.String(), `"validation_error"`)

	appConfig.telemetrySettings.Items[0].ValidationError = "rate must be repaired"
	rec = do(http.MethodGet, "/control/api/config/telemetry-pricing", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"validation_error":"rate must be repaired"`)
	appConfig.telemetrySettings.Items[0].ValidationError = ""

	rec = do(http.MethodPatch, "/control/api/config/telemetry-pricing", `{"items":[{"key":"TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS","value":"3"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "3", appConfig.telemetryValues[domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens])

	rec = do(http.MethodPatch, "/control/api/config/telemetry-pricing", "{")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	for _, body := range []string{`{}`, `{"telemetry_items":[]}`} {
		rec = do(http.MethodPatch, "/control/api/config/telemetry-pricing", body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		require.Equal(t, "3", appConfig.telemetryValues[domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens])
	}
	for _, body := range []string{
		`{"items":[{"key":"TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS"}]}`,
		`{"items":[{"key":"TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS","rate":"4"}]}`,
		`{"items":[{"key":"TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS","value":null}]}`,
	} {
		rec = do(http.MethodPatch, "/control/api/config/telemetry-pricing", body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		require.Equal(t, "3", appConfig.telemetryValues[domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens])
	}

	rec = do(http.MethodPatch, "/control/api/config/telemetry-pricing", `{"items":[{"key":"TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS","value":""}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "", appConfig.telemetryValues[domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens])

	appConfig.updateErr = service.ErrInvalidAppConfig
	rec = do(http.MethodPatch, "/control/api/config/telemetry-pricing", `{"items":[{"key":"TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS","value":"-1"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestControlConfigNilResponses(t *testing.T) {
	require.Empty(t, toControlGeneralConfig(nil).Items)
	require.Empty(t, toControlSSOConfig(nil).Items)
	require.Empty(t, toControlDreamingConfig(nil).Items)
	require.Empty(t, toControlCommunityDetectionConfig(nil).Items)
	require.Empty(t, toControlOperationLogConfig(nil).Items)
	require.Empty(t, toControlPrivateMemoryConfig(nil).Items)
	require.Empty(t, toControlRecallFeedbackConfig(nil).Items)
	require.Empty(t, toControlTelemetryPricingConfig(nil, "", "").Items)
}

func TestControlPortalConfigUnavailableHandlers(t *testing.T) {
	tests := []struct {
		name string
		call func(*controlPortalHandler, echo.Context) error
	}{
		{name: "get general", call: func(h *controlPortalHandler, c echo.Context) error { return h.getGeneralConfig(c) }},
		{name: "update general", call: func(h *controlPortalHandler, c echo.Context) error { return h.updateGeneralConfig(c) }},
		{name: "get sso", call: func(h *controlPortalHandler, c echo.Context) error { return h.getSSOConfig(c) }},
		{name: "update sso", call: func(h *controlPortalHandler, c echo.Context) error { return h.updateSSOConfig(c) }},
		{name: "get dreaming", call: func(h *controlPortalHandler, c echo.Context) error { return h.getDreamingConfig(c) }},
		{name: "update dreaming", call: func(h *controlPortalHandler, c echo.Context) error { return h.updateDreamingConfig(c) }},
		{name: "get community", call: func(h *controlPortalHandler, c echo.Context) error { return h.getCommunityDetectionConfig(c) }},
		{name: "update community", call: func(h *controlPortalHandler, c echo.Context) error { return h.updateCommunityDetectionConfig(c) }},
		{name: "get operation logs", call: func(h *controlPortalHandler, c echo.Context) error { return h.getOperationLogConfig(c) }},
		{name: "update operation logs", call: func(h *controlPortalHandler, c echo.Context) error { return h.updateOperationLogConfig(c) }},
		{name: "get private memory", call: func(h *controlPortalHandler, c echo.Context) error { return h.getPrivateMemoryConfig(c) }},
		{name: "update private memory", call: func(h *controlPortalHandler, c echo.Context) error { return h.updatePrivateMemoryConfig(c) }},
		{name: "get recall feedback", call: func(h *controlPortalHandler, c echo.Context) error { return h.getRecallFeedbackConfig(c) }},
		{name: "update recall feedback", call: func(h *controlPortalHandler, c echo.Context) error { return h.updateRecallFeedbackConfig(c) }},
		{name: "get telemetry pricing", call: func(h *controlPortalHandler, c echo.Context) error { return h.getTelemetryPricingConfig(c) }},
		{name: "update telemetry pricing", call: func(h *controlPortalHandler, c echo.Context) error { return h.updateTelemetryPricingConfig(c) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, tt.call(&controlPortalHandler{}, newControlConfigContext(http.MethodPatch, `{}`)), "app config service unavailable")
		})
	}
}

func TestControlPortalConfigGetErrors(t *testing.T) {
	h := &controlPortalHandler{appConfig: &controlAppConfigSvc{getErr: errors.New("repo failed")}}
	tests := []struct {
		name string
		call func(echo.Context) error
	}{
		{name: "general", call: h.getGeneralConfig},
		{name: "sso", call: h.getSSOConfig},
		{name: "dreaming", call: h.getDreamingConfig},
		{name: "community detection", call: h.getCommunityDetectionConfig},
		{name: "operation logs", call: h.getOperationLogConfig},
		{name: "private memory", call: h.getPrivateMemoryConfig},
		{name: "recall feedback", call: h.getRecallFeedbackConfig},
		{name: "telemetry pricing", call: h.getTelemetryPricingConfig},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(newControlConfigContext(http.MethodGet, ""))
			if tt.name == "private memory" {
				var apiErr *httperr.APIError
				require.ErrorAs(t, err, &apiErr)
				require.Equal(t, httperr.SERVICE_UNAVAILABLE, apiErr.Code)
				require.NotContains(t, err.Error(), "repo failed")
				return
			}
			require.ErrorContains(t, err, "repo failed")
		})
	}
}

func TestControlPortalConfigUpdateBackendErrors(t *testing.T) {
	backendErr := errors.New("db failed")
	tests := []struct {
		name string
		call func(*controlPortalHandler, echo.Context) error
		body string
	}{
		{
			name: "general",
			call: func(h *controlPortalHandler, c echo.Context) error { return h.updateGeneralConfig(c) },
			body: `{"items":[{"key":"APP_TIMEZONE","value":"UTC"}]}`,
		},
		{
			name: "dreaming",
			call: func(h *controlPortalHandler, c echo.Context) error { return h.updateDreamingConfig(c) },
			body: `{"items":[{"key":"DREAMING_ENABLED","value":"true"}]}`,
		},
		{
			name: "community detection",
			call: func(h *controlPortalHandler, c echo.Context) error { return h.updateCommunityDetectionConfig(c) },
			body: `{"items":[{"key":"COMMUNITY_DETECTION_ENABLED","value":"true"}]}`,
		},
		{
			name: "operation logs",
			call: func(h *controlPortalHandler, c echo.Context) error { return h.updateOperationLogConfig(c) },
			body: `{"items":[{"key":"OPERATION_LOG_RETENTION_DAYS","value":"45"}]}`,
		},
		{
			name: "private memory",
			call: func(h *controlPortalHandler, c echo.Context) error {
				return httpmw.BindAndValidateStrict[controlPrivateMemoryConfigRequest](privateMemoryConfigBodyKey)(h.updatePrivateMemoryConfig)(c)
			},
			body: `{"items":[{"key":"PRIVATE_MEMORY_RETENTION_DAYS","value":"30"}]}`,
		},
		{
			name: "recall feedback",
			call: func(h *controlPortalHandler, c echo.Context) error { return h.updateRecallFeedbackConfig(c) },
			body: `{"items":[{"key":"RECALL_FEEDBACK_ENABLED","value":"true"}]}`,
		},
		{
			name: "telemetry pricing",
			call: func(h *controlPortalHandler, c echo.Context) error { return h.updateTelemetryPricingConfig(c) },
			body: `{"items":[{"key":"TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS","value":"1"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &controlPortalHandler{appConfig: &controlAppConfigSvc{updateErr: backendErr}}
			err := tt.call(h, newControlConfigContext(http.MethodPatch, tt.body))
			if tt.name == "private memory" {
				var apiErr *httperr.APIError
				require.ErrorAs(t, err, &apiErr)
				require.Equal(t, httperr.SERVICE_UNAVAILABLE, apiErr.Code)
				require.NotContains(t, err.Error(), backendErr.Error())
				return
			}
			require.ErrorIs(t, err, backendErr)
		})
	}
}

func TestControlPortalOperationLogConfigUnavailable(t *testing.T) {
	require.ErrorContains(t, (&controlPortalHandler{}).getOperationLogConfig(newControlConfigContext(http.MethodGet, "")), "app config service unavailable")
	require.ErrorContains(t, (&controlPortalHandler{}).updateOperationLogConfig(newControlConfigContext(http.MethodPatch, `{}`)), "app config service unavailable")
}

func TestControlPortalRemovedProfileRoutesReturnNotFound(t *testing.T) {
	profiles, _, server := testControlServer(t)
	teamID := profiles.profiles[0].ID
	for _, path := range []string{
		"/control/api/profiles",
		"/control/api/teams/" + teamID.String() + "/profiles",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Control-Portal-Token", "secret")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code, "removed path %q", path)
	}
}

func newControlConfigContext(method, body string) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}
