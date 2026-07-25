package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const migrationTimeout = 5 * time.Minute

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

	ctx, cancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer cancel()

	// Execute command
	switch command {
	case "up":
		fmt.Println("Running migrations up...")
		db, err := postgres.Open(ctx, &cfg)
		if err != nil {
			log.Fatalf("Failed to connect to postgres: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			log.Fatalf("Failed to get underlying sql.DB: %v", err)
		}
		defer sqlDB.Close()
		if err := postgres.RunUp(ctx, db); err != nil {
			log.Fatalf("Failed to run up migrations: %v", err)
		}
		fmt.Println("Migrations completed successfully")

	case "down":
		fmt.Println("Running migrations down...")
		db, err := postgres.Open(ctx, &cfg)
		if err != nil {
			log.Fatalf("Failed to connect to postgres: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			log.Fatalf("Failed to get underlying sql.DB: %v", err)
		}
		defer sqlDB.Close()
		if err := postgres.RunDown(ctx, db); err != nil {
			log.Fatalf("Failed to run down migrations: %v", err)
		}
		fmt.Println("Rollback completed successfully")

	case "status":
		fmt.Println("Migration status:")
		db, err := postgres.Open(ctx, &cfg)
		if err != nil {
			log.Fatalf("Failed to connect to postgres: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			log.Fatalf("Failed to get underlying sql.DB: %v", err)
		}
		defer sqlDB.Close()
		if err := postgres.RunStatus(ctx, db); err != nil {
			log.Fatalf("Failed to get migration status: %v", err)
		}
	}
}
