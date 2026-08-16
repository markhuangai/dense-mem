package service

import (
	"context"
	"fmt"
	"time"
)

// RateLimitServiceInterface is the companion interface for RateLimitService.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type RateLimitServiceInterface interface {
	Check(ctx context.Context, subjectID, routePath string, limit int) (allowed bool, remaining int, resetAt time.Time, err error)
}

// RateLimitStore is the minimal store interface for rate-limit counters.
type RateLimitStore interface {
	IncrWithExpire(ctx context.Context, key string, expireSeconds int64) (int64, error)
}

// RateLimitService implements rate limiting using a fixed-window algorithm.
type RateLimitService struct {
	store RateLimitStore
	now   func() time.Time
}

// Ensure RateLimitService implements RateLimitServiceInterface
var _ RateLimitServiceInterface = (*RateLimitService)(nil)

// NewRateLimitService creates a new rate limit service instance.
func NewRateLimitService(store RateLimitStore) *RateLimitService {
	return newRateLimitServiceWithClock(store, time.Now)
}

// newRateLimitServiceWithClock creates a rate limit service with a controllable clock for testing.
func newRateLimitServiceWithClock(store RateLimitStore, now func() time.Time) *RateLimitService {
	return &RateLimitService{
		store: store,
		now:   now,
	}
}

// Check performs a rate limit check for the given authenticated subject and route.
// It returns whether the request is allowed, remaining count, reset time, and any error.
// The key format is: profile:{id}:ratelimit:{routePath}:{windowStartUnix}
// This uses a fixed-window algorithm where the window start is computed from wall clock
// to ensure all concurrent requests within the same time window share the same bucket.
func (s *RateLimitService) Check(ctx context.Context, subjectID, routePath string, limit int) (allowed bool, remaining int, resetAt time.Time, err error) {
	// Compute window start from wall clock (Unix timestamp truncated to minute boundary)
	// This ensures all requests within the same minute share the same bucket
	now := s.now().UTC()
	windowStartUnix := now.Unix() - (now.Unix() % 60)
	windowStart := time.Unix(windowStartUnix, 0).UTC()
	resetAt = windowStart.Add(60 * time.Second)

	// Build the rate limit key directly
	key := fmt.Sprintf("profile:%s:ratelimit:%s:%d", subjectID, routePath, windowStartUnix)

	// Increment and set expire atomically (70s expiry for 60s window to handle clock skew)
	count, err := s.store.IncrWithExpire(ctx, key, 70)
	if err != nil {
		return false, 0, resetAt, fmt.Errorf("failed to increment rate limit counter: %w", err)
	}

	remaining = int(int64(limit) - count)
	if remaining < 0 {
		remaining = 0
	}

	// Check if the request is allowed
	if count > int64(limit) {
		return false, 0, resetAt, nil
	}

	return true, remaining, resetAt, nil
}
