package sse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Constants for lifecycle configuration
const (
	// HeartbeatInterval is the interval between heartbeat keepalive comments
	HeartbeatInterval = 30 * time.Second

	// MaxStreamDuration is the maximum duration a stream can run
	MaxStreamDuration = 5 * time.Minute

	// MaxConcurrentStreams is the maximum number of concurrent streams per team.
	MaxConcurrentStreams = 10

	// StreamCounterKey is the Redis key suffix for stream concurrency counter
	StreamCounterKey = "count"
)

// Errors for lifecycle operations
var (
	ErrTooManyStreams   = errors.New("too many concurrent streams for team")
	ErrStreamTerminated = errors.New("stream terminated due to max duration")
)

// RedisClientForLifecycle is the minimal Redis interface needed for stream lifecycle.
type RedisClientForLifecycle interface {
	KeyBuilder() any
	Incr(ctx context.Context, key string) (int64, error)
	Decr(ctx context.Context, key string) (int64, error)
	Expire(ctx context.Context, key string, expiration int64) error
	Del(ctx context.Context, key string) error
	Scan(ctx context.Context, cursor uint64, match string, count int64) (keys []string, nextCursor uint64, err error)
}

// RedisKeyBuilder is the interface for building Redis keys.
type RedisKeyBuilder interface {
	Stream(teamID, identifier string) (string, error)
}

// StreamLifecycle manages the complete lifecycle of an SSE stream.
// It handles heartbeat, max duration, disconnect detection, and cleanup.
type StreamLifecycle interface {
	Start(ctx context.Context, teamID string, writer SSEWriter, work func(context.Context) error) error
}

// ConcurrencyLimiter manages concurrent stream limits per team.
type ConcurrencyLimiter interface {
	Acquire(ctx context.Context, teamID string) (release func(), err error)
}

// HeartbeatSender sends periodic SSE keepalive comments.
type HeartbeatSender interface {
	Run(ctx context.Context, writer SSEWriter)
}

// streamLifecycle implements StreamLifecycle.
type streamLifecycle struct {
	concurrencyLimiter ConcurrencyLimiter
	heartbeatSender    HeartbeatSender
	maxDuration        time.Duration
	cleanupRepo        StreamCleanupRepository
}

// Ensure streamLifecycle implements StreamLifecycle.
var _ StreamLifecycle = (*streamLifecycle)(nil)

// redisConcurrencyLimiter implements ConcurrencyLimiter using Redis.
type redisConcurrencyLimiter struct {
	redisClient RedisClientForLifecycle
	maxStreams  int
	counterTTL  int64 // TTL in seconds for the counter key
}

// Ensure redisConcurrencyLimiter implements ConcurrencyLimiter.
var _ ConcurrencyLimiter = (*redisConcurrencyLimiter)(nil)

// heartbeatSender implements HeartbeatSender.
type heartbeatSender struct {
	interval time.Duration
}

// Ensure heartbeatSender implements HeartbeatSender.
var _ HeartbeatSender = (*heartbeatSender)(nil)

// StreamCleanupRepository is the interface for cleaning up stream state.
type StreamCleanupRepository interface {
	PurgeTeamStreamState(ctx context.Context, teamID string) error
}

// redisStreamCleanupRepository implements StreamCleanupRepository.
type redisStreamCleanupRepository struct {
	client RedisClientForLifecycle
}

// Ensure redisStreamCleanupRepository implements StreamCleanupRepository.
var _ StreamCleanupRepository = (*redisStreamCleanupRepository)(nil)

// NewStreamLifecycle creates a new StreamLifecycle instance.
func NewStreamLifecycle(concurrencyLimiter ConcurrencyLimiter, cleanupRepo StreamCleanupRepository) StreamLifecycle {
	return &streamLifecycle{
		concurrencyLimiter: concurrencyLimiter,
		heartbeatSender:    NewHeartbeatSender(),
		maxDuration:        MaxStreamDuration,
		cleanupRepo:        cleanupRepo,
	}
}

// NewStreamLifecycleWithConfig creates a new StreamLifecycle with custom configuration.
func NewStreamLifecycleWithConfig(
	concurrencyLimiter ConcurrencyLimiter,
	heartbeatSender HeartbeatSender,
	maxDuration time.Duration,
	cleanupRepo StreamCleanupRepository,
) StreamLifecycle {
	return &streamLifecycle{
		concurrencyLimiter: concurrencyLimiter,
		heartbeatSender:    heartbeatSender,
		maxDuration:        maxDuration,
		cleanupRepo:        cleanupRepo,
	}
}

