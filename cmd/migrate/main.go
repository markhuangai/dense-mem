package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/markhuangai/dense-mem/cmd/internal/migrationapp"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const connectionTimeout = 5 * time.Minute

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <up|down|status>\n", os.Args[0])
		os.Exit(1)
	}

	command := os.Args[1]

	// Validate command
	validCommands := map[string]bool{"up": true, "down": true, "status": true}
	if !validCommands[command] {
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		fmt.Fprintf(os.Stderr, "Usage: %s <up|down|status>\n", os.Args[0])
		os.Exit(1)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	logger := observability.New(level)
	slog.SetDefault(logger.Slog())
	migrationTimeout := time.Duration(cfg.GetPostgresMigrationTimeoutSeconds()) * time.Second

	// Execute command
	switch command {
	case "up":
		connectionCtx, connectionCancel := context.WithTimeout(context.Background(), connectionTimeout)
		db, err := postgres.Open(connectionCtx, &cfg)
		connectionCancel()
		if err != nil {
			log.Fatalf("Failed to connect to postgres: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			log.Fatalf("Failed to get underlying sql.DB: %v", err)
		}
		defer sqlDB.Close()
		if err := migrationapp.RunUp(context.Background(), db, migrationTimeout, logger.Slog()); err != nil {
			log.Fatalf("Failed to run up migrations: %v", err)
		}

	case "down":
		connectionCtx, connectionCancel := context.WithTimeout(context.Background(), connectionTimeout)
		db, err := postgres.Open(connectionCtx, &cfg)
		connectionCancel()
		if err != nil {
			log.Fatalf("Failed to connect to postgres: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			log.Fatalf("Failed to get underlying sql.DB: %v", err)
		}
		defer sqlDB.Close()
		if err := migrationapp.RunDown(context.Background(), db, migrationTimeout, logger.Slog()); err != nil {
			log.Fatalf("Failed to run down migrations: %v", err)
		}

	case "status":
		fmt.Println("Migration status:")
		statusCtx, statusCancel := context.WithTimeout(context.Background(), connectionTimeout)
		defer statusCancel()
		db, err := postgres.Open(statusCtx, &cfg)
		if err != nil {
			log.Fatalf("Failed to connect to postgres: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			log.Fatalf("Failed to get underlying sql.DB: %v", err)
		}
		defer sqlDB.Close()
		if err := postgres.RunStatus(statusCtx, db); err != nil {
			log.Fatalf("Failed to get migration status: %v", err)
		}
	}
}
