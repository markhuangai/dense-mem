package http

import (
	"github.com/labstack/echo/v4"

	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	settings "github.com/markhuangai/dense-mem/internal/settings"
)

// ControlPortalBindings keeps control-listener dependencies in one named
// capability facet while preserving the existing constructor contract.
type ControlPortalBindings struct {
	Telemetry ControlPortalTelemetry
}

func NewControlPortalServerWithCapabilityBindings(
	cfg httpcontract.ConfigProvider,
	teamSvc handler.TeamServiceInterface,
	credentialSvc handler.CredentialServiceInterface,
	metricsSvc operations.UsageMetricsReader,
	bindings ControlPortalBindings,
	health HealthConfig,
	logger httpcontract.LogProvider,
	securitySvcs ...settings.SecurityService,
) (*echo.Echo, error) {
	return newControlPortalServerWithMetricsAndTelemetry(
		cfg,
		teamSvc,
		credentialSvc,
		metricsSvc,
		bindings.Telemetry,
		health,
		logger,
		securitySvcs...,
	)
}
