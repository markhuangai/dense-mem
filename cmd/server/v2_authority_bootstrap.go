package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
)

var errV2AuthorityBlocked = errors.New("v2 authority bootstrap blocked")

type v2AuthorityMode string

const (
	v2AuthorityActive            v2AuthorityMode = "active"
	v2AuthorityFresh             v2AuthorityMode = "fresh"
	v2AuthorityMigrationRequired v2AuthorityMode = "migration_required"
)

type v2AuthorityBootstrap struct {
	Mode              v2AuthorityMode
	DataPlaneAllowed  bool
	RequiresNeo4j     bool
	MigrationRequired bool
	Marker            *domain.V2CompatibilityMarker
	ReadinessMessage  string
}

type v2AuthorityBootstrapStore interface {
	GetLatestMarker(ctx context.Context) (*domain.V2CompatibilityMarker, error)
	CommitFreshV2Authority(ctx context.Context, input repository.V2CommitFreshV2AuthorityInput) (*domain.V2CompatibilityMarker, error)
}

type legacyMigrationDataPlaneStatusGate struct {
	inner interface {
		Status(ctx context.Context) (*domain.V2MigrationControlStatus, error)
	}
}

func (g legacyMigrationDataPlaneStatusGate) Status(ctx context.Context) (*domain.V2MigrationControlStatus, error) {
	if g.inner == nil {
		return nil, nil
	}
	status, err := g.inner.Status(ctx)
	if err != nil || status == nil || !status.DataPlaneAllowed {
		return status, err
	}
	out := *status
	out.DataPlaneAllowed = false
	if out.State == domain.V2MigrationStateCutOver {
		out.ReadinessMessage = "migration cutover complete; restart to activate PostgreSQL V2 authority"
	} else if out.ReadinessMessage == "" {
		out.ReadinessMessage = "legacy migration is required; data plane is disabled"
	}
	return &out, nil
}

func checkV2MigrationDataPlaneReadiness(ctx context.Context, statusProvider interface {
	Status(context.Context) (*domain.V2MigrationControlStatus, error)
}) error {
	if statusProvider == nil {
		return fmt.Errorf("%w: migration status is required", errV2AuthorityBlocked)
	}
	status, err := statusProvider.Status(ctx)
	if err != nil {
		return err
	}
	if status == nil {
		return fmt.Errorf("%w: migration status is unavailable", errV2AuthorityBlocked)
	}
	if status.DataPlaneAllowed {
		return nil
	}
	message := status.ReadinessMessage
	if message == "" {
		message = "legacy migration is required; data plane is disabled"
	}
	return fmt.Errorf("%w: %s", errV2AuthorityBlocked, message)
}

func classifyV2Authority(
	ctx context.Context,
	cfg config.Config,
	store v2AuthorityBootstrapStore,
) (v2AuthorityBootstrap, error) {
	if store == nil {
		return v2AuthorityBootstrap{}, fmt.Errorf("%w: migration control store is required", errV2AuthorityBlocked)
	}
	marker, err := store.GetLatestMarker(ctx)
	if err != nil {
		return v2AuthorityBootstrap{}, fmt.Errorf("%w: read compatibility marker: %w", errV2AuthorityBlocked, err)
	}
	if marker != nil {
		switch marker.Status {
		case domain.V2MigrationMarkerCompatible:
			return v2AuthorityBootstrap{
				Mode:             v2AuthorityActive,
				DataPlaneAllowed: true,
				Marker:           marker,
				ReadinessMessage: "compatible V2 authority marker present",
			}, nil
		case domain.V2MigrationMarkerIncompatible, domain.V2MigrationMarkerCorrupt:
			return v2AuthorityBootstrap{}, fmt.Errorf("%w: compatibility marker status %s", errV2AuthorityBlocked, marker.Status)
		default:
			return v2AuthorityBootstrap{}, fmt.Errorf("%w: unknown compatibility marker status %s", errV2AuthorityBlocked, marker.Status)
		}
	}
	if cfg.HasNeo4jConfig() {
		return v2AuthorityBootstrap{
			Mode:              v2AuthorityMigrationRequired,
			DataPlaneAllowed:  false,
			RequiresNeo4j:     true,
			MigrationRequired: true,
			ReadinessMessage:  "legacy migration is required; use the private control portal",
		}, nil
	}
	marker, err = store.CommitFreshV2Authority(ctx, repository.V2CommitFreshV2AuthorityInput{
		MarkerVersion: migrationcontrol.DefaultCutoverMarkerVersion,
		Metadata: map[string]any{
			"source": "startup_fresh_v2_authority",
		},
	})
	if err != nil {
		if errors.Is(err, repository.ErrV2MigrationFreshInitBlocked) {
			return v2AuthorityBootstrap{}, fmt.Errorf("%w: postgres database is not empty and no Neo4j migration source is configured: %w", errV2AuthorityBlocked, err)
		}
		return v2AuthorityBootstrap{}, fmt.Errorf("%w: initialize fresh V2 authority: %w", errV2AuthorityBlocked, err)
	}
	return v2AuthorityBootstrap{
		Mode:             v2AuthorityFresh,
		DataPlaneAllowed: true,
		Marker:           marker,
		ReadinessMessage: "fresh V2 authority initialized",
	}, nil
}
