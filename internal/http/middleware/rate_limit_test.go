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
	rateLimitPerMinute      int
	fragmentCreateRateLimit int
	fragmentReadRateLimit   int
	claimWriteRateLimit     int
	claimReadRateLimit      int
}

func (c *testRateLimitConfig) GetPostgresDSN() string            { return "" }
func (c *testRateLimitConfig) GetNeo4jURI() string               { return "" }
func (c *testRateLimitConfig) GetNeo4jUser() string              { return "" }
func (c *testRateLimitConfig) GetNeo4jPassword() string          { return "" }
func (c *testRateLimitConfig) GetNeo4jDatabase() string          { return "" }
func (c *testRateLimitConfig) GetRedisAddr() string              { return "" }
func (c *testRateLimitConfig) GetRedisPassword() string          { return "" }
func (c *testRateLimitConfig) GetRedisDB() int                   { return 0 }
func (c *testRateLimitConfig) GetHTTPMaxBodyBytes() int          { return 1048576 }
func (c *testRateLimitConfig) GetRateLimitPerMinute() int        { return c.rateLimitPerMinute }
func (c *testRateLimitConfig) GetFragmentCreateRateLimit() int   { return c.fragmentCreateRateLimit }
func (c *testRateLimitConfig) GetFragmentReadRateLimit() int     { return c.fragmentReadRateLimit }
func (c *testRateLimitConfig) GetSSEHeartbeatSeconds() int       { return 30 }
func (c *testRateLimitConfig) GetSSEMaxDurationSeconds() int     { return 300 }
func (c *testRateLimitConfig) GetSSEMaxConcurrentStreams() int   { return 10 }
func (c *testRateLimitConfig) GetEmbeddingDimensions() int       { return 1536 }
func (c *testRateLimitConfig) GetAIAPIURL() string               { return "" }
func (c *testRateLimitConfig) GetAIAPIKey() string               { return "" }
func (c *testRateLimitConfig) GetAIEmbeddingModel() string       { return "" }
func (c *testRateLimitConfig) GetAIEmbeddingDimensions() int     { return 0 }
func (c *testRateLimitConfig) GetAIEmbeddingTimeoutSeconds() int { return 30 }
func (c *testRateLimitConfig) IsEmbeddingConfigured() bool       { return false }
func (c *testRateLimitConfig) GetAIVerifierAPIURL() string       { return "" }
func (c *testRateLimitConfig) GetAIVerifierAPIKey() string       { return "" }
func (c *testRateLimitConfig) GetAIVerifierModel() string        { return "gpt-4o-mini" }
func (c *testRateLimitConfig) GetAIVerifierTimeoutSeconds() int  { return 60 }
func (c *testRateLimitConfig) GetAIVerifierMaxConcurrency() int  { return 5 }
func (c *testRateLimitConfig) GetClaimWriteRateLimit() int {
	if c.claimWriteRateLimit != 0 {
		return c.claimWriteRateLimit
	}
	return 60
}
func (c *testRateLimitConfig) GetClaimReadRateLimit() int {
	if c.claimReadRateLimit != 0 {
		return c.claimReadRateLimit
	}
	return 300
}
func (c *testRateLimitConfig) GetRecallValidatedClaimWeight() float64 { return 0.5 }
func (c *testRateLimitConfig) GetPromoteTxTimeoutSeconds() int        { return 10 }
func (c *testRateLimitConfig) GetAICommunityMaxNodes() int            { return 500000 }
func (c *testRateLimitConfig) GetControlHTTPAddr() string             { return "127.0.0.1:8090" }
func (c *testRateLimitConfig) GetControlPortalToken() string          { return "" }

// runRateLimitMiddlewareContract is the shared contract helper for rate limit
// middleware. It exercises header contract and 429 behavior for any backend
// that implements service.RateLimitServiceInterface (AC-09).
func runRateLimitMiddlewareContract(t *testing.T, name string, svc service.RateLimitServiceInterface) {
	t.Helper()

	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	cfg := &testRateLimitConfig{
		rateLimitPerMinute:      2,
		fragmentCreateRateLimit: 60,
		fragmentReadRateLimit:   300,
	}
	e.Use(RateLimitMiddleware(svc, cfg, nil))

	principalProfileID := uuid.New()
	principal := &Principal{KeyID: uuid.New(), ProfileID: &principalProfileID, Role: "standard"}

	e.GET("/api/v1/profiles/:id", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+principalProfileID.String(), nil)
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
	addr                    string
	password                string
	db                      int
	rateLimitPerMinute      int
	fragmentCreateRateLimit int
	fragmentReadRateLimit   int
}

