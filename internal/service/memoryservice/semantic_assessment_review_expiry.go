package memoryservice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
)

type semanticAssessmentReviewExpiryState struct {
	lastSuccess time.Time
	inFlight    bool
}

type semanticAssessmentReviewExpiryThrottle struct {
	delegate SemanticAssessmentReviewExpiry
	interval time.Duration
	mu       sync.Mutex
	states   map[string]*semanticAssessmentReviewExpiryState
}

func NewSemanticAssessmentReviewExpiryThrottle(
	delegate SemanticAssessmentReviewExpiry,
	interval time.Duration,
) SemanticAssessmentReviewExpiry {
	if interval <= 0 {
		interval = time.Minute
	}
	return &semanticAssessmentReviewExpiryThrottle{
		delegate: delegate,
		interval: interval,
		states:   map[string]*semanticAssessmentReviewExpiryState{},
	}
}

func (t *semanticAssessmentReviewExpiryThrottle) ExpirePlacementAssessmentReviews(
	ctx context.Context,
	input repository.ExpirePlacementAssessmentReviewsInput,
) (int64, error) {
	if t == nil || t.delegate == nil {
		return 0, errors.New("semantic assessment review expiry delegate is required")
	}
	teamID := strings.TrimSpace(input.TeamID)
	if teamID == "" {
		return t.delegate.ExpirePlacementAssessmentReviews(ctx, input)
	}
	now := input.Now.UTC()
	if input.Now.IsZero() {
		now = time.Now().UTC()
		input.Now = now
	}

	t.mu.Lock()
	state := t.states[teamID]
	if state == nil {
		state = &semanticAssessmentReviewExpiryState{}
		t.states[teamID] = state
	}
	if state.inFlight || (!state.lastSuccess.IsZero() && now.Before(state.lastSuccess.Add(t.interval))) {
		t.mu.Unlock()
		return 0, nil
	}
	state.inFlight = true
	t.mu.Unlock()

	affected, err := t.delegate.ExpirePlacementAssessmentReviews(ctx, input)
	t.mu.Lock()
	state.inFlight = false
	if err == nil {
		state.lastSuccess = now
	}
	t.mu.Unlock()
	return affected, err
}
