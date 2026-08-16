package inmem

import (
	"context"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/sse"
)

// InMemoryConcurrencyLimiter implements sse.ConcurrencyLimiter using an
// in-memory mutex-protected map keyed by team ID.
type InMemoryConcurrencyLimiter struct {
	mu         sync.Mutex
	entries    map[string]*teamCounterEntry
	maxStreams int
	ttl        time.Duration
	now        func() time.Time
}

type teamCounterEntry struct {
	count     int64
	expiresAt time.Time
}

// NewInMemoryConcurrencyLimiter creates a limiter with the given max concurrent
// streams per team and TTL for counter entries, using the system clock.
func NewInMemoryConcurrencyLimiter(maxStreams int, ttl time.Duration) *InMemoryConcurrencyLimiter {
	return NewInMemoryConcurrencyLimiterWithClock(maxStreams, ttl, time.Now)
}

// NewInMemoryConcurrencyLimiterWithClock creates a limiter that uses the
// supplied clock function for TTL expiry checks. Useful for deterministic testing.
func NewInMemoryConcurrencyLimiterWithClock(maxStreams int, ttl time.Duration, now func() time.Time) *InMemoryConcurrencyLimiter {
	return &InMemoryConcurrencyLimiter{
		entries:    make(map[string]*teamCounterEntry),
		maxStreams: maxStreams,
		ttl:        ttl,
		now:        now,
	}
}

// Acquire increments the concurrent-stream counter for the given team ID.
// If the counter (after incrementing) exceeds maxStreams, the acquire is
// rejected with sse.ErrTooManyStreams and no release function is returned.
// Expired entries are treated as absent before attempting the increment.
func (l *InMemoryConcurrencyLimiter) Acquire(_ context.Context, teamID string) (release func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, exists := l.entries[teamID]

	// Treat expired entries as absent.
	if exists && l.now().After(entry.expiresAt) {
		delete(l.entries, teamID)
		exists = false
	}

	if !exists {
		entry = &teamCounterEntry{}
		l.entries[teamID] = entry
	}

	entry.count++
	if entry.count > int64(l.maxStreams) {
		// Roll back the increment.
		entry.count--
		if entry.count == 0 {
			delete(l.entries, teamID)
		}
		return nil, sse.ErrTooManyStreams
	}

	// Refresh expiry on every successful acquire.
	entry.expiresAt = l.now().Add(l.ttl)

	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()

		if released {
			return
		}
		released = true

		e, ok := l.entries[teamID]
		if !ok {
			return
		}
		e.count--
		if e.count < 0 {
			e.count = 0
		}
		if e.count == 0 {
			delete(l.entries, teamID)
		}
	}, nil
}
