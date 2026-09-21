package serverapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

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

// RunMigrationControlRetirement runs the explicitly authorized destructive
// migration without starting the application runtime or ordinary migrations.
func RunMigrationControlRetirement(processCtx context.Context) error {
	if processCtx == nil {
		processCtx = context.Background()
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	level, err := observability.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return fmt.Errorf("parse log level: %w", err)
	}
	logger, err := newRootLogger(cfg, level)
	if err != nil {
		return fmt.Errorf("configure root logger: %w", err)
	}
	retirementTimeout := time.Duration(cfg.GetPostgresMigrationTimeoutSeconds()) * time.Second
	retirementCtx, cancel := context.WithTimeout(processCtx, retirementTimeout)
	defer cancel()
	pgDB, err := postgres.OpenWithClientAndLogger(retirementCtx, &cfg, logger)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pgDB.Close()
	if err := postgres.ValidateSinglePrimaryTopology(retirementCtx, pgDB.GetDB()); err != nil {
		return fmt.Errorf("validate postgres topology: %w", err)
	}
	if err := migrationapp.RunMigrationControlRetirement(retirementCtx, pgDB.GetDB(), retirementTimeout, logger.Slog()); err != nil {
		logMigrationFailure(retirementCtx, logger, "migration-control-retirement", err)
		return fmt.Errorf("run migration-control retirement: %w", err)
	}
	return nil
}

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

	level := cfg.GetLogLevel()
	logger, err := newRootLogger(cfg, level)
	if err != nil {
		return fmt.Errorf("configure root logger: %w", err)
	}

	startupCtx, startupCancel := context.WithTimeout(processCtx, DefaultStartupTimeout)
	defer startupCancel()
	pgDB, err := postgres.OpenWithClientAndLogger(startupCtx, &cfg, logger)
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
		logMigrationFailure(migrationCtx, logger, "up", err)
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

func logMigrationFailure(ctx context.Context, logger *observability.Logger, direction string, err error) {
	if logger == nil || err == nil {
		return
	}
	logger.ErrorContext(context.WithoutCancel(ctx), "postgres migrations failed", err, observability.String("direction", direction))
}

func newRootLogger(cfg config.Config, level slog.Level) (*observability.Logger, error) {
	protector, err := newRootProtector(cfg)
	if err != nil {
		return nil, err
	}
	return observability.NewWithProtector(level, protector), nil
}

func newRootProtector(cfg config.Config) (*observability.CredentialProtector, error) {
	postgresConfig, err := pgconn.ParseConfig(cfg.PostgresDSN)
	if err != nil {
		return nil, &config.ValidationError{Field: "POSTGRES_DSN", Message: "invalid connection configuration"}
	}
	return observability.NewCredentialProtector(
		cfg.PostgresDSN,
		postgresConfig.Password,
		cfg.RedisPassword,
		cfg.AIAPIKey,
		cfg.AIVerifierAPIKey,
		cfg.ControlPortalToken,
		cfg.TelemetryScrapeToken,
	), nil
}
