package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const migrationTimeout = 5 * time.Minute

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <up|down|status|backfill-neo4j>\n", os.Args[0])
		os.Exit(1)
	}

	command := os.Args[1]

	// Validate command
	validCommands := map[string]bool{"up": true, "down": true, "status": true, "backfill-neo4j": true}
	if !validCommands[command] {
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		fmt.Fprintf(os.Stderr, "Usage: %s <up|down|status|backfill-neo4j>\n", os.Args[0])
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

	case "backfill-neo4j":
		// Neo4j content_hash backfill (AC-43)
		// Batch-safe backfill for existing fragments; no single monolithic transaction.
		fmt.Println("Running Neo4j content_hash backfill...")
		neo4jClient, err := neo4j.NewClient(ctx, &cfg)
		if err != nil {
			log.Fatalf("Failed to connect to Neo4j: %v", err)
		}
		defer neo4jClient.Close(ctx)

		// Ensure schema (idempotent) before backfill
		logger := observability.New(slog.LevelInfo)
		schemaBootstrapper := neo4j.NewSchemaBootstrapper(neo4jClient, cfg.GetEmbeddingDimensions(), logger)
		if err := schemaBootstrapper.EnsureSchema(ctx); err != nil {
			log.Fatalf("Failed to ensure Neo4j schema: %v", err)
		}

		// Run backfill with default batch size of 100
		migrationRunner := neo4j.NewFragmentMigrationRunner(neo4jClient, logger)
		processed, err := migrationRunner.BackfillContentHashes(ctx, 100)
		if err != nil {
			log.Fatalf("Failed to backfill content_hash: %v", err)
		}
		fmt.Printf("Backfill completed successfully. Processed %d fragments.\n", processed)
	}
}
