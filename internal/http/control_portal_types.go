package http

import (
	nethttp "net/http"

	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/conflictqueue"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

type ControlPortalTelemetry struct {
	Reader          service.TelemetryReader
	HTTPMetrics     observability.HTTPMetrics
	ScrapeHandler   nethttp.Handler
	ScrapeToken     string
	SSO             *service.SSOService
	Directory       *service.DirectoryIdentityService
	ControlIdentity *service.ControlIdentityService
	Config          service.AppConfigService
	Logs            service.OperationLogReader
	RecallFeedback  service.RecallFeedbackEventReader
	Dreams          dreamservice.ControlService
	Communities     communityservice.Service
	ConflictQueue   conflictqueue.Reader
	Convergence     service.SearchConvergenceReader
}

type controlPortalHandler struct {
	profiles        handler.ProfileServiceInterface
	keys            handler.APIKeyServiceInterface
	security        service.SecurityService
	metrics         service.UsageMetricsReader
	telemetry       service.TelemetryReader
	operationLogs   service.OperationLogReader
	recallFeedback  service.RecallFeedbackEventReader
	dreams          dreamservice.ControlService
	communities     communityservice.Service
	conflictQueue   conflictqueue.Reader
	convergence     service.SearchConvergenceReader
	health          HealthConfig
	sso             *service.SSOService
	directory       *service.DirectoryIdentityService
	controlIdentity *service.ControlIdentityService
	appConfig       service.AppConfigService
	logger          observability.LogProvider
	verifierModel   string
	embeddingModel  string
}
