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
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	operationspostgres "github.com/markhuangai/dense-mem/internal/operations/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var errAuthorityBlocked = operations.ErrAuthorityBlocked

const cutoverMarkerVersion = operations.CutoverMarkerVersion

type authorityMode = operations.AuthorityMode

const authorityActive = operations.AuthorityActive

type authorityBootstrap = operations.AuthorityBootstrap
type authorityBootstrapStore = operationscontract.AuthorityReader

func ClassifyAuthority(ctx context.Context, store authorityBootstrapStore) (authorityBootstrap, error) {
	return operations.ClassifyAuthority(ctx, store)
}

func checkActiveAuthority(authority authorityBootstrap) error {
	return operations.CheckActiveAuthority(authority)
}

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
	migrationCtx, migrationCancel := context.WithTimeout(processCtx, migrationTimeout)
	if err := migrationapp.RunUp(migrationCtx, pgDB.GetDB(), migrationTimeout, logger.Slog()); err != nil {
		migrationCancel()
		return fmt.Errorf("run postgres migrations: %w", err)
	}
	migrationCancel()
	postMigrationCtx, postMigrationCancel := context.WithTimeout(processCtx, DefaultStartupTimeout)
	defer postMigrationCancel()
	sqlDB, err := pgDB.GetDB().DB()
	if err != nil {
		return fmt.Errorf("access postgres sql client: %w", err)
	}
	if err := postgres.ValidateStartupMigrationState(postMigrationCtx, sqlDB, postgres.MigrationsDir()); err != nil {
		return fmt.Errorf("validate postgres migration state: %w", err)
	}
	if err := postgres.CheckPGVectorExtension(postMigrationCtx, pgDB.GetDB()); err != nil {
		return fmt.Errorf("check pgvector extension: %w", err)
	}

	rlsHelper := postgres.NewRLS()
	authorityRepo := operationspostgres.NewAuthorityRepository(pgDB.GetDB(), rlsHelper)
	authority, err := ClassifyAuthority(postMigrationCtx, authorityRepo)
	if err != nil {
		return fmt.Errorf("bootstrap authority: %w", err)
	}
	if err := RunActiveServer(processCtx, postMigrationCtx, cfg, pgDB, logger, level, authority, options); err != nil {
		if errors.Is(err, ErrRuntimeShutdownTimeout) {
			// A live worker may still be using PostgreSQL. Leave the adapter open
			// for the terminating process instead of closing it underneath work.
			closePostgres = false
		}
		return fmt.Errorf("active server runtime: %w", err)
	}
	return nil
}
