package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type identityCleanupPreflightRepositoryStub struct {
	report domain.IdentityCleanupPreflight
	err    error
	ctx    context.Context
}

func (s *identityCleanupPreflightRepositoryStub) ReadIdentityCleanupPreflight(ctx context.Context) (domain.IdentityCleanupPreflight, error) {
	s.ctx = ctx
	return s.report, s.err
}

func TestIdentityCleanupPreflightServiceForwardsRepositoryResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	report := domain.IdentityCleanupPreflight{Ready: true, BridgeState: "cutover_ready"}
	repo := &identityCleanupPreflightRepositoryStub{report: report}
	svc := NewIdentityCleanupPreflightService(repo)

	got, err := svc.Preflight(ctx)

	require.NoError(t, err)
	require.Equal(t, report, got)
	require.Same(t, ctx, repo.ctx)
}

func TestIdentityCleanupPreflightServicePropagatesRepositoryError(t *testing.T) {
	wantErr := errors.New("database unavailable")
	svc := NewIdentityCleanupPreflightService(&identityCleanupPreflightRepositoryStub{err: wantErr})

	_, err := svc.Preflight(context.Background())

	require.ErrorIs(t, err, wantErr)
}

func TestIdentityCleanupPreflightServiceReportsUnavailable(t *testing.T) {
	var nilService *IdentityCleanupPreflightService
	for _, svc := range []*IdentityCleanupPreflightService{
		nilService,
		NewIdentityCleanupPreflightService(nil),
	} {
		_, err := svc.Preflight(context.Background())
		require.ErrorIs(t, err, ErrIdentityCleanupPreflightUnavailable)
	}
}
