package serverapp

import (
	"context"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/markhuangai/dense-mem/cmd/internal/migrationapp"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const DefaultStartupTimeout = 5 * time.Minute

// RunFromEnvironment performs the common release bootstrap and starts the
// active server. RuntimeOptions is the only supported composition seam; the
// release command passes zero options and therefore cannot select a test slice.
func RunFromEnvironment(options RuntimeOptions) {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	if err := cfg.ValidateServerStartup(); err != nil {
		log.Fatalf("invalid startup config: %v", err)
	}

	level, err := observability.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		log.Fatal(err)
	}
	logger := observability.New(level)
	slog.SetDefault(logger.Slog())

	preflightCtx, preflightCancel := context.WithTimeout(context.Background(), DefaultStartupTimeout)

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
	sqlDB, err := pgDB.GetDB().DB()
	if err != nil {
		log.Fatalf("failed to access postgres sql client: %v", err)
	}
	migrationStateCtx, migrationStateCancel := context.WithTimeout(context.Background(), DefaultStartupTimeout)
	if err := postgres.ValidateStartupMigrationState(migrationStateCtx, sqlDB, postgres.MigrationsDir()); err != nil {
		migrationStateCancel()
		log.Fatalf("postgres migration state validation failed: %v", err)
	}
	migrationStateCancel()

	postMigrationCtx, postMigrationCancel := context.WithTimeout(context.Background(), DefaultStartupTimeout)
	defer postMigrationCancel()
	if err := postgres.CheckPGVectorExtension(postMigrationCtx, pgDB.GetDB()); err != nil {
		log.Fatalf("pgvector extension check failed: %v", err)
	}

	rlsHelper := postgres.NewRLS()
	authorityRepo := repository.NewAuthorityRepository(pgDB.GetDB(), rlsHelper)
	authority, err := EnsureAuthority(postMigrationCtx, authorityRepo)
	if err != nil {
		log.Fatalf("authority bootstrap failed: %v", err)
	}
	RunActiveServer(postMigrationCtx, cfg, pgDB, logger, level, authority, options)
}
