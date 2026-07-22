package serverapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
)

var errAuthorityBlocked = errors.New("authority bootstrap blocked")

type authorityMode string

const (
	authorityActive authorityMode = "active"
)

type authorityBootstrap struct {
	Mode             authorityMode
	DataPlaneAllowed bool
	Marker           *domain.V2CompatibilityMarker
	ReadinessMessage string
}

type authorityBootstrapStore interface {
	GetLatestMarker(ctx context.Context) (*domain.V2CompatibilityMarker, error)
	CommitFreshV2Authority(ctx context.Context, input repository.V2CommitFreshV2AuthorityInput) (*domain.V2CompatibilityMarker, error)
}

func ClassifyAuthority(ctx context.Context, store authorityBootstrapStore) (authorityBootstrap, error) {
	if store == nil {
		return authorityBootstrap{}, fmt.Errorf("%w: migration control store is required", errAuthorityBlocked)
	}
	marker, err := store.GetLatestMarker(ctx)
	if err != nil {
		return authorityBootstrap{}, fmt.Errorf("%w: read compatibility marker: %w", errAuthorityBlocked, err)
	}
	return classifyAuthorityMarker(marker)
}

func EnsureAuthority(ctx context.Context, store authorityBootstrapStore) (authorityBootstrap, error) {
	if store == nil {
		return authorityBootstrap{}, fmt.Errorf("%w: migration control store is required", errAuthorityBlocked)
	}
	marker, err := store.GetLatestMarker(ctx)
	if err != nil {
		return authorityBootstrap{}, fmt.Errorf("%w: read compatibility marker: %w", errAuthorityBlocked, err)
	}
	if marker == nil {
		marker, err = store.CommitFreshV2Authority(ctx, repository.V2CommitFreshV2AuthorityInput{
			MarkerVersion: migrationcontrol.DefaultCutoverMarkerVersion,
			Now:           time.Now().UTC(),
			Metadata:      map[string]any{"created_by": "server_boot"},
		})
		if err != nil {
			return authorityBootstrap{}, fmt.Errorf("%w: create fresh V2 authority marker: %w", errAuthorityBlocked, err)
		}
	}
	return classifyAuthorityMarker(marker)
}

func classifyAuthorityMarker(marker *domain.V2CompatibilityMarker) (authorityBootstrap, error) {
	if marker == nil {
		return authorityBootstrap{}, fmt.Errorf("%w: compatible V2 cutover marker is required before startup", errAuthorityBlocked)
	}
	switch marker.Status {
	case domain.V2MigrationMarkerCompatible:
		return authorityBootstrap{
			Mode:             authorityActive,
			DataPlaneAllowed: true,
			Marker:           marker,
			ReadinessMessage: "compatible V2 authority marker present",
		}, nil
	case domain.V2MigrationMarkerIncompatible, domain.V2MigrationMarkerCorrupt:
		return authorityBootstrap{}, fmt.Errorf("%w: compatibility marker status %s", errAuthorityBlocked, marker.Status)
	default:
		return authorityBootstrap{}, fmt.Errorf("%w: unknown compatibility marker status %s", errAuthorityBlocked, marker.Status)
	}
}
