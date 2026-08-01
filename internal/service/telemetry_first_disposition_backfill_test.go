package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestPlacementFirstDispositionBackfillServiceContinuesAfterPartialBatch(t *testing.T) {
	backfillRepo := &placementFirstDispositionBackfillStub{
		results: []repository.PlacementFirstDispositionBackfillResult{
			{Candidates: 1, Inserted: 1},
			{SweepComplete: true},
		},
	}
	service := NewPlacementFirstDispositionBackfillService(backfillRepo, nil)
	done := make(chan struct{})
	go func() {
		service.run(context.Background())
		close(done)
	}()

	require.Eventually(t, func() bool {
		return backfillRepo.callCount() == 2
	}, time.Second, 5*time.Millisecond)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("backfill service did not stop after a completed sweep")
	}
}

type placementFirstDispositionBackfillStub struct {
	mu      sync.Mutex
	results []repository.PlacementFirstDispositionBackfillResult
	calls   int
}

func (s *placementFirstDispositionBackfillStub) BackfillPlacementFirstDispositionMarkers(
	context.Context,
	int,
) (repository.PlacementFirstDispositionBackfillResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls >= len(s.results) {
		s.calls++
		return repository.PlacementFirstDispositionBackfillResult{SweepComplete: true}, nil
	}
	result := s.results[s.calls]
	s.calls++
	return result, nil
}

func (s *placementFirstDispositionBackfillStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
