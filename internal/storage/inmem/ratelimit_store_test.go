package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryRateLimitStore_ExpiresKeys(t *testing.T) {
	now := time.Unix(1700000000, 0)
	store := NewInMemoryRateLimitStoreWithClock(func() time.Time { return now })

	count, err := store.IncrWithExpire(context.Background(), "k", 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Advance past expiry
	now = now.Add(2 * time.Second)
	count, err = store.IncrWithExpire(context.Background(), "k", 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestNewInMemoryRateLimitStoreUsesDefaultClock(t *testing.T) {
	store := NewInMemoryRateLimitStore()
	count, err := store.IncrWithExpire(context.Background(), "default-clock", 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestInMemoryRateLimitStore_IncrementsSameKey(t *testing.T) {
	now := time.Unix(1700000000, 0)
	store := NewInMemoryRateLimitStoreWithClock(func() time.Time { return now })

	count1, err := store.IncrWithExpire(context.Background(), "k", 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count1)

	count2, err := store.IncrWithExpire(context.Background(), "k", 10)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count2)

	count3, err := store.IncrWithExpire(context.Background(), "k", 10)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count3)
}

func TestInMemoryRateLimitStore_DifferentKeysAreIndependent(t *testing.T) {
	now := time.Unix(1700000000, 0)
	store := NewInMemoryRateLimitStoreWithClock(func() time.Time { return now })

	c1, err := store.IncrWithExpire(context.Background(), "a", 60)
	require.NoError(t, err)
	assert.Equal(t, int64(1), c1)

	c2, err := store.IncrWithExpire(context.Background(), "b", 60)
	require.NoError(t, err)
	assert.Equal(t, int64(1), c2)
}

func TestInMemoryRateLimitStore_ConcurrentAccess(t *testing.T) {
	now := time.Unix(1700000000, 0)
	store := NewInMemoryRateLimitStoreWithClock(func() time.Time { return now })

	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func() {
			_, err := store.IncrWithExpire(context.Background(), "concurrent-key", 60)
			require.NoError(t, err)
			done <- true
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}

	count, err := store.IncrWithExpire(context.Background(), "concurrent-key", 60)
	require.NoError(t, err)
	assert.Equal(t, int64(101), count)
}
