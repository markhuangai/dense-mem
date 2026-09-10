package operations

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
		Version:    CutoverMarkerVersion,
		Status:     domain.MigrationMarkerCompatible,
	}}

	bootstrap, err := ClassifyAuthority(context.Background(), store)

	require.NoError(t, err)
	require.Equal(t, AuthorityActive, bootstrap.Mode)
	require.Equal(t, store.marker, bootstrap.Marker)
}

func TestClassifyAuthorityFailsClosedForIncompatibleMarker(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{
		marker: &domain.CompatibilityMarker{MarkerKind: domain.MigrationMarkerKindCutover, Version: CutoverMarkerVersion, Status: domain.MigrationMarkerCorrupt},
	})
	require.ErrorIs(t, err, ErrAuthorityBlocked)
	require.ErrorContains(t, err, "corrupt")

	_, err = ClassifyAuthority(context.Background(), &authorityStoreStub{
		marker: &domain.CompatibilityMarker{MarkerKind: domain.MigrationMarkerKindCutover, Version: CutoverMarkerVersion, Status: domain.MigrationMarkerIncompatible},
	})
	require.ErrorIs(t, err, ErrAuthorityBlocked)
	require.ErrorContains(t, err, "incompatible")
}

func TestClassifyAuthorityFailsClosedWithoutMarker(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{})
	require.ErrorIs(t, err, ErrAuthorityBlocked)
	require.ErrorContains(t, err, "compatible cutover marker")
}

func TestClassifyAuthorityFailsClosedWhenMarkerReadFails(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{
		markerErr: errors.New("database unavailable"),
	})
	require.ErrorIs(t, err, ErrAuthorityBlocked)
	require.ErrorContains(t, err, "read compatibility marker")
	require.ErrorContains(t, err, "database unavailable")
}

func TestClassifyAuthorityFailsClosedForUnknownMarkerStatus(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), &authorityStoreStub{
		marker: &domain.CompatibilityMarker{MarkerKind: domain.MigrationMarkerKindCutover, Version: CutoverMarkerVersion, Status: "pending"},
	})

	require.ErrorIs(t, err, ErrAuthorityBlocked)
	require.ErrorContains(t, err, "unknown compatibility marker status pending")
}

func TestClassifyAuthorityRejectsWrongMarkerIdentityAndNilStore(t *testing.T) {
	_, err := ClassifyAuthority(context.Background(), nil)
	require.ErrorIs(t, err, ErrAuthorityBlocked)
	require.ErrorContains(t, err, "authority store is required")

	for _, marker := range []*domain.CompatibilityMarker{
		{MarkerKind: "wrong", Version: CutoverMarkerVersion, Status: domain.MigrationMarkerCompatible},
		{MarkerKind: domain.MigrationMarkerKindCutover, Version: "wrong", Status: domain.MigrationMarkerCompatible},
	} {
		_, err = ClassifyAuthority(context.Background(), &authorityStoreStub{marker: marker})
		require.ErrorIs(t, err, ErrAuthorityBlocked)
		require.ErrorContains(t, err, "exact v2.6.1 cutover marker")
	}
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
