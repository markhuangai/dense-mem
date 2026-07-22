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
	store := &authorityStoreStub{marker: &domain.V2CompatibilityMarker{
		Status: domain.V2MigrationMarkerCompatible,
	}}

	bootstrap, err := ClassifyAuthority(context.Background(), store)

	require.NoError(t, err)
	require.Equal(t, authorityActive, bootstrap.Mode)
	require.Equal(t, store.marker, bootstrap.Marker)
}

func TestClassifyAuthorityFailsClosedForIncompatibleMarker(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{
		marker: &domain.V2CompatibilityMarker{Status: domain.V2MigrationMarkerCorrupt},
	})
	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "corrupt")

	_, err = ClassifyAuthority(context.Background(), &authorityStoreStub{
		marker: &domain.V2CompatibilityMarker{Status: domain.V2MigrationMarkerIncompatible},
	})
	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "incompatible")
}

func TestClassifyAuthorityFailsClosedWithoutMarker(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{})
	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "compatible V2 cutover marker")
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
		marker: &domain.V2CompatibilityMarker{Status: "pending"},
	})

	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "unknown compatibility marker status pending")
}

func TestEnsureAuthorityCreatesFreshMarkerWhenNoneExists(t *testing.T) {
	store := &authorityStoreStub{
		freshMarker: &domain.V2CompatibilityMarker{
			Status:   domain.V2MigrationMarkerCompatible,
			Metadata: map[string]any{"fresh_install": true},
		},
	}

	bootstrap, err := EnsureAuthority(context.Background(), store)

	require.NoError(t, err)
	require.Equal(t, authorityActive, bootstrap.Mode)
	require.Equal(t, store.freshMarker, bootstrap.Marker)
	require.Equal(t, 1, store.freshCommits)
}

func TestEnsureAuthorityFailsWhenFreshMarkerCreationBlocked(t *testing.T) {
	_, err := EnsureAuthority(context.Background(), &authorityStoreStub{
		freshErr: repository.ErrV2MigrationFreshInitBlocked,
	})

	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorIs(t, err, repository.ErrV2MigrationFreshInitBlocked)
	require.ErrorContains(t, err, "create fresh V2 authority marker")
}

type authorityStoreStub struct {
	marker       *domain.V2CompatibilityMarker
	markerErr    error
	freshMarker  *domain.V2CompatibilityMarker
	freshErr     error
	freshCommits int
}

func (s *authorityStoreStub) GetLatestMarker(context.Context) (*domain.V2CompatibilityMarker, error) {
	if s.markerErr != nil {
		return nil, s.markerErr
	}
	return s.marker, nil
}

func (s *authorityStoreStub) CommitFreshV2Authority(context.Context, repository.V2CommitFreshV2AuthorityInput) (*domain.V2CompatibilityMarker, error) {
	s.freshCommits++
	if s.freshErr != nil {
		return nil, s.freshErr
	}
	if s.freshMarker != nil {
		return s.freshMarker, nil
	}
	return nil, errors.New("unexpected fresh authority commit")
}
