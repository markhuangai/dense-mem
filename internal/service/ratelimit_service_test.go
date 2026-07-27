package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingStore struct {
	lastKey string
	lastTTL int64
	count   int64
}

func (s *capturingStore) IncrWithExpire(_ context.Context, key string, ttl int64) (int64, error) {
	s.lastKey = key
	s.lastTTL = ttl
	s.count++
	return s.count, nil
}

func TestRateLimitService_BuildsKeyWithoutKeyBuilder(t *testing.T) {
	store := &capturingStore{}
	now := time.Unix(1700000065, 0).UTC() // window start = 1700000040
	svc := newRateLimitServiceWithClock(store, func() time.Time { return now })

	allowed, remaining, resetAt, err := svc.Check(context.Background(), "profile-1", "/mcp/search", 10)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Equal(t, 9, remaining)
	assert.Equal(t, "profile:profile-1:ratelimit:/mcp/search:1700000040", store.lastKey)
	assert.Equal(t, time.Unix(1700000100, 0).UTC(), resetAt)
	assert.Equal(t, int64(70), store.lastTTL)
}

func TestRateLimitService_ExceedsLimit(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	store := &capturingStore{count: 4} // will return 5 on first call (limit=2 → denied)
	svc := newRateLimitServiceWithClock(store, func() time.Time { return now })

	allowed, remaining, _, err := svc.Check(context.Background(), "p", "/r", 2)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Equal(t, 0, remaining)
}
