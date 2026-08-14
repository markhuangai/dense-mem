package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"
)

func main() {
	var dsn, migrationsDir string
	flag.StringVar(&dsn, "dsn", "", "PostgreSQL DSN")
	flag.StringVar(&migrationsDir, "migrations", "migrations/postgres", "Goose migration directory")
	flag.Parse()
	if dsn == "" {
		log.Fatal("-dsn is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("ping postgres: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS(migrationsDir))
	if err != nil {
		log.Fatalf("create goose provider: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		log.Fatalf("apply migrations: %v", err)
	}
	fmt.Println("migrations_applied=true")
}
