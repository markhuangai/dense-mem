package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/markhuangai/dense-mem/cmd/internal/migrationapp"
	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const startupTimeout = 5 * time.Minute

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	if err := cfg.ValidateServerStartup(); err != nil {
		log.Fatalf("invalid startup config: %v", err)
	}

	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	logger := observability.New(level)
	slog.SetDefault(logger.Slog())

	validation.SetEmbeddingDimensions(cfg.GetEmbeddingDimensions())
	middleware.SetAuthVerificationConcurrency(cfg.AuthVerifyMaxConcurrency)

	preflightCtx, preflightCancel := context.WithTimeout(context.Background(), startupTimeout)

	pgDB, err := postgres.OpenWithClient(preflightCtx, &cfg)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	defer pgDB.Close()
	if err := postgres.ValidateSinglePrimaryTopology(preflightCtx, pgDB.GetDB()); err != nil {
		log.Fatalf("unsupported postgres topology: %v", err)
	}
	preflightCancel()

	migrationTimeout := time.Duration(cfg.GetPostgresMigrationTimeoutSeconds()) * time.Second
	if err := migrationapp.RunUp(context.Background(), pgDB.GetDB(), migrationTimeout, logger.Slog()); err != nil {
		log.Fatalf("failed to run postgres migrations: %v", err)
	}

	postMigrationCtx, postMigrationCancel := context.WithTimeout(context.Background(), startupTimeout)
	defer postMigrationCancel()
	if err := postgres.CheckPGVectorExtension(postMigrationCtx, pgDB.GetDB()); err != nil {
		log.Fatalf("pgvector extension check failed: %v", err)
	}

	embeddingConfigRepo := postgres.NewEmbeddingConfigRepository(pgDB.GetDB())
	embeddingConsistencySvc := service.NewEmbeddingConsistencyService(embeddingConfigRepo, &cfg)
	if err := embeddingConsistencySvc.CheckAtStartup(postMigrationCtx); err != nil {
		log.Fatalf("embedding consistency check failed: %v", err)
	}

	rlsHelper := postgres.NewRLS()
	authorityRepo := repository.NewAuthorityRepository(pgDB.GetDB(), rlsHelper)
	authority, err := serverapp.EnsureAuthority(postMigrationCtx, authorityRepo)
	if err != nil {
		log.Fatalf("authority bootstrap failed: %v", err)
	}
	serverapp.RunActiveServer(postMigrationCtx, cfg, pgDB, logger, level, authority, serverapp.RuntimeOptions{})
}