func (c *redisRateLimitConfig) GetPostgresDSN() string                 { return "" }
func (c *redisRateLimitConfig) GetNeo4jURI() string                    { return "" }
func (c *redisRateLimitConfig) GetNeo4jUser() string                   { return "" }
func (c *redisRateLimitConfig) GetNeo4jPassword() string               { return "" }
func (c *redisRateLimitConfig) GetNeo4jDatabase() string               { return "" }
func (c *redisRateLimitConfig) GetRedisAddr() string                   { return c.addr }
func (c *redisRateLimitConfig) GetRedisPassword() string               { return c.password }
func (c *redisRateLimitConfig) GetRedisDB() int                        { return c.db }
func (c *redisRateLimitConfig) GetHTTPMaxBodyBytes() int               { return 1048576 }
func (c *redisRateLimitConfig) GetRateLimitPerMinute() int             { return c.rateLimitPerMinute }
func (c *redisRateLimitConfig) GetFragmentCreateRateLimit() int        { return c.fragmentCreateRateLimit }
func (c *redisRateLimitConfig) GetFragmentReadRateLimit() int          { return c.fragmentReadRateLimit }
func (c *redisRateLimitConfig) GetSSEHeartbeatSeconds() int            { return 30 }
func (c *redisRateLimitConfig) GetSSEMaxDurationSeconds() int          { return 300 }
func (c *redisRateLimitConfig) GetSSEMaxConcurrentStreams() int        { return 10 }
func (c *redisRateLimitConfig) GetEmbeddingDimensions() int            { return 1536 }
func (c *redisRateLimitConfig) GetAIAPIURL() string                    { return "" }
func (c *redisRateLimitConfig) GetAIAPIKey() string                    { return "" }
func (c *redisRateLimitConfig) GetAIEmbeddingModel() string            { return "" }
func (c *redisRateLimitConfig) GetAIEmbeddingDimensions() int          { return 0 }
func (c *redisRateLimitConfig) GetAIEmbeddingTimeoutSeconds() int      { return 30 }
func (c *redisRateLimitConfig) IsEmbeddingConfigured() bool            { return false }
func (c *redisRateLimitConfig) GetAIVerifierAPIURL() string            { return "" }
func (c *redisRateLimitConfig) GetAIVerifierAPIKey() string            { return "" }
func (c *redisRateLimitConfig) GetAIVerifierModel() string             { return "gpt-4o-mini" }
func (c *redisRateLimitConfig) GetAIVerifierTimeoutSeconds() int       { return 60 }
func (c *redisRateLimitConfig) GetAIVerifierMaxConcurrency() int       { return 5 }
func (c *redisRateLimitConfig) GetClaimWriteRateLimit() int            { return 60 }
func (c *redisRateLimitConfig) GetClaimReadRateLimit() int             { return 300 }
func (c *redisRateLimitConfig) GetRecallValidatedClaimWeight() float64 { return 0.5 }
func (c *redisRateLimitConfig) GetPromoteTxTimeoutSeconds() int        { return 10 }
func (c *redisRateLimitConfig) GetAICommunityMaxNodes() int            { return 500000 }
func (c *redisRateLimitConfig) GetControlHTTPAddr() string             { return "127.0.0.1:8090" }
func (c *redisRateLimitConfig) GetControlPortalToken() string          { return "" }

func TestRateLimitMiddleware_Contract_Redis(t *testing.T) {
	t.Parallel()

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		t.Skip("REDIS_ADDR not set — skipping Redis-backed rate limit test")
	}

	cfg := &redisRateLimitConfig{
		addr:                    redisAddr,
		rateLimitPerMinute:      2,
		fragmentCreateRateLimit: 60,
		fragmentReadRateLimit:   300,
	}

	redisClient, err := redis.NewClient(t.Context(), cfg)
	if err != nil {
		t.Skipf("Redis not available at %s: %v", redisAddr, err)
	}
	defer redisClient.Close()

	svc := service.NewRateLimitService(redisClient)

	runRateLimitMiddlewareContract(t, "Redis", svc)
}

