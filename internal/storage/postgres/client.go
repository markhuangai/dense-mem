package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/observability"
)

// PostgresClient is the companion interface for any Postgres DB wrapper.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type PostgresClient interface {
	GetDB() *gorm.DB
	Ping(ctx context.Context) error
	Close() error
}

// DB wraps a GORM database connection with configured pool settings.
type DB struct {
	db    *gorm.DB
	sqlDB *sql.DB
}

// Ensure DB implements PostgresClient
var _ PostgresClient = (*DB)(nil)

// GetDB returns the underlying GORM database instance.
func (d *DB) GetDB() *gorm.DB {
	return d.db
}

// Ping verifies the database connection with a 5-second timeout.
func (d *DB) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return d.sqlDB.PingContext(pingCtx)
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.sqlDB.Close()
}

// ConfigProvider defines the configuration needed for Postgres connection.
type ConfigProvider interface {
	GetPostgresDSN() string
}

type poolConfigProvider interface {
	GetPostgresMaxOpenConns() int
	GetPostgresMaxIdleConns() int
	GetPostgresConnMaxLifetimeSeconds() int
}

// Open creates a new GORM/Postgres connection with configured pool settings.
// Returns an error if the connection cannot be established or ping fails.
func Open(ctx context.Context, cfg ConfigProvider) (*gorm.DB, error) {
	return open(ctx, cfg, nil)
}

// OpenWithLogger opens PostgreSQL with the process root as its structured
// query logger. The legacy Open entry point remains console-compatible for
// independent adopters.
func OpenWithLogger(ctx context.Context, cfg ConfigProvider, logger observability.LogProvider) (*gorm.DB, error) {
	return open(ctx, cfg, logger)
}

func open(ctx context.Context, cfg ConfigProvider, logger observability.LogProvider) (*gorm.DB, error) {
	dsn := cfg.GetPostgresDSN()
	if dsn == "" {
		return nil, fmt.Errorf("postgres DSN is empty")
	}

	gormLogger := newGORMLogger(os.Stdout)
	if logger != nil {
		gormLogger = newGORMLoggerWithRoot(logger, postgresSlowQueryThreshold(cfg))
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	maxOpenConns := 25
	maxIdleConns := 10
	connMaxLifetime := 30 * time.Minute
	if poolCfg, ok := cfg.(poolConfigProvider); ok {
		if value := poolCfg.GetPostgresMaxOpenConns(); value > 0 {
			maxOpenConns = value
		}
		if value := poolCfg.GetPostgresMaxIdleConns(); value > 0 {
			maxIdleConns = value
		}
		if value := poolCfg.GetPostgresConnMaxLifetimeSeconds(); value > 0 {
			connMaxLifetime = time.Duration(value) * time.Second
		}
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxIdleConns)
	sqlDB.SetConnMaxLifetime(connMaxLifetime)

	// Ping with 5-second timeout
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}

	return db, nil
}

type slowQueryThresholdProvider interface {
	GetPostgresSlowQueryThresholdMS() int
}

func postgresSlowQueryThreshold(cfg ConfigProvider) time.Duration {
	threshold := 200
	if provider, ok := cfg.(slowQueryThresholdProvider); ok && provider.GetPostgresSlowQueryThresholdMS() > 0 {
		threshold = provider.GetPostgresSlowQueryThresholdMS()
	}
	maxMilliseconds := int64(time.Duration(1<<63-1) / time.Millisecond)
	if int64(threshold) > maxMilliseconds {
		threshold = int(maxMilliseconds)
	}
	return time.Duration(threshold) * time.Millisecond
}

// OpenWithClient creates a new DB wrapper with the configured pool.
// This is the preferred method when you need the PostgresClient interface.
func OpenWithClient(ctx context.Context, cfg ConfigProvider) (*DB, error) {
	return OpenWithClientAndLogger(ctx, cfg, nil)
}

// OpenWithClientAndLogger is the root-aware client constructor used by the
// server composition boundary.
func OpenWithClientAndLogger(ctx context.Context, cfg ConfigProvider, logger observability.LogProvider) (*DB, error) {
	db, err := open(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	return &DB{
		db:    db,
		sqlDB: sqlDB,
	}, nil
}

// OpenOperationLogClient opens a small independent pool for the required
// operation-log sink. Keeping this pool separate prevents SQL diagnostics from
// waiting on application transactions that already occupy the main pool.
func OpenOperationLogClient(ctx context.Context, cfg ConfigProvider, logger observability.LogProvider) (*DB, error) {
	db, err := open(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get operation log sql client: %w", err)
	}
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(2)
	return &DB{db: db, sqlDB: sqlDB}, nil
}
