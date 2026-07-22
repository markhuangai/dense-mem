package http

import "github.com/labstack/echo/v4"

func registerModeScopedControlPortal(
	e *echo.Echo,
	api *echo.Group,
	portalMode string,
	control *controlPortalHandler,
) bool {
	switch portalMode {
	case "migration":
		registerV2MigrationControlRoutes(api, control)
		registerControlPortalStatic(e)
		return true
	case "cleanup":
		registerV2MigrationStatusRoute(api, control)
		registerControlPortalStatic(e)
		return true
	default:
		return false
	}
}

func registerControlPortalStatic(e *echo.Echo) {
	if staticDir := defaultPortalStaticDir(); staticDir != "" {
		e.Static("/", staticDir)
	}
}
