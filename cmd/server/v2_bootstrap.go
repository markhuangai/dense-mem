package main

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/config"
	internalhttp "github.com/markhuangai/dense-mem/internal/http"
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

func buildDormantV2Bootstrap(cfg config.Config, db *gorm.DB) dormantV2Bootstrap {
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
					if !cfg.GetPGVectorExtensionRequired() {
						return nil
					}
					return postgres.CheckPGVectorExtension(ctx, db)
				},
			},
			{
				Name: "v2_provider_contract",
				Check: func(context.Context) error {
					return cfg.ValidateV2DormantStartup()
				},
			},
			{
				Name: "v2_search_profile",
				Check: func(context.Context) error {
					return cfg.ValidateV2DormantStartup()
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
				Optional: true,
				Check: func(context.Context) error {
					return errV2MigrationPending
				},
			},
		},
	}
}

func (b dormantV2Bootstrap) HealthChecks() []internalhttp.HealthCheck {
	return append([]internalhttp.HealthCheck(nil), b.checks...)
}
