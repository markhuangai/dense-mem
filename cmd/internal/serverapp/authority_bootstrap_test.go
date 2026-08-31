package serverapp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestClassifyAuthorityActivatesWhenCompatibleMarkerPresent(t *testing.T) {
	store := &authorityStoreStub{marker: &domain.CompatibilityMarker{
		MarkerKind: domain.MigrationMarkerKindCutover,
		Version:    cutoverMarkerVersion,
		Status:     domain.MigrationMarkerCompatible,
	}}

	bootstrap, err := ClassifyAuthority(context.Background(), store)

	require.NoError(t, err)
	require.Equal(t, authorityActive, bootstrap.Mode)
	require.Equal(t, store.marker, bootstrap.Marker)
}

func TestClassifyAuthorityFailsClosedForIncompatibleMarker(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{
		marker: &domain.CompatibilityMarker{MarkerKind: domain.MigrationMarkerKindCutover, Version: cutoverMarkerVersion, Status: domain.MigrationMarkerCorrupt},
	})
	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "corrupt")

	_, err = ClassifyAuthority(context.Background(), &authorityStoreStub{
		marker: &domain.CompatibilityMarker{MarkerKind: domain.MigrationMarkerKindCutover, Version: cutoverMarkerVersion, Status: domain.MigrationMarkerIncompatible},
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
		marker: &domain.CompatibilityMarker{MarkerKind: domain.MigrationMarkerKindCutover, Version: cutoverMarkerVersion, Status: "pending"},
	})

	require.ErrorIs(t, err, errAuthorityBlocked)
	require.ErrorContains(t, err, "unknown compatibility marker status pending")
}

type authorityStoreStub struct {
	marker    *domain.CompatibilityMarker
	markerErr error
}

func (s *authorityStoreStub) GetLatestMarker(context.Context) (*domain.CompatibilityMarker, error) {
	if s.markerErr != nil {
		return nil, s.markerErr
	}
	return s.marker, nil
}
