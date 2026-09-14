package http

import (
	nethttp "net/http"

	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/conflict/evidence"
	"github.com/markhuangai/dense-mem/internal/conflict/queue"
	"github.com/markhuangai/dense-mem/internal/dream"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	"github.com/markhuangai/dense-mem/internal/recall"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	settings "github.com/markhuangai/dense-mem/internal/settings"
)

type ControlPortalTelemetry struct {
	Reader            operations.TelemetryReader
	HTTPMetrics       httpcontract.HTTPMetrics
	ScrapeHandler     nethttp.Handler
	ScrapeToken       string
	SSO               *accessservice.SSOService
	Directory         *accessservice.DirectoryIdentityService
	ControlIdentity   *accessservice.ControlIdentityService
	Config            settings.AppConfigService
	Logs              operations.OperationLogReader
	RecallFeedback    recall.RecallFeedbackEventReader
	Dreams            dream.ControlService
	Communities       communityapp.Service
	ConflictQueue     conflictqueue.Reader
	EvidenceConflicts evidenceconflict.Reader
	Convergence       searchapp.SearchConvergenceReader
	RememberAttempts  rememberapp.RememberAttemptDiagnosticsReader
	PrivateMemory     PrivateMemoryServiceInterface
}

type controlPortalHandler struct {
	teams             handler.TeamServiceInterface
	credentials       handler.CredentialServiceInterface
	security          settings.SecurityService
	metrics           operations.UsageMetricsReader
	telemetry         operations.TelemetryReader
	operationLogs     operations.OperationLogReader
	recallFeedback    recall.RecallFeedbackEventReader
	dreams            dream.ControlService
	communities       communityapp.Service
	conflictQueue     conflictqueue.Reader
	evidenceConflicts evidenceconflict.Reader
	convergence       searchapp.SearchConvergenceReader
	rememberAttempts  rememberapp.RememberAttemptDiagnosticsReader
	privateMemory     PrivateMemoryServiceInterface
	health            HealthConfig
	sso               *accessservice.SSOService
	directory         *accessservice.DirectoryIdentityService
	controlIdentity   *accessservice.ControlIdentityService
	appConfig         settings.AppConfigService
	logger            httpcontract.LogProvider
	verifierModel     string
	embeddingModel    string
}
