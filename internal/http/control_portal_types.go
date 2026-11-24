package http

import (
	"github.com/markhuangai/dense-mem/internal/conflict/evidence"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	"github.com/markhuangai/dense-mem/internal/recall"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	topics "github.com/markhuangai/dense-mem/internal/topics"
	nethttp "net/http"
	"time"
)

type PostgresPoolMetrics struct {
	ProcessStartedAt time.Time `json:"process_started_at"`
	WaitCount        int64     `json:"wait_count"`
	WaitDurationNS   int64     `json:"wait_duration_ns"`
}

type ControlPortalTelemetry struct {
	AuditExport         AuditExportReader
	Diagnostics         DiagnosticBundleReader
	Reader              operations.TelemetryReader
	HTTPMetrics         httpcontract.HTTPMetrics
	ScrapeHandler       nethttp.Handler
	ScrapeToken         string
	SSO                 *accessservice.SSOService
	Directory           *accessservice.DirectoryIdentityService
	ControlIdentity     *accessservice.ControlIdentityService
	Config              settings.AppConfigService
	Decisions           settings.DecisionConfigService
	Logs                operations.OperationLogReader
	RecallFeedback      recall.RecallFeedbackEventReader
	TopicMaintenance    *topics.MaintenanceService
	EvidenceConflicts   evidenceconflict.Reader
	Convergence         searchapp.SearchConvergenceReader
	RememberAttempts    rememberapp.RememberAttemptDiagnosticsReader
	RememberInvocations rememberapp.RememberInvocationDiagnosticsReader
	PrivateMemory       PrivateMemoryServiceInterface
	PostgresPool        func() PostgresPoolMetrics
}

type controlPortalHandler struct {
	auditExport         AuditExportReader
	diagnostics         DiagnosticBundleReader
	teams               handler.TeamServiceInterface
	credentials         handler.CredentialServiceInterface
	security            settings.SecurityService
	metrics             operations.UsageMetricsReader
	postgresPool        func() PostgresPoolMetrics
	telemetry           operations.TelemetryReader
	operationLogs       operations.OperationLogReader
	recallFeedback      recall.RecallFeedbackEventReader
	topicMaintenance    *topics.MaintenanceService
	evidenceConflicts   evidenceconflict.Reader
	convergence         searchapp.SearchConvergenceReader
	rememberAttempts    rememberapp.RememberAttemptDiagnosticsReader
	rememberInvocations rememberapp.RememberInvocationDiagnosticsReader
	privateMemory       PrivateMemoryServiceInterface
	health              HealthConfig
	sso                 *accessservice.SSOService
	directory           *accessservice.DirectoryIdentityService
	controlIdentity     *accessservice.ControlIdentityService
	appConfig           settings.AppConfigService
	decisions           settings.DecisionConfigService
	logger              httpcontract.LogProvider
	verifierModel       string
	embeddingModel      string
}