// Start begins the stream lifecycle.
// It acquires a concurrency slot, starts heartbeat, monitors for disconnect,
// enforces max duration, and ensures cleanup on all exit paths.
func (l *streamLifecycle) Start(
	ctx context.Context,
	teamID string,
	writer SSEWriter,
	work func(context.Context) error,
) error {
	// Acquire concurrency slot
	release, err := l.concurrencyLimiter.Acquire(ctx, teamID)
	if err != nil {
		return err
	}

	// Ensure release is called on all exit paths
	var releaseOnce sync.Once
	safeRelease := func() {
		releaseOnce.Do(release)
	}
	defer safeRelease()

	// Create cancellable context for work function
	workCtx, workCancel := context.WithCancel(ctx)
	defer workCancel()

	// Channel to signal work completion
	workDone := make(chan error, 1)

	// Start work function in goroutine
	go func() {
		defer close(workDone)
		defer func() {
			if r := recover(); r != nil {
				workDone <- fmt.Errorf("panic in work function: %v", r)
			}
		}()
		workDone <- work(workCtx)
	}()

	// Start heartbeat sender in goroutine
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())
	defer heartbeatCancel()
	go l.heartbeatSender.Run(heartbeatCtx, writer)

	// Set up max duration timer
	maxDurationTimer := time.NewTimer(l.maxDuration)
	defer maxDurationTimer.Stop()

	// Wait for: work completion, context cancellation (disconnect), or max duration
	select {
	case err := <-workDone:
		// Work completed naturally
		return err

	case <-ctx.Done():
		// Client disconnected
		workCancel() // Signal work to abort
		// Drain workDone channel
		<-workDone
		// Clean up stream-specific Redis keys
		if l.cleanupRepo != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_ = l.cleanupRepo.PurgeTeamStreamState(cleanupCtx, teamID)
		}
		return ctx.Err()

	case <-maxDurationTimer.C:
		// Max duration reached - send done event
		workCancel() // Signal work to abort
		// Drain workDone channel
		<-workDone
		// Send done event
		_ = writer.WriteEvent(EventTypeDone, map[string]any{"reason": "max_duration_exceeded"})
		return ErrStreamTerminated
	}
}

// NewConcurrencyLimiter creates a new ConcurrencyLimiter using Redis.
func NewConcurrencyLimiter(redisClient RedisClientForLifecycle) ConcurrencyLimiter {
	return &redisConcurrencyLimiter{
		redisClient: redisClient,
		maxStreams:  MaxConcurrentStreams,
		counterTTL:  3600, // 1 hour TTL for counter key
	}
}

// NewConcurrencyLimiterWithConfig creates a new ConcurrencyLimiter with custom configuration.
func NewConcurrencyLimiterWithConfig(redisClient RedisClientForLifecycle, maxStreams int, counterTTL int64) ConcurrencyLimiter {
	return &redisConcurrencyLimiter{
		redisClient: redisClient,
		maxStreams:  maxStreams,
		counterTTL:  counterTTL,
	}
}

// Acquire increments the stream counter for the team.
// Returns a release function that decrements the counter.
// Returns ErrTooManyStreams if the limit is exceeded.
func (l *redisConcurrencyLimiter) Acquire(ctx context.Context, teamID string) (func(), error) {
	kb, ok := l.redisClient.KeyBuilder().(RedisKeyBuilder)
	if !ok {
		return nil, fmt.Errorf("keybuilder does not implement Stream method")
	}
	key, err := kb.Stream(teamID, StreamCounterKey)
	if err != nil {
		return nil, fmt.Errorf("failed to build stream counter key: %w", err)
	}

	// Increment the counter
	count, err := l.redisClient.Incr(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to increment stream counter: %w", err)
	}

	// Set TTL on first increment
	if count == 1 {
		if err := l.redisClient.Expire(ctx, key, l.counterTTL); err != nil {
			// Best effort decrement on failure
			_, _ = l.redisClient.Decr(ctx, key)
			return nil, fmt.Errorf("failed to set stream counter TTL: %w", err)
		}
	}

	// Check if limit exceeded
	if count > int64(l.maxStreams) {
		// Decrement since we exceeded
		_, _ = l.redisClient.Decr(ctx, key)
		return nil, ErrTooManyStreams
	}

	// Return release function that decrements counter
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_, _ = l.redisClient.Decr(ctx, key)
	}, nil
}

// NewHeartbeatSender creates a new HeartbeatSender.
func NewHeartbeatSender() HeartbeatSender {
	return &heartbeatSender{
		interval: HeartbeatInterval,
	}
}

// NewHeartbeatSenderWithInterval creates a new HeartbeatSender with custom interval.
func NewHeartbeatSenderWithInterval(interval time.Duration) HeartbeatSender {
	return &heartbeatSender{
		interval: interval,
	}
}

// Run sends periodic heartbeat keepalive comments.
// Stops when context is cancelled.
func (h *heartbeatSender) Run(ctx context.Context, writer SSEWriter) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Send keepalive comment frame
			// SSE comment format: ": keepalive\n\n"
			_ = writer.WriteComment("keepalive")
		}
	}
}

// NewStreamCleanupRepository creates a new StreamCleanupRepository.
func NewStreamCleanupRepository(client RedisClientForLifecycle) StreamCleanupRepository {
	return &redisStreamCleanupRepository{
		client: client,
	}
}

// PurgeTeamStreamState deletes all stream keys for a team.
func (r *redisStreamCleanupRepository) PurgeTeamStreamState(ctx context.Context, teamID string) error {
	pattern := fmt.Sprintf("profile:%s:stream:*", teamID)

	var cursor uint64
	for {
		keys, nextCursor, err := r.client.Scan(ctx, cursor, pattern, 100)
		if err != nil {
			return fmt.Errorf("failed to scan stream keys: %w", err)
		}

		// Delete found keys
		for _, key := range keys {
			if err := r.client.Del(ctx, key); err != nil {
				// Log but continue
				continue
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}
