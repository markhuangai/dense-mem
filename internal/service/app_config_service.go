package service

// This compatibility facade keeps the historical service import path stable.
// Application-settings policy and caching live in internal/settings.

import (
	settings "github.com/markhuangai/dense-mem/internal/settings"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
)

type AppConfigService = settings.AppConfigService
type AppConfigServiceImpl = settings.AppConfigServiceImpl
type PrivateMemoryConfigService = settings.PrivateMemoryConfigService

const (
	DefaultAppConfigCacheCheckInterval      = settings.DefaultAppConfigCacheCheckInterval
	DefaultAppTimezone                      = settings.DefaultAppTimezone
	DefaultOperationLogRetentionDays        = settings.DefaultOperationLogRetentionDays
	DefaultRecallFeedbackRetentionDays      = settings.DefaultRecallFeedbackRetentionDays
	DefaultCommunityDetectionStartTimeLocal = settings.DefaultCommunityDetectionStartTimeLocal
	DefaultCommunityDetectionMaxConcurrency = settings.DefaultCommunityDetectionMaxConcurrency
	DefaultCommunityDetectionJitterSeconds  = settings.DefaultCommunityDetectionJitterSeconds
	DefaultPrivateMemoryRetentionDays       = settings.DefaultPrivateMemoryRetentionDays
)

var ErrInvalidAppConfig = settings.ErrInvalidAppConfig

func NewAppConfigService(repo settingscontract.AppConfigRepository, audit AuditService) *AppConfigServiceImpl {
	return settings.NewAppConfigService(repo, audit)
}
