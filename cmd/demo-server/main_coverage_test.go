package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestValidateDemoStartupConfigCoversRequiredFields(t *testing.T) {
	valid := validDemoStartupConfig()
	for name, mutate := range map[string]func(*config.Config){
		"embedding URL":        func(c *config.Config) { c.AIAPIURL = "" },
		"embedding key":        func(c *config.Config) { c.AIAPIKey = "" },
		"embedding model":      func(c *config.Config) { c.AIEmbeddingModel = "" },
		"verifier model":       func(c *config.Config) { c.AIVerifierModel = "" },
		"redis":                func(c *config.Config) { c.RedisAddr = "" },
		"embedding dimensions": func(c *config.Config) { c.AIEmbeddingDimensions = 0 },
		"verifier URL": func(c *config.Config) {
			c.AIAPIURL = ""
			c.AIVerifierAPIURL = ""
		},
		"verifier key": func(c *config.Config) {
			c.AIAPIKey = ""
			c.AIVerifierAPIKey = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			mutate(&cfg)
			if err := validateDemoStartupConfig(&cfg); err == nil {
				t.Fatal("invalid demo configuration was accepted")
			}
		})
	}
	if err := validateDemoStartupConfig(&valid); err != nil {
		t.Fatalf("valid demo configuration rejected: %v", err)
	}
}

func TestDemoRuntimeOptionsAndDeferredQuotaMiddleware(t *testing.T) {
	options := demoRuntimeOptions()
	if options.ValidateStartup == nil || options.ConfigureRegistry == nil || options.RegisterRoutes == nil || options.BuildWorker == nil || len(options.PostAuthMiddleware) != 1 || len(options.UserPortalMiddleware) != 1 {
		t.Fatalf("demo runtime options = %+v", options)
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	err := options.PostAuthMiddleware[0](func(echo.Context) error { return nil })(e.NewContext(req, rec))
	if err == nil {
		t.Fatal("deferred quota middleware accepted an uninitialized manager")
	}
	wrapped, err := options.ConfigureRegistry(context.Background(), serverapp.RuntimeContext{}, registry.New())
	require.NoError(t, err)
	require.NotNil(t, wrapped)
}
