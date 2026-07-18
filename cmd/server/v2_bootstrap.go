package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	internalhttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

var (
	errV2PGVectorDegraded    = errors.New("pgvector extension requirement disabled")
	errV2MigrationPending    = errors.New("v2 legacy migration pending")
	errV2QueueNotReady       = errors.New("v2 queue readiness failed")
	errV2SchemaIndexNotReady = errors.New("v2 schema/index readiness failed")
	errV2WorkersNotReady     = errors.New("v2 worker readiness failed")
)

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
				Name:     "v2_pgvector",
				Optional: !cfg.GetPGVectorExtensionRequired(),
				Check: func(ctx context.Context) error {
					if !cfg.GetPGVectorExtensionRequired() {
						return errV2PGVectorDegraded
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
				Name: "v2_schema_index_profile",
				Check: func(context.Context) error {
					return validateV2SchemaIndexReadiness(cfg)
				},
			},
			{
				Name: "v2_queue_profile",
				Check: func(context.Context) error {
					return validateV2QueueReadiness(cfg)
				},
			},
			{
				Name: "v2_workers",
				Check: func(context.Context) error {
					return validateV2WorkerReadiness(cfg)
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
	switch status.State {
	case domain.V2MigrationStateNotRequired, domain.V2MigrationStateCutOver:
		return nil
	default:
		return fmt.Errorf("%w: %s", errV2MigrationPending, status.State)
	}
}

func validateV2WorkerReadiness(cfg config.Config) error {
	if err := requirePositiveV2Readiness("MEMORY_PLACEMENT_WORKER_COUNT", cfg.GetMemoryPlacementWorkerCount(), errV2WorkersNotReady); err != nil {
		return err
	}
	return requirePositiveV2Readiness("EMBEDDING_WORKER_COUNT", cfg.GetEmbeddingWorkerCount(), errV2WorkersNotReady)
}

func validateV2SchemaIndexReadiness(cfg config.Config) error {
	required := []struct {
		field string
		value string
	}{
		{"SEARCH_DOCUMENT_FORMAT_VERSION", cfg.GetSearchDocumentFormatVersion()},
		{"EMBEDDING_NORMALIZATION_VERSION", cfg.GetEmbeddingNormalizationVersion()},
		{"PREDICATE_REGISTRY_VERSION", cfg.GetPredicateRegistryVersion()},
		{"PGVECTOR_DISTANCE", cfg.GetPGVectorDistance()},
		{"PGVECTOR_ANN_STRATEGY", cfg.GetPGVectorANNStrategy()},
	}
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			return wrapV2ReadinessError(item.field, "required for v2 schema/index readiness", errV2SchemaIndexNotReady)
		}
	}
	if err := requirePositiveV2Readiness("PGVECTOR_HNSW_M", cfg.GetPGVectorHNSWM(), errV2SchemaIndexNotReady); err != nil {
		return err
	}
	if err := requirePositiveV2Readiness("PGVECTOR_HNSW_EF_CONSTRUCTION", cfg.GetPGVectorHNSWEFConstruction(), errV2SchemaIndexNotReady); err != nil {
		return err
	}
	return requirePositiveV2Readiness("PGVECTOR_INDEX_BUILD_MAX_CONCURRENCY", cfg.GetPGVectorIndexBuildMaxConcurrency(), errV2SchemaIndexNotReady)
}

func validateV2QueueReadiness(cfg config.Config) error {
	checks := []struct {
		field string
		value int
	}{
		{"MEMORY_PLACEMENT_LEASE_SECONDS", cfg.GetMemoryPlacementLeaseSeconds()},
		{"MEMORY_PLACEMENT_HEARTBEAT_SECONDS", cfg.GetMemoryPlacementHeartbeatSeconds()},
		{"MEMORY_PLACEMENT_POLL_SECONDS", cfg.GetMemoryPlacementPollSeconds()},
		{"MEMORY_PLACEMENT_MAX_ATTEMPTS", cfg.GetMemoryPlacementMaxAttempts()},
		{"EMBEDDING_BATCH_SIZE", cfg.GetEmbeddingBatchSize()},
		{"EMBEDDING_JOB_LEASE_SECONDS", cfg.GetEmbeddingJobLeaseSeconds()},
		{"EMBEDDING_JOB_POLL_SECONDS", cfg.GetEmbeddingJobPollSeconds()},
		{"EMBEDDING_JOB_MAX_ATTEMPTS", cfg.GetEmbeddingJobMaxAttempts()},
		{"EMBEDDING_JOB_RETRY_MAX_SECONDS", cfg.GetEmbeddingJobRetryMaxSeconds()},
		{"EMBEDDING_PENDING_STALE_SECONDS", cfg.GetEmbeddingPendingStaleSeconds()},
	}
	for _, check := range checks {
		if err := requirePositiveV2Readiness(check.field, check.value, errV2QueueNotReady); err != nil {
			return err
		}
	}
	if cfg.GetMemoryPlacementHeartbeatSeconds() >= cfg.GetMemoryPlacementLeaseSeconds() {
		return wrapV2ReadinessError(
			"MEMORY_PLACEMENT_HEARTBEAT_SECONDS",
			"must be lower than MEMORY_PLACEMENT_LEASE_SECONDS for v2 queue readiness",
			errV2QueueNotReady,
		)
	}
	if cfg.GetEmbeddingJobPollSeconds() >= cfg.GetEmbeddingJobLeaseSeconds() {
		return wrapV2ReadinessError(
			"EMBEDDING_JOB_POLL_SECONDS",
			"must be lower than EMBEDDING_JOB_LEASE_SECONDS for v2 queue readiness",
			errV2QueueNotReady,
		)
	}
	if cfg.GetDistributedCoordinationRequired() && strings.TrimSpace(cfg.GetRedisAddr()) == "" {
		return wrapV2ReadinessError(
			"DISTRIBUTED_COORDINATION_REQUIRED",
			"REDIS_ADDR is required for distributed v2 queue readiness",
			errV2QueueNotReady,
		)
	}
	return nil
}

func requirePositiveV2Readiness(field string, value int, sentinel error) error {
	if value <= 0 {
		return wrapV2ReadinessError(field, fmt.Sprintf("must be greater than 0, got %d", value), sentinel)
	}
	return nil
}

func wrapV2ReadinessError(field string, message string, sentinel error) error {
	return fmt.Errorf("%w: %w", sentinel, &config.ValidationError{Field: field, Message: message})
}

func (b dormantV2Bootstrap) HealthChecks() []internalhttp.HealthCheck {
	return append([]internalhttp.HealthCheck(nil), b.checks...)
}
