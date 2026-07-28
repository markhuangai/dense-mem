package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
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
	dsn := cfg.GetPostgresDSN()
	if dsn == "" {
		return nil, fmt.Errorf("postgres DSN is empty")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: newGORMLogger(os.Stdout),
	})
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

// OpenWithClient creates a new DB wrapper with the configured pool.
// This is the preferred method when you need the PostgresClient interface.
func OpenWithClient(ctx context.Context, cfg ConfigProvider) (*DB, error) {
	db, err := Open(ctx, cfg)
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

func newGORMLogger(output io.Writer) gormlogger.Interface {
	return gormlogger.New(
		log.New(output, "\r\n", log.LstdFlags),
		gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: false,
			ParameterizedQueries:      true,
			Colorful:                  false,
		},
	)
}
