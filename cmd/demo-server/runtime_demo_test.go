package main

import (
	"context"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/demo"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
)

type demoRuntimeCounterStore struct{}

func (demoRuntimeCounterStore) IncrWithExpire(context.Context, string, int64) (int64, error) {
	return 1, nil
}

func (demoRuntimeCounterStore) AddWithExpire(context.Context, string, int64, int64) (int64, error) {
	return 1, nil
}

func TestDemoRuntimeModeKeepsAdminSurfaceDisabled(t *testing.T) {
	mode := newServerRuntimeMode()

	require.True(t, mode.DisableControlPortal)
	require.True(t, mode.RequireRedis)
	require.NotNil(t, mode.ValidateConfig)
	require.NotNil(t, mode.ConfigureServices)
	require.NotNil(t, mode.RegisterRoutes)
	require.NotNil(t, mode.StartBackground)

	var cfg config.Config
	err := mode.ValidateConfig(&cfg)
	require.ErrorContains(t, err, "required for demo server startup")
}

func TestConfigureDemoServicesAddsQuotasAndDisablesCommunityDetection(t *testing.T) {
	mode := newServerRuntimeMode()
	services := serverRuntimeServices{
		PostAuthMiddleware:   []echo.MiddlewareFunc{},
		UserPortalMiddleware: []echo.MiddlewareFunc{},
	}
	ctx := serverRuntimeContext{CounterStore: demoRuntimeCounterStore{}}

	err := mode.ConfigureServices(context.Background(), ctx, &services)
	require.NoError(t, err)

	require.Len(t, services.PostAuthMiddleware, 1)
	require.Len(t, services.UserPortalMiddleware, 1)
	require.IsType(t, demo.DisabledCommunityDetectService{}, services.CommunityDetectRegistrySvc)

	err = services.CommunityDetectRegistrySvc.Detect(context.Background(), "team-1", communityservice.DetectOptions{})
	require.ErrorIs(t, err, communityservice.ErrCommunityUnavailable)
}
