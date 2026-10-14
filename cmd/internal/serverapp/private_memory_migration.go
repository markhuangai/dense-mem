package serverapp

import (
	"context"
	"errors"
	"flag"
	"fmt"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func RunPrivateMemoryMigration(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("private-memory-migrate", flag.ContinueOnError)
	fingerprint := flags.String("source-fingerprint", "", "source_fingerprint from the stopped-state preflight")
	maintenance := flags.Bool("maintenance-confirmed", false, "requests and workers are stopped and a restorable backup is verified")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*maintenance || len(*fingerprint) != 71 {
		return fmt.Errorf("private-memory-migrate requires --maintenance-confirmed and --source-fingerprint from preflight")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.GetPostgresMigrationTimeoutSeconds())*time.Second)
	defer cancel()
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	db, err := postgres.OpenWithClientAndLogger(ctx, &cfg, logger)
	if err != nil {
		return err
	}
	defer db.Close()
	repository := privacypostgres.NewPrivateMemoryRepository(db.GetDB(), postgres.NewRLS())
	report, err := repository.InspectPrivateMemoryMigration(ctx)
	if err != nil && (!errors.Is(err, privacy.ErrPrivateMemoryMigrationBlocked) || report == nil || report.Status == "pending") {
		return err
	}
	if report == nil || report.SourceFingerprint != *fingerprint {
		return fmt.Errorf("%w: source fingerprint changed; rerun stopped-state preflight", privacy.ErrPrivateMemoryMigrationBlocked)
	}
	migrator, err := postgres.NewMigrator(db.GetDB())
	if err != nil {
		return err
	}
	if err := migrator.PreparePrivateMemoryMigration(ctx); err != nil {
		return err
	}
	if err := repository.MigratePrivateMemory(ctx, *fingerprint); err != nil {
		return err
	}
	if err := migrator.RunUp(ctx); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, `{"planner_version":"private-memory.v2.7.0.1","status":"complete"}`)
	return err
}
