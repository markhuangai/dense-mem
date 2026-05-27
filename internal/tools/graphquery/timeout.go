package graphquery

import (
	"context"
	"time"
)

type timeoutService struct {
	inner          GraphQueryService
	defaultTimeout time.Duration
}

var _ GraphQueryService = (*timeoutService)(nil)

func NewTimeoutService(inner GraphQueryService, defaultTimeout time.Duration) GraphQueryService {
	return &timeoutService{inner: inner, defaultTimeout: defaultTimeout}
}

func (s *timeoutService) Execute(ctx context.Context, profileID string, query string, params map[string]any) (*GraphQueryResult, error) {
	if s.defaultTimeout <= 0 {
		return s.inner.Execute(ctx, profileID, query, params)
	}
	if _, ok := ctx.Deadline(); ok {
		return s.inner.Execute(ctx, profileID, query, params)
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, s.defaultTimeout)
	defer cancel()
	return s.inner.Execute(timeoutCtx, profileID, query, params)
}
