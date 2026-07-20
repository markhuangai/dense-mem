package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	internalhttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

var errV2MigrationPending = errors.New("v2 migration marker pending")

type dormantV2Bootstrap struct {
	Mode                 string
	Enabled              bool
	AcceptsDataPlane     bool
	ReadinessDescription string
	checks               []internalhttp.HealthCheck
}

type v2MigrationStatusReader interface {
	Status(ctx context.Context) (*domain.V2MigrationControlStatus, error)
}

type v2SearchReadinessChecker interface {
	CheckSearchReadiness(ctx context.Context) (*repository.V2SearchReadiness, error)
}

type defaultCatalogV2Dependencies struct {
	Evaluation        repository.V2EvaluationRepository
	EvaluationEnabled bool
	Communities       repository.V2CommunityRepository
}

func buildDefaultCatalogV2Dependencies(bootstrap dormantV2Bootstrap, semantic *repository.V2SemanticRepositoryImpl) defaultCatalogV2Dependencies {
	if !bootstrap.Enabled {
		return defaultCatalogV2Dependencies{}
	}
	if bootstrap.Mode != config.V2BootModeUAT {
		return defaultCatalogV2Dependencies{}
	}
	return defaultCatalogV2Dependencies{Evaluation: semantic, EvaluationEnabled: true, Communities: semantic}
}

func buildDormantV2Bootstrap(cfg config.Config, db *gorm.DB, migration v2MigrationStatusReader) dormantV2Bootstrap {
	if !cfg.IsV2BootEnabled() {
		return dormantV2Bootstrap{
			Mode:                 cfg.GetV2BootMode(),
			Enabled:              false,
			AcceptsDataPlane:     false,
			ReadinessDescription: "v2 boot disabled",
		}
	}

	return dormantV2Bootstrap{
		Mode:                 cfg.GetV2BootMode(),
		Enabled:              true,
		AcceptsDataPlane:     false,
		ReadinessDescription: "v2 dormant; normal v1 authority remains active",
		checks: []internalhttp.HealthCheck{
			{
				Name: "v2_postgres_topology",
				Check: func(ctx context.Context) error {
					return postgres.ValidateSinglePrimaryTopology(ctx, db)
				},
			},
			{
				Name: "v2_pgvector",
				Check: func(ctx context.Context) error {
					return postgres.CheckPGVectorExtension(ctx, db)
				},
			},
			{
				Name: "v2_workers",
				Check: func(context.Context) error {
					return nil
				},
			},
			{
				Name:     "v2_migration_state",
				Optional: !cfg.GetV2LegacyMigrationRequired(),
				Check: func(ctx context.Context) error {
					return checkV2MigrationState(ctx, cfg, migration)
				},
			},
		},
	}
}

func checkV2MigrationState(ctx context.Context, cfg config.Config, migration v2MigrationStatusReader) error {
	if migration == nil {
		if cfg.GetV2LegacyMigrationRequired() {
			return errV2MigrationPending
		}
		return nil
	}
	status, err := migration.Status(ctx)
	if err != nil {
		return err
	}
	if status == nil {
		if cfg.GetV2LegacyMigrationRequired() {
			return errV2MigrationPending
		}
		return nil
	}
	switch status.State {
	case domain.V2MigrationStateNotRequired, domain.V2MigrationStateCutOver:
		return nil
	default:
		return fmt.Errorf("%w: %s", errV2MigrationPending, status.State)
	}
}

func checkV2SearchReadiness(ctx context.Context, search v2SearchReadinessChecker) error {
	if search == nil {
		return fmt.Errorf("%w: search repository is required", repository.ErrV2SearchContractMismatch)
	}
	readiness, err := search.CheckSearchReadiness(ctx)
	if err != nil {
		return err
	}
	if readiness == nil || readiness.Ready {
		return nil
	}
	reasons := make([]string, 0, len(readiness.Reasons))
	for _, reason := range readiness.Reasons {
		message := strings.TrimSpace(reason.Message)
		if message == "" {
			message = strings.TrimSpace(reason.Code)
		}
		if message != "" {
			reasons = append(reasons, message)
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "search readiness check failed")
	}
	return fmt.Errorf("%w: %s", repository.ErrV2SearchContractMismatch, strings.Join(reasons, "; "))
}

func (b dormantV2Bootstrap) HealthChecks() []internalhttp.HealthCheck {
	return append([]internalhttp.HealthCheck(nil), b.checks...)
}
