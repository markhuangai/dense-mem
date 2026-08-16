package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/storage/inmem"
	"github.com/markhuangai/dense-mem/internal/storage/redis"
)

type stubRateLimitService struct {
	allowed       bool
	remaining     int
	resetAt       time.Time
	err           error
	lastSubject   string
	lastRoutePath string
	lastLimit     int
}

func (s *stubRateLimitService) Check(ctx context.Context, subject, routePath string, limit int) (bool, int, time.Time, error) {
	s.lastSubject = subject
	s.lastRoutePath = routePath
	s.lastLimit = limit
	resetAt := s.resetAt
	if resetAt.IsZero() {
		resetAt = time.Now().Add(time.Minute)
	}
	return s.allowed, s.remaining, resetAt, s.err
}

// testRateLimitConfig implements config.ConfigProvider for rate limit tests.
type testRateLimitConfig struct {
	rateLimitPerMinute int
}

func (c *testRateLimitConfig) GetPostgresDSN() string              { return "" }
func (c *testRateLimitConfig) GetRedisAddr() string                { return "" }
func (c *testRateLimitConfig) GetRedisPassword() string            { return "" }
func (c *testRateLimitConfig) GetRedisDB() int                     { return 0 }
func (c *testRateLimitConfig) GetHTTPMaxBodyBytes() int            { return 1048576 }
func (c *testRateLimitConfig) GetRateLimitPerMinute() int          { return c.rateLimitPerMinute }
func (c *testRateLimitConfig) GetSSEHeartbeatSeconds() int         { return 30 }
func (c *testRateLimitConfig) GetSSEMaxDurationSeconds() int       { return 300 }
func (c *testRateLimitConfig) GetEmbeddingDimensions() int         { return 1536 }
func (c *testRateLimitConfig) GetAIAPIURL() string                 { return "" }
func (c *testRateLimitConfig) GetAIAPIKey() string                 { return "" }
func (c *testRateLimitConfig) GetAIEmbeddingModel() string         { return "" }
func (c *testRateLimitConfig) GetAIEmbeddingDimensions() int       { return 0 }
func (c *testRateLimitConfig) GetAIEmbeddingTimeoutSeconds() int   { return 30 }
func (c *testRateLimitConfig) IsEmbeddingConfigured() bool         { return false }
func (c *testRateLimitConfig) GetAIVerifierAPIURL() string         { return "" }
func (c *testRateLimitConfig) GetAIVerifierAPIKey() string         { return "" }
func (c *testRateLimitConfig) GetAIVerifierModel() string          { return "gpt-4o-mini" }
func (c *testRateLimitConfig) GetAIVerifierTimeoutSeconds() int    { return 60 }
func (c *testRateLimitConfig) GetAIVerifierMaxConcurrency() int    { return 5 }
func (c *testRateLimitConfig) GetPromoteTxTimeoutSeconds() int     { return 10 }
func (c *testRateLimitConfig) GetMemoryPackImportHistoryDays() int { return 30 }
func (c *testRateLimitConfig) GetControlHTTPAddr() string          { return "127.0.0.1:8090" }
func (c *testRateLimitConfig) GetControlPortalToken() string       { return "" }

// runRateLimitMiddlewareContract is the shared contract helper for rate limit
// middleware. It exercises header contract and 429 behavior for any backend
// that implements service.RateLimitServiceInterface (AC-09).
func runRateLimitMiddlewareContract(t *testing.T, name string, svc service.RateLimitServiceInterface) {
	t.Helper()

	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	cfg := &testRateLimitConfig{rateLimitPerMinute: 2}
	e.Use(RateLimitMiddleware(svc, cfg, nil))

	principalProfileID := uuid.New()
	principal := &Principal{CredentialID: testUUIDPtr(uuid.New()), OwnerID: principalProfileID, Role: "standard"}

	e.GET("/ui/api/team/profiles/:id", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ui/api/team/profiles/"+principalProfileID.String(), nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), principal))
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.NotEmpty(t, rec.Header().Get("X-RateLimit-Limit"), "%s: request %d should have X-RateLimit-Limit header", name, i)
		assert.NotEmpty(t, rec.Header().Get("X-RateLimit-Remaining"), "%s: request %d should have X-RateLimit-Remaining header", name, i)
		assert.NotEmpty(t, rec.Header().Get("X-RateLimit-Reset"), "%s: request %d should have X-RateLimit-Reset header", name, i)
		if i == 2 {
			assert.Equal(t, http.StatusTooManyRequests, rec.Code, "%s: request %d should return 429", name, i)
			assert.NotEmpty(t, rec.Header().Get("Retry-After"), "%s: request %d should have Retry-After header", name, i)
		} else {
			assert.Equal(t, http.StatusOK, rec.Code, "%s: request %d should return 200", name, i)
		}
	}
}

func TestRateLimitMiddleware_Contract_InMemory(t *testing.T) {
	t.Parallel()

	store := inmem.NewInMemoryRateLimitStore()
	svc := service.NewRateLimitService(store)

	runRateLimitMiddlewareContract(t, "InMemory", svc)
}

// redisRateLimitConfig implements config.ConfigProvider for Redis-backed rate limit tests.
type redisRateLimitConfig struct {
	addr               string
	password           string
	db                 int
	rateLimitPerMinute int
}

