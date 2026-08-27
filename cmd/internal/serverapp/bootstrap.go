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
		log.Fatal("failed to load config")
	}
	if err := cfg.ValidateServerStartup(); err != nil {
		log.Fatal("invalid startup config")
	}

	level, err := observability.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		log.Fatal("invalid log level")
	}
	logger := observability.New(level)
	slog.SetDefault(logger.Slog())

	preflightCtx, preflightCancel := context.WithTimeout(context.Background(), DefaultStartupTimeout)

	pgDB, err := postgres.OpenWithClient(preflightCtx, &cfg)
	if err != nil {
		log.Fatal("failed to connect to postgres")
	}
	defer pgDB.Close()
	if err := postgres.ValidateSinglePrimaryTopology(preflightCtx, pgDB.GetDB()); err != nil {
		log.Fatal("unsupported postgres topology")
	}
	preflightCancel()

	migrationTimeout := time.Duration(cfg.GetPostgresMigrationTimeoutSeconds()) * time.Second
	if err := migrationapp.RunUp(context.Background(), pgDB.GetDB(), migrationTimeout, logger.Slog()); err != nil {
		log.Fatal("failed to run postgres migrations")
	}
	sqlDB, err := pgDB.GetDB().DB()
	if err != nil {
		log.Fatal("failed to access postgres sql client")
	}
	migrationStateCtx, migrationStateCancel := context.WithTimeout(context.Background(), DefaultStartupTimeout)
	if err := postgres.ValidateStartupMigrationState(migrationStateCtx, sqlDB, postgres.MigrationsDir()); err != nil {
		migrationStateCancel()
		log.Fatal("postgres migration state validation failed")
	}
	migrationStateCancel()

	postMigrationCtx, postMigrationCancel := context.WithTimeout(context.Background(), DefaultStartupTimeout)
	defer postMigrationCancel()
	if err := postgres.CheckPGVectorExtension(postMigrationCtx, pgDB.GetDB()); err != nil {
		log.Fatal("pgvector extension check failed")
	}

	rlsHelper := postgres.NewRLS()
	authorityRepo := repository.NewAuthorityRepository(pgDB.GetDB(), rlsHelper)
	authority, err := EnsureAuthority(postMigrationCtx, authorityRepo)
	if err != nil {
		log.Fatal("authority bootstrap failed")
	}
	RunActiveServer(postMigrationCtx, cfg, pgDB, logger, level, authority, options)
}
