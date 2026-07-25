package serverapp

import (
	"context"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/inmem"
	"github.com/markhuangai/dense-mem/internal/storage/redis"
)

// backendBundle holds the wired backend components for either Redis or in-memory mode.
type backendBundle struct {
	cleanupRepo        redis.CleanupRepositoryInterface
	rateLimitService   service.RateLimitServiceInterface
	counterStore       CounterStore
	concurrencyLimiter sse.ConcurrencyLimiter
	streamCleanupRepo  sse.StreamCleanupRepository
	degraded           bool
	reason             string
	closeFn            func() error
	redisPingFn        func(ctx context.Context) error // nil in in-memory mode
}

// buildBackendBundle wires either Redis-backed or in-memory backends depending on
// whether cfg.RedisAddr is set. The closeFn returned should be called on shutdown
// to release resources (e.g. close the Redis client).
func buildBackendBundle(ctx context.Context, cfg config.Config) (*backendBundle, error) {
	if cfg.RedisAddr != "" {
		return buildRedisBackend(ctx, cfg)
	}
	if cfg.GetDistributedCoordinationRequired() {
		return nil, fmt.Errorf("distributed coordination requires REDIS_ADDR")
	}
	return buildInMemoryBackend(cfg)
}

func buildRedisBackend(ctx context.Context, cfg config.Config) (*backendBundle, error) {
	redisClient, err := redis.NewClient(ctx, &cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	redisCleanup := redis.NewCleanupRepository(redisClient.GetClient())

	rateLimitService := service.NewRateLimitService(redisClient)

	concurrencyLimiter := sse.NewConcurrencyLimiterWithConfig(redisClient, cfg.SSEMaxConcurrentStreams, 3600)
	streamCleanupRepo := sse.NewStreamCleanupRepository(redisClient)

	return &backendBundle{
		cleanupRepo:        redisCleanup,
		rateLimitService:   rateLimitService,
		counterStore:       redisClient,
		concurrencyLimiter: concurrencyLimiter,
		streamCleanupRepo:  streamCleanupRepo,
		degraded:           false,
		reason:             "",
		closeFn:            redisClient.Close,
		redisPingFn:        redisClient.Ping,
	}, nil
}

func buildInMemoryBackend(cfg config.Config) (*backendBundle, error) {
	inmemStore := inmem.NewInMemoryRateLimitStore()
	rateLimitService := service.NewRateLimitService(inmemStore)

	concurrencyLimiter := inmem.NewInMemoryConcurrencyLimiter(cfg.SSEMaxConcurrentStreams, time.Hour)
	streamCleanupRepo := inmem.NewNoopStreamCleanupRepository()
	cleanupRepo := inmem.NewNoopCleanupRepository()

	return &backendBundle{
		cleanupRepo:        cleanupRepo,
		rateLimitService:   rateLimitService,
		concurrencyLimiter: concurrencyLimiter,
		streamCleanupRepo:  streamCleanupRepo,
		degraded:           true,
		reason:             "in-memory backend: no cross-instance rate limiting or session cleanup",
		closeFn:            func() error { return nil },
		redisPingFn:        nil,
	}, nil
}

// logInMemoryModeWarning emits a WARN-level log when the server is running in
// degraded (in-memory) mode, indicating that multi-instance features are disabled.
func logInMemoryModeWarning(logger observability.LogProvider, degraded bool, reason string) {
	if !degraded {
		return
	}
	logger.Warn("running in-memory mode: multi-instance features disabled",
		observability.String("mode", "in-memory"),
		observability.String("reason", reason),
	)
}
