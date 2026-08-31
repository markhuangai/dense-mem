package serverapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var errAuthorityBlocked = errors.New("authority bootstrap blocked")

const cutoverMarkerVersion = "dense-mem.v2.6.1.cutover.v1"

type authorityMode string

const (
	authorityActive authorityMode = "active"
)

type authorityBootstrap struct {
	Mode             authorityMode
	Marker           *domain.CompatibilityMarker
	ReadinessMessage string
}

type authorityBootstrapStore interface {
	GetLatestMarker(ctx context.Context) (*domain.CompatibilityMarker, error)
}

func ClassifyAuthority(ctx context.Context, store authorityBootstrapStore) (authorityBootstrap, error) {
	if store == nil {
		return authorityBootstrap{}, fmt.Errorf("%w: authority store is required", errAuthorityBlocked)
	}
	marker, err := store.GetLatestMarker(ctx)
	if err != nil {
		return authorityBootstrap{}, fmt.Errorf("%w: read compatibility marker: %w", errAuthorityBlocked, err)
	}
	return classifyAuthorityMarker(marker)
}

func classifyAuthorityMarker(marker *domain.CompatibilityMarker) (authorityBootstrap, error) {
	if marker == nil {
		return authorityBootstrap{}, fmt.Errorf("%w: compatible cutover marker is required before startup", errAuthorityBlocked)
	}
	if marker.MarkerKind != domain.MigrationMarkerKindCutover || marker.Version != cutoverMarkerVersion {
		return authorityBootstrap{}, fmt.Errorf("%w: exact v2.6.1 cutover marker is required", errAuthorityBlocked)
	}
	switch marker.Status {
	case domain.MigrationMarkerCompatible:
		return authorityBootstrap{
			Mode:             authorityActive,
			Marker:           marker,
			ReadinessMessage: "compatible authority marker present",
		}, nil
	case domain.MigrationMarkerIncompatible, domain.MigrationMarkerCorrupt:
		return authorityBootstrap{}, fmt.Errorf("%w: compatibility marker status %s", errAuthorityBlocked, marker.Status)
	default:
		return authorityBootstrap{}, fmt.Errorf("%w: unknown compatibility marker status %s", errAuthorityBlocked, marker.Status)
	}
}
