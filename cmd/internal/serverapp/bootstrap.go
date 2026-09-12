package serverapp

import (
	"context"
	"errors"
	"fmt"
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
// active server. The command owns process cancellation; this function owns
// bounded startup and database cleanup.
func RunFromEnvironment(processCtx context.Context, options RuntimeOptions) error {
	if processCtx == nil {
		processCtx = context.Background()
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	validateStartup := options.ValidateStartup
	if validateStartup == nil {
		validateStartup = func(cfg *config.Config) error { return cfg.ValidateServerStartup() }
	}
	if err := validateStartup(&cfg); err != nil {
		return fmt.Errorf("validate startup config: %w", err)
	}

	level, err := observability.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return fmt.Errorf("parse log level: %w", err)
	}
	logger := observability.New(level)
	slog.SetDefault(logger.Slog())

	startupCtx, startupCancel := context.WithTimeout(processCtx, DefaultStartupTimeout)
	defer startupCancel()
	pgDB, err := postgres.OpenWithClient(startupCtx, &cfg)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	closePostgres := true
	defer func() {
		if closePostgres {
			_ = pgDB.Close()
		}
	}()
	if err := postgres.ValidateSinglePrimaryTopology(startupCtx, pgDB.GetDB()); err != nil {
		return fmt.Errorf("validate postgres topology: %w", err)
	}

	migrationTimeout := time.Duration(cfg.GetPostgresMigrationTimeoutSeconds()) * time.Second
	if err := migrationapp.RunUp(startupCtx, pgDB.GetDB(), migrationTimeout, logger.Slog()); err != nil {
		return fmt.Errorf("run postgres migrations: %w", err)
	}
	sqlDB, err := pgDB.GetDB().DB()
	if err != nil {
		return fmt.Errorf("access postgres sql client: %w", err)
	}
	if err := postgres.ValidateStartupMigrationState(startupCtx, sqlDB, postgres.MigrationsDir()); err != nil {
		return fmt.Errorf("validate postgres migration state: %w", err)
	}
	if err := postgres.CheckPGVectorExtension(startupCtx, pgDB.GetDB()); err != nil {
		return fmt.Errorf("check pgvector extension: %w", err)
	}

	rlsHelper := postgres.NewRLS()
	authorityRepo := repository.NewAuthorityRepository(pgDB.GetDB(), rlsHelper)
	authority, err := ClassifyAuthority(startupCtx, authorityRepo)
	if err != nil {
		return fmt.Errorf("bootstrap authority: %w", err)
	}
	if err := RunActiveServer(processCtx, startupCtx, cfg, pgDB, logger, level, authority, options); err != nil {
		if errors.Is(err, ErrRuntimeShutdownTimeout) {
			// A live worker may still be using PostgreSQL. Leave the adapter open
			// for the terminating process instead of closing it underneath work.
			closePostgres = false
		}
		return fmt.Errorf("active server runtime: %w", err)
	}
	return nil
}