func (c *redisRateLimitConfig) GetPostgresDSN() string              { return "" }
func (c *redisRateLimitConfig) GetRedisAddr() string                { return c.addr }
func (c *redisRateLimitConfig) GetRedisPassword() string            { return c.password }
func (c *redisRateLimitConfig) GetRedisDB() int                     { return c.db }
func (c *redisRateLimitConfig) GetHTTPMaxBodyBytes() int            { return 1048576 }
func (c *redisRateLimitConfig) GetRateLimitPerMinute() int          { return c.rateLimitPerMinute }
func (c *redisRateLimitConfig) GetSSEHeartbeatSeconds() int         { return 30 }
func (c *redisRateLimitConfig) GetSSEMaxDurationSeconds() int       { return 300 }
func (c *redisRateLimitConfig) GetEmbeddingDimensions() int         { return 1536 }
func (c *redisRateLimitConfig) GetAIAPIURL() string                 { return "" }
func (c *redisRateLimitConfig) GetAIAPIKey() string                 { return "" }
func (c *redisRateLimitConfig) GetAIEmbeddingModel() string         { return "" }
func (c *redisRateLimitConfig) GetAIEmbeddingDimensions() int       { return 0 }
func (c *redisRateLimitConfig) GetAIEmbeddingTimeoutSeconds() int   { return 30 }
func (c *redisRateLimitConfig) IsEmbeddingConfigured() bool         { return false }
func (c *redisRateLimitConfig) GetAIVerifierAPIURL() string         { return "" }
func (c *redisRateLimitConfig) GetAIVerifierAPIKey() string         { return "" }
func (c *redisRateLimitConfig) GetAIVerifierModel() string          { return "gpt-4o-mini" }
func (c *redisRateLimitConfig) GetAIVerifierTimeoutSeconds() int    { return 60 }
func (c *redisRateLimitConfig) GetAIVerifierMaxConcurrency() int    { return 5 }
func (c *redisRateLimitConfig) GetPromoteTxTimeoutSeconds() int     { return 10 }
func (c *redisRateLimitConfig) GetMemoryPackImportHistoryDays() int { return 30 }
func (c *redisRateLimitConfig) GetControlHTTPAddr() string          { return "127.0.0.1:8090" }
func (c *redisRateLimitConfig) GetControlPortalToken() string       { return "" }

func TestRateLimitMiddleware_Contract_Redis(t *testing.T) {
	t.Parallel()

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		t.Skip("REDIS_ADDR not set — skipping Redis-backed rate limit test")
	}

	cfg := &redisRateLimitConfig{addr: redisAddr, rateLimitPerMinute: 2}

	redisClient, err := redis.NewClient(t.Context(), cfg)
	if err != nil {
		t.Skipf("Redis not available at %s: %v", redisAddr, err)
	}
	defer redisClient.Close()

	svc := service.NewRateLimitService(redisClient)

	runRateLimitMiddlewareContract(t, "Redis", svc)
}

func TestSelectRateLimitUsesStandardTier(t *testing.T) {
	t.Parallel()

	cfg := &testRateLimitConfig{rateLimitPerMinute: 100}
	assert.Equal(t, 100, selectRateLimit(cfg))
}

func TestRateLimitMiddleware_EdgeBranches(t *testing.T) {
	cfg := &testRateLimitConfig{rateLimitPerMinute: 100}

	t.Run("no principal passes through", func(t *testing.T) {
		e := echo.New()
		limiter := &stubRateLimitService{allowed: true}
		e.Use(RateLimitMiddleware(limiter, cfg, nil))
		e.GET("/ui/api/other", func(c echo.Context) error {
			return c.NoContent(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodGet, "/ui/api/other", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Empty(t, limiter.lastSubject)
	})

	t.Run("principal without profile is forbidden", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		e.Use(RateLimitMiddleware(&stubRateLimitService{allowed: true}, cfg, nil))
		e.GET("/ui/api/other", func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/ui/api/other", nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), &Principal{CredentialID: testUUIDPtr(uuid.New())}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("rate limit service error fails closed", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		limiter := &stubRateLimitService{err: errors.New("store down")}
		e.Use(RateLimitMiddleware(limiter, cfg, nil))
		e.GET("/ui/api/other", func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})

		profileID := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/ui/api/other", nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), &Principal{
			CredentialID: testUUIDPtr(uuid.New()),
			OwnerID:      profileID,
		}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, 100, limiter.lastLimit)
	})

	t.Run("principal limit overrides larger route tier", func(t *testing.T) {
		e := echo.New()
		limiter := &stubRateLimitService{allowed: true, remaining: 4}
		e.Use(RateLimitMiddleware(limiter, cfg, nil))
		e.GET("/ui/api/other", func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})

		profileID := uuid.New()
		keyID := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/ui/api/other", nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), &Principal{
			CredentialID: testUUIDPtr(keyID),
			OwnerID:      profileID,
			RateLimit:    5,
		}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, 5, limiter.lastLimit)
		assert.Contains(t, limiter.lastSubject, profileID.String()+":key:"+keyID.String())
		assert.Equal(t, "5", rec.Header().Get("X-RateLimit-Limit"))
	})

	t.Run("denied request sets retry after and audits", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		limiter := &stubRateLimitService{
			allowed:   false,
			remaining: 0,
			resetAt:   time.Now().Add(-time.Second),
		}
		e.Use(CorrelationIDMiddleware())
		e.Use(RateLimitMiddleware(limiter, cfg, &mockAuditService{}))
		e.DELETE("/ui/api/other", func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})

		profileID := uuid.New()
		req := httptest.NewRequest(http.MethodDelete, "/ui/api/other", nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), &Principal{
			CredentialID: testUUIDPtr(uuid.New()),
			OwnerID:      profileID,
		}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusTooManyRequests, rec.Code)
		assert.Equal(t, "0", rec.Header().Get("Retry-After"))
		assert.Equal(t, 100, limiter.lastLimit)
	})
}
