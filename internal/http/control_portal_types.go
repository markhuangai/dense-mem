package http

import (
	nethttp "net/http"

	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
)

type ControlPortalTelemetry struct {
	Reader         service.TelemetryReader
	HTTPMetrics    observability.HTTPMetrics
	ScrapeHandler  nethttp.Handler
	ScrapeToken    string
	SSO            *service.SSOService
	Config         service.AppConfigService
	Logs           service.OperationLogReader
	RecallFeedback service.RecallFeedbackEventReader
	Dreams         dreamservice.Service
	Migration      migrationcontrol.Service
}

type controlPortalHandler struct {
	profiles       handler.ProfileServiceInterface
	keys           handler.APIKeyServiceInterface
	security       service.SecurityService
	metrics        service.UsageMetricsReader
	telemetry      service.TelemetryReader
	operationLogs  service.OperationLogReader
	recallFeedback service.RecallFeedbackEventReader
	dreams         dreamservice.Service
	migration      migrationcontrol.Service
	health         HealthConfig
	sso            *service.SSOService
	appConfig      service.AppConfigService
}
