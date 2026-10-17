package serverapp

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/cmd/internal/migrationapp"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func RunPrivateMemoryMigration(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("private-memory-migrate", flag.ContinueOnError)
	fingerprint := flags.String("source-fingerprint", "", "optional source_fingerprint constraint from preflight")
	flags.Bool("maintenance-confirmed", false, "deprecated; migration runs automatically before server startup")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*fingerprint != "" && (!strings.HasPrefix(*fingerprint, "sha256:") || len(*fingerprint) != 71)) {
		return fmt.Errorf("private-memory-migrate accepts only an optional --source-fingerprint=sha256:<64-character hash>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := migrationapp.WithTimeout(ctx, time.Duration(cfg.GetPostgresMigrationTimeoutSeconds())*time.Second)
	defer cancel()
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	db, err := postgres.OpenWithClientAndLogger(ctx, &cfg, logger)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := migrationapp.RunPrivateMemoryMigration(ctx, db.GetDB(), *fingerprint); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, `{"planner_version":"private-memory.v2.7.0.1","status":"complete"}`)
	return err
}
