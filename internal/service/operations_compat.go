package service

// This compatibility facade preserves the historical operations service
// import path while policy and maintenance live in internal/operations.

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

type (
	OperationLogReader            = operations.OperationLogReader
	OperationLogService           = operations.OperationLogService
	OperationLogRetentionProvider = operations.OperationLogRetentionProvider
	OperationLogServiceImpl       = operations.OperationLogServiceImpl
	TelemetryReader               = operations.TelemetryReader
	TelemetryFilter               = operations.TelemetryFilter
	TelemetrySnapshot             = operations.TelemetrySnapshot
	TelemetryWindow               = operations.TelemetryWindow
	TelemetryScope                = operations.TelemetryScope
	TelemetryCard                 = operations.TelemetryCard
	TelemetrySeries               = operations.TelemetrySeries
	TelemetryPoint                = operations.TelemetryPoint
	PrometheusTelemetryService    = operations.PrometheusTelemetryService
	TelemetryFeatureResolver      = operations.TelemetryFeatureResolver
	TelemetryPricingReader        = operationscontract.TelemetryPricingReader
	TelemetryPricingResolver      = operations.TelemetryPricingResolver
	UsageMetricsRecorder          = operations.UsageMetricsRecorder
	UsageMetricsReader            = operations.UsageMetricsReader
	UsageMetricsService           = operations.UsageMetricsService
	UsageMetricsServiceImpl       = operations.UsageMetricsServiceImpl
	TelemetryLifecycleReader      = operationscontract.TelemetryLifecycleReader
	TelemetryLifecycleFilter      = operationscontract.TelemetryLifecycleFilter
	TelemetryLifecycleSnapshot    = operationscontract.TelemetryLifecycleSnapshot
)

const (
	TelemetryRetentionDays       = operations.TelemetryRetentionDays
	TelemetryAudienceOperator    = operations.TelemetryAudienceOperator
	TelemetryAudienceUser        = operations.TelemetryAudienceUser
	TelemetrySnapshotReady       = operations.TelemetrySnapshotReady
	TelemetrySnapshotDegraded    = operations.TelemetrySnapshotDegraded
	TelemetrySnapshotUnavailable = operations.TelemetrySnapshotUnavailable
	TelemetryItemReady           = operations.TelemetryItemReady
	TelemetryItemInactive        = operations.TelemetryItemInactive
	TelemetryItemUnavailable     = operations.TelemetryItemUnavailable
	TelemetryItemUnsupported     = operations.TelemetryItemUnsupported
	UsageMetricsBucketSeconds    = operations.UsageMetricsBucketSeconds
	UsageMetricsRetentionDays    = operations.UsageMetricsRetentionDays
)

var ErrOperationLogQueueFull = operations.ErrOperationLogQueueFull

func NewOperationLogService(repo operationscontract.OperationLogRepository, retention OperationLogRetentionProvider) *OperationLogServiceImpl {
	return operations.NewOperationLogService(repo, retention)
}

func NewPrometheusTelemetryService(baseURL string, timeout time.Duration) *PrometheusTelemetryService {
	return operations.NewPrometheusTelemetryService(baseURL, timeout)
}

func NewPrometheusTelemetryServiceWithLogger(baseURL string, timeout time.Duration, logger observability.LogProvider) *PrometheusTelemetryService {
	return operations.NewPrometheusTelemetryServiceWithLogger(baseURL, timeout, logger)
}

func NewPrometheusTelemetryServiceWithJobAndLogger(baseURL string, timeout time.Duration, prometheusJob string, logger observability.LogProvider) *PrometheusTelemetryService {
	return operations.NewPrometheusTelemetryServiceWithJobAndLogger(baseURL, timeout, prometheusJob, logger)
}

func NewUsageMetricsService(repo operationscontract.UsageMetricsRepository, logger observability.LogProvider) *UsageMetricsServiceImpl {
	return operations.NewUsageMetricsService(repo, logger)
}

func NewTelemetryPricingResolver(pricing operationscontract.TelemetryPricingReader) observability.AIPricingResolver {
	return operations.NewTelemetryPricingResolver(pricing)
}