// TestSelectRateLimit_FragmentTiers asserts AC-54: fragment writes use the
// stricter FragmentCreateRateLimit while fragment reads use the looser
// FragmentReadRateLimit. Non-fragment traffic falls back to the standard tier
// for the profile.
func TestSelectRateLimit_FragmentTiers(t *testing.T) {
	t.Parallel()

	cfg := &testRateLimitConfig{
		rateLimitPerMinute:      100,
		fragmentCreateRateLimit: 60,
		fragmentReadRateLimit:   300,
	}

	cases := []struct {
		name   string
		role   string
		method string
		path   string
		want   int
	}{
		{"fragment create POST uses write tier", "standard", "POST", "/api/v1/profiles/:id/fragments", 60},
		{"fragment delete DELETE uses write tier", "standard", "DELETE", "/api/v1/profiles/:id/fragments/:fragmentId", 60},
		{"fragment list GET uses read tier", "standard", "GET", "/api/v1/profiles/:id/fragments", 300},
		{"fragment read GET uses read tier", "standard", "GET", "/api/v1/profiles/:id/fragments/:fragmentId", 300},
		{"non-fragment standard uses default tier", "standard", "GET", "/api/v1/profiles/:id", 100},
		{"fragment write stricter than read", "standard", "POST", "/api/v1/profiles/:id/fragments", 60},
	}

	for _, tc := range cases {
		got := selectRateLimit(cfg, tc.role, tc.method, tc.path)
		if got != tc.want {
			t.Errorf("%s: selectRateLimit(%q, %q, %q) = %d; want %d", tc.name, tc.role, tc.method, tc.path, got, tc.want)
		}
	}

	if cfg.GetFragmentCreateRateLimit() >= cfg.GetFragmentReadRateLimit() {
		t.Errorf("fragment create (%d) should be stricter than fragment read (%d)",
			cfg.GetFragmentCreateRateLimit(), cfg.GetFragmentReadRateLimit())
	}
}

// TestSelectRateLimit_ClaimTiers asserts that POST and DELETE on /claims routes
// use GetClaimWriteRateLimit and GETs use GetClaimReadRateLimit.
func TestSelectRateLimit_ClaimTiers(t *testing.T) {
	t.Parallel()

	cfg := &testRateLimitConfig{
		rateLimitPerMinute:      100,
		fragmentCreateRateLimit: 60,
		fragmentReadRateLimit:   300,
		claimWriteRateLimit:     40,
		claimReadRateLimit:      200,
	}

	cases := []struct {
		name   string
		role   string
		method string
		path   string
		want   int
	}{
		{"claim create POST uses write tier", "standard", "POST", "/api/v1/claims", 40},
		{"claim delete DELETE uses write tier", "standard", "DELETE", "/api/v1/claims/:id", 40},
		{"claim list GET uses read tier", "standard", "GET", "/api/v1/claims", 200},
		{"claim read GET uses read tier", "standard", "GET", "/api/v1/claims/:id", 200},
	}

	for _, tc := range cases {
		got := selectRateLimit(cfg, tc.role, tc.method, tc.path)
		if got != tc.want {
			t.Errorf("%s: selectRateLimit(%q, %q, %q) = %d; want %d",
				tc.name, tc.role, tc.method, tc.path, got, tc.want)
		}
	}

	if cfg.GetClaimWriteRateLimit() >= cfg.GetClaimReadRateLimit() {
		t.Errorf("claim write (%d) should be stricter than claim read (%d)",
			cfg.GetClaimWriteRateLimit(), cfg.GetClaimReadRateLimit())
	}
}

