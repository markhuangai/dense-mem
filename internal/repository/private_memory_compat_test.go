package repository

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPrivateMemoryCompatibilityClockSyncIsSafeConcurrently(t *testing.T) {
	repo := NewPrivateMemoryRepository(nil, nil)
	fixed := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return fixed }

	var wait sync.WaitGroup
	for range 100 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			repo.syncClock()
		}()
	}
	wait.Wait()

	require.NotNil(t, repo.Store)
}
