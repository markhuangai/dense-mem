package http

import "github.com/labstack/echo/v4"

func registerControlDreamDiagnosticRoutes(api *echo.Group, control *controlPortalHandler, dreams, diagnostics bool) {
	if dreams && diagnostics {
		api.GET("/teams/:teamId/dreams/:dreamId/diagnostics", control.listTeamDreamDiagnosticsForHypothesis)
	}
	if diagnostics {
		api.GET("/teams/:teamId/dreaming/runs/:runId/diagnostics", control.listTeamDreamDiagnostics)
		api.GET("/teams/:teamId/dreaming/runs/:runId/diagnostics/:diagnosticId", control.getTeamDreamDiagnostic)
	}
}
