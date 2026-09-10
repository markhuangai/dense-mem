package operations

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

var ErrAuthorityBlocked = errors.New("authority bootstrap blocked")

const CutoverMarkerVersion = "dense-mem.v2.6.1.cutover.v1"

type AuthorityMode string

const AuthorityActive AuthorityMode = "active"

type AuthorityBootstrap struct {
	Mode             AuthorityMode
	Marker           *domain.CompatibilityMarker
	ReadinessMessage string
}

func ClassifyAuthority(ctx context.Context, store operationscontract.AuthorityReader) (AuthorityBootstrap, error) {
	if store == nil {
		return AuthorityBootstrap{}, fmt.Errorf("%w: authority store is required", ErrAuthorityBlocked)
	}
	marker, err := store.GetLatestMarker(ctx)
	if err != nil {
		return AuthorityBootstrap{}, fmt.Errorf("%w: read compatibility marker: %w", ErrAuthorityBlocked, err)
	}
	return classifyAuthorityMarker(marker)
}

func classifyAuthorityMarker(marker *domain.CompatibilityMarker) (AuthorityBootstrap, error) {
	if marker == nil {
		return AuthorityBootstrap{}, fmt.Errorf("%w: compatible cutover marker is required before startup", ErrAuthorityBlocked)
	}
	if marker.MarkerKind != domain.MigrationMarkerKindCutover || marker.Version != CutoverMarkerVersion {
		return AuthorityBootstrap{}, fmt.Errorf("%w: exact v2.6.1 cutover marker is required", ErrAuthorityBlocked)
	}
	switch marker.Status {
	case domain.MigrationMarkerCompatible:
		return AuthorityBootstrap{
			Mode:             AuthorityActive,
			Marker:           marker,
			ReadinessMessage: "compatible authority marker present",
		}, nil
	case domain.MigrationMarkerIncompatible, domain.MigrationMarkerCorrupt:
		return AuthorityBootstrap{}, fmt.Errorf("%w: compatibility marker status %s", ErrAuthorityBlocked, marker.Status)
	default:
		return AuthorityBootstrap{}, fmt.Errorf("%w: unknown compatibility marker status %s", ErrAuthorityBlocked, marker.Status)
	}
}

func CheckActiveAuthority(authority AuthorityBootstrap) error {
	if authority.Mode != AuthorityActive ||
		authority.Marker == nil ||
		authority.Marker.Status != domain.MigrationMarkerCompatible {
		return fmt.Errorf("%w: compatible authority marker is required", ErrAuthorityBlocked)
	}
	return nil
}
