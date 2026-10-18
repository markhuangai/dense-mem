package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/markhuangai/dense-mem/cmd/internal/migrationapp"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func RunPrivateMemoryPreflight(ctx context.Context, output io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	ctx, cancel := migrationapp.WithTimeout(ctx, time.Duration(cfg.GetPostgresMigrationTimeoutSeconds())*time.Second)
	defer cancel()
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	db, err := postgres.OpenWithClientAndLogger(ctx, &cfg, logger)
	if err != nil {
		return fmt.Errorf("open preflight database: %w", err)
	}
	defer db.Close()
	inspector := privacypostgres.NewPrivateMemoryRepository(db.GetDB(), postgres.NewRLS())
	return writePrivateMemoryPreflight(ctx, inspector, output)
}

func writePrivateMemoryPreflight(ctx context.Context, inspector privacy.PrivateMemoryMigrationInspector, output io.Writer) error {
	report, err := inspector.InspectPrivateMemoryMigration(ctx)
	if report != nil {
		if encodeErr := json.NewEncoder(output).Encode(report); encodeErr != nil {
			return fmt.Errorf("write preflight report: %w", encodeErr)
		}
	}
	return err
}
