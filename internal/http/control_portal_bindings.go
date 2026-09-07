package http

import (
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service"
)

// ControlPortalBindings keeps control-listener dependencies in one named
// capability facet while preserving the existing constructor contract.
type ControlPortalBindings struct {
	Telemetry ControlPortalTelemetry
}

func NewControlPortalServerWithCapabilityBindings(
	cfg config.ConfigProvider,
	teamSvc handler.TeamServiceInterface,
	credentialSvc handler.CredentialServiceInterface,
	metricsSvc service.UsageMetricsReader,
	bindings ControlPortalBindings,
	health HealthConfig,
	logger observability.LogProvider,
	securitySvcs ...service.SecurityService,
) (*echo.Echo, error) {
	return NewControlPortalServerWithMetricsAndTelemetry(
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
