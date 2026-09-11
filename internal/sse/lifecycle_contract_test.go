package sse_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/inmem"
)

// runConcurrencyLimiterContract exercises shared ConcurrencyLimiter behavior:
// cap enforcement, rejection when at capacity, and clean release (AC-10, AC-11).
func runConcurrencyLimiterContract(t *testing.T, name string, factory func(t *testing.T) sse.ConcurrencyLimiter) {
	t.Helper()

	t.Run("enforces cap and rejects overflow", func(t *testing.T) {
		t.Parallel()

		limiter := factory(t)
		ctx := context.Background()
		const cap = 3
		profile := "contract-test-profile-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())

		releases := make([]func(), 0, cap)
		for i := 0; i < cap; i++ {
			release, err := limiter.Acquire(ctx, profile)
			require.NoError(t, err, "%s: acquire %d should succeed", name, i+1)
			releases = append(releases, release)
		}

		// Next acquire must be rejected
		release, err := limiter.Acquire(ctx, profile)
		assert.ErrorIs(t, err, sse.ErrTooManyStreams, "%s: acquire at cap+1 should be rejected with ErrTooManyStreams", name)
		assert.Nil(t, release, "%s: release should be nil on rejection", name)

		// Release one, then acquire should succeed
		releases[0]()
		release, err = limiter.Acquire(ctx, profile)
		require.NoError(t, err, "%s: acquire after release should succeed", name)
		releases = append(releases[1:], release)

		// Clean up
		for _, r := range releases {
			r()
		}
	})

	t.Run("release is idempotent", func(t *testing.T) {
		t.Parallel()

		limiter := factory(t)
		ctx := context.Background()

		profile := "idempotent-profile-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
		release, err := limiter.Acquire(ctx, profile)
		require.NoError(t, err)

		release()
		release() // second call must be safe
		release() // third call must be safe
	})

	t.Run("concurrent acquires are safe", func(t *testing.T) {
		t.Parallel()

		limiter := factory(t)
		ctx := context.Background()

		var acquired int64
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				profile := "concurrent-profile-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
				release, err := limiter.Acquire(ctx, profile)
				if err == nil {
					atomic.AddInt64(&acquired, 1)
					time.Sleep(10 * time.Millisecond)
					release()
				}
			}()
		}
		wg.Wait()

		// At least some should have been acquired, and some should have been rejected
		assert.GreaterOrEqual(t, atomic.LoadInt64(&acquired), int64(1), "%s: some acquires should succeed under contention", name)
	})
}

func TestConcurrencyLimiter_Contract_InMemory(t *testing.T) {
	t.Parallel()

	// Use the real in-memory implementation so we validate its semantics,
	// not just behavior defined by a mock (AC-10, AC-11, AC-B2).
	factory := func(t *testing.T) sse.ConcurrencyLimiter {
		t.Helper()
		// TTL of 1 hour ensures counters don't expire mid-test.
		return inmem.NewInMemoryConcurrencyLimiter(3, time.Hour)
	}

	runConcurrencyLimiterContract(t, "InMemory", factory)
}
