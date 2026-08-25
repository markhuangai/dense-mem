package serverapp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestClassifyAuthorityActivatesWhenCompatibleMarkerPresent(t *testing.T) {
	store := &authorityStoreStub{marker: &domain.CompatibilityMarker{
		Version: cutoverMarkerVersion, Status: domain.MigrationMarkerCompatible,
	}}

	bootstrap, err := ClassifyAuthority(context.Background(), store)

	require.NoError(t, err)
	require.Equal(t, authorityActive, bootstrap.Mode)
	require.Equal(t, store.marker, bootstrap.Marker)
}

func TestClassifyAuthorityFailsClosedForIncompatibleMarker(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{
		marker: &domain.CompatibilityMarker{Status: domain.MigrationMarkerCorrupt},
	})
	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "corrupt")

	_, err = ClassifyAuthority(context.Background(), &authorityStoreStub{
		marker: &domain.CompatibilityMarker{Status: domain.MigrationMarkerIncompatible},
	})
	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "incompatible")
}

func TestClassifyAuthorityFailsClosedWithoutMarker(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{})
	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "compatible cutover marker")
}

func TestClassifyAuthorityFailsClosedWhenMarkerReadFails(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{
		markerErr: errors.New("database unavailable"),
	})
	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "read compatibility marker")
	require.ErrorContains(t, err, "database unavailable")
}

func TestClassifyAuthorityFailsClosedForUnknownMarkerStatus(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{
		marker: &domain.CompatibilityMarker{Status: "pending"},
	})

	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "unknown compatibility marker status pending")
}

func TestEnsureAuthorityFailsClosedWhenMarkerIsAbsent(t *testing.T) {
	store := &authorityStoreStub{}
	_, err := EnsureAuthority(context.Background(), store)

	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "compatible cutover marker")
	require.Zero(t, store.freshCommits)
}

type authorityStoreStub struct {
	marker       *domain.CompatibilityMarker
	markerErr    error
	freshMarker  *domain.CompatibilityMarker
	freshErr     error
	freshCommits int
}

func (s *authorityStoreStub) GetLatestMarker(context.Context) (*domain.CompatibilityMarker, error) {
	if s.markerErr != nil {
		return nil, s.markerErr
	}
	return s.marker, nil
}

func (s *authorityStoreStub) CommitFreshAuthority(context.Context, repository.CommitFreshAuthorityInput) (*domain.CompatibilityMarker, error) {
	s.freshCommits++
	if s.freshErr != nil {
		return nil, s.freshErr
	}
	if s.freshMarker != nil {
		return s.freshMarker, nil
	}
	return nil, errors.New("unexpected fresh authority commit")
}
