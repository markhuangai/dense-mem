package serverapp

// This compatibility facade keeps feature wiring in serverapp while the
// telemetry policy helper lives in internal/operations.

import (
	operations "github.com/markhuangai/dense-mem/internal/operations"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	settings "github.com/markhuangai/dense-mem/internal/settings"
)

func configureTelemetryFeatures(prometheus *operations.PrometheusTelemetryService, appConfig settings.AppConfigService, dreams dreamservice.Service) {
	operations.ConfigureTelemetryFeatures(prometheus, appConfig, dreams)
}