// TestRateLimitMiddleware_EnforcesStricterFragmentWriteTier proves that when a
// principal floods POST /fragments, the middleware returns 429 at exactly the
// write-tier ceiling, even though the same principal could still issue read
// traffic under the higher read-tier quota. This is AC-54's enforcement check,
// not just a config-ordering assertion.
func TestRateLimitMiddleware_EnforcesStricterFragmentWriteTier(t *testing.T) {
	t.Parallel()

	store := inmem.NewInMemoryRateLimitStore()
	svc := service.NewRateLimitService(store)

	// Tight numbers for a fast, deterministic test: writes get 2/min, reads 5/min.
	cfg := &testRateLimitConfig{
		rateLimitPerMinute:      50,
		fragmentCreateRateLimit: 2,
		fragmentReadRateLimit:   5,
	}

	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(RateLimitMiddleware(svc, cfg, nil))

	profileID := uuid.New()
	principal := &Principal{KeyID: uuid.New(), ProfileID: &profileID, Role: "standard"}

	e.POST("/api/v1/profiles/:id/fragments", func(c echo.Context) error {
		return c.NoContent(http.StatusCreated)
	})
	e.GET("/api/v1/profiles/:id/fragments", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	post := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/"+profileID.String()+"/fragments", nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), principal))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	get := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/fragments", nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), principal))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	// First two POSTs must succeed — write tier allows 2.
	assert.Equal(t, http.StatusCreated, post(), "first write must succeed under write-tier")
	assert.Equal(t, http.StatusCreated, post(), "second write must succeed under write-tier")

	// Third POST must exceed the write tier and return 429 even though the
	// default/read tiers still have headroom. This is the actual enforcement check.
	assert.Equal(t, http.StatusTooManyRequests, post(),
		"third write must be rate-limited — stricter write tier enforced, not config-only")

	// Reads share the same principal but use a looser tier and must still pass.
	// This proves the middleware picks the per-route tier, not a shared global bucket.
	assert.Equal(t, http.StatusOK, get(), "read must still succeed — read tier is separate from write tier")
	assert.Equal(t, http.StatusOK, get(), "second read must still succeed")
}

func TestRateLimitMiddleware_EdgeBranches(t *testing.T) {
	cfg := &testRateLimitConfig{
		rateLimitPerMinute:      100,
		fragmentCreateRateLimit: 50,
		fragmentReadRateLimit:   200,
		claimWriteRateLimit:     40,
		claimReadRateLimit:      150,
	}

	t.Run("no principal passes through", func(t *testing.T) {
		e := echo.New()
		limiter := &stubRateLimitService{allowed: true}
		e.Use(RateLimitMiddleware(limiter, cfg, nil))
		e.GET("/api/v1/other", func(c echo.Context) error {
			return c.NoContent(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/other", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Empty(t, limiter.lastSubject)
	})

	t.Run("principal without profile is forbidden", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		e.Use(RateLimitMiddleware(&stubRateLimitService{allowed: true}, cfg, nil))
		e.GET("/api/v1/other", func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/other", nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), &Principal{KeyID: uuid.New()}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("rate limit service error fails open", func(t *testing.T) {
		e := echo.New()
		limiter := &stubRateLimitService{err: errors.New("store down")}
		e.Use(RateLimitMiddleware(limiter, cfg, nil))
		e.GET("/api/v1/other", func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})

		profileID := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/other", nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), &Principal{
			KeyID:     uuid.New(),
			ProfileID: &profileID,
		}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, 100, limiter.lastLimit)
	})

	t.Run("principal limit overrides larger route tier", func(t *testing.T) {
		e := echo.New()
		limiter := &stubRateLimitService{allowed: true, remaining: 4}
		e.Use(RateLimitMiddleware(limiter, cfg, nil))
		e.GET("/api/v1/profiles/:id/fragments", func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})

		profileID := uuid.New()
		keyID := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/fragments", nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), &Principal{
			KeyID:     keyID,
			ProfileID: &profileID,
			RateLimit: 5,
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
		e.DELETE("/api/v1/profiles/:id/claims/:claim_id", func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})

		profileID := uuid.New()
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/profiles/"+profileID.String()+"/claims/claim-1", nil)
		req = req.WithContext(SetPrincipalForTest(req.Context(), &Principal{
			KeyID:     uuid.New(),
			ProfileID: &profileID,
		}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusTooManyRequests, rec.Code)
		assert.Equal(t, "0", rec.Header().Get("Retry-After"))
		assert.Equal(t, 40, limiter.lastLimit)
	})
}
