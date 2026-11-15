package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func main() {
	protector := evalharness.NewRunnerCredentialProtector(os.Getenv("DENSE_MEM_EVAL_API_KEY"), os.Getenv("DENSE_MEM_EVAL_EMBED_API_KEY"), os.Getenv("DENSE_MEM_EVAL_VERIFIER_URL"), os.Getenv("DENSE_MEM_EVAL_EMBED_URL"), os.Getenv("AI_VERIFIER_API_URL"), os.Getenv("AI_API_URL"), os.Getenv("EVAL_API_KEY"), os.Getenv("EVAL_CONTROL_TOKEN"), os.Getenv("EVAL_DATABASE_PASSWORD"), os.Getenv("EVAL_DATABASE_URL"))
	logger := observability.NewWithProtector(slog.LevelDebug, protector)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := execute(ctx, os.Args[1:], logger, protector); err != nil {
		logger.ErrorContext(ctx, "evaluation_failed", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, logger *observability.Logger, protector *observability.CredentialProtector) error {
	if len(args) != 2 || (args[0] != "validate" && args[0] != "run" && args[0] != "report") {
		return errors.New("usage: eval-runner validate|run|report EXECUTION_MANIFEST; workflow-internal only")
	}
	manifest, err := evalharness.ReadManifest(args[1])
	if err != nil {
		return err
	}
	registryPath := "tests/eval/datasets.json"
	if manifest.Mode == "fixtures" {
		registryPath = "tests/eval/fixtures/datasets.json"
	}
	if manifest.Mode == "replay" {
		fixtureHash, err := evalharness.FileHash("tests/eval/fixtures/datasets.json")
		if err != nil {
			return err
		}
		if fixtureHash == manifest.RegistryHash {
			registryPath = "tests/eval/fixtures/datasets.json"
		}
	}
	hash, err := evalharness.FileHash(registryPath)
	if err != nil {
		return err
	}
	if hash != manifest.RegistryHash {
		return errors.New("registry digest differs from execution manifest")
	}
	registry, err := evalharness.ReadRegistry(registryPath)
	if err != nil {
		return err
	}
	dataset, err := registry.Dataset(manifest.Dataset)
	if err != nil {
		return err
	}
	corpus, questions, err := evalharness.LoadDataset(dataset, manifest.DataDir)
	if err != nil {
		return err
	}
	if args[0] == "validate" {
		logger.InfoContext(ctx, "evaluation_validated", observability.String("dataset", dataset.ID), observability.Int("corpus_rows", len(corpus)), observability.Int("questions", len(questions)))
		if os.Getenv("EVAL_SERVER_LOG_FILE") != "" {
			return logFile(ctx, os.Getenv("EVAL_SERVER_LOG_FILE"), &evalharness.Capture{Logger: logger.ContextSlog(), Protector: protector, RunID: manifest.RunID})
		}
		return nil
	}
	if args[0] == "report" {
		paths, err := filepath.Glob(filepath.Join(os.Getenv("EVAL_LOGS_DIR"), "*.txt"))
		if err != nil {
			return err
		}
		readers := []io.Reader{}
		for _, path := range paths {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			readers = append(readers, file)
		}
		events, err := evalharness.ReplayEvents(readers, manifest.RunID)
		if err != nil {
			return err
		}
		report, err := evalharness.ReplayReport(events, manifest, corpus, questions)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(manifest.OutputDir, 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(filepath.Join(manifest.OutputDir, "scores.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		return writeReport(file, report)
	}
	if manifest.Mode == "replay" {
		return errors.New("replay cannot start a runtime or call a provider")
	}
	if !manifest.ModelDeadline.After(time.Now()) {
		return errors.New("model deadline is expired")
	}
	for _, name := range []string{"DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_VERIFIER_URL", "EVAL_API_KEY", "EVAL_CONTROL_TOKEN", "EVAL_DATABASE_URL", "EVAL_TEAM_ID", "EVAL_CREDENTIAL_ID"} {
		if os.Getenv(name) == "" {
			return fmt.Errorf("required runner setting missing: %s", name)
		}
	}
	if err := verifyRuntime(ctx, manifest); err != nil {
		return err
	}
	database, err := sql.Open("pgx", os.Getenv("EVAL_DATABASE_URL"))
	if err != nil {
		return errors.New("runner database configuration is invalid")
	}
	defer database.Close()
	database.SetMaxOpenConns(2)
	capture := &evalharness.Capture{Logger: logger.ContextSlog(), Protector: protector, RunID: manifest.RunID}
	client := &evalharness.HTTPClient{BaseURL: os.Getenv("EVAL_MCP_URL"), APIKey: os.Getenv("EVAL_API_KEY"), Capture: capture}
	native := &evalharness.Native{DB: database, Client: client, Scope: evalharness.Scope{TeamID: os.Getenv("EVAL_TEAM_ID"), CredentialID: os.Getenv("EVAL_CREDENTIAL_ID")}, ControlURL: os.Getenv("EVAL_CONTROL_URL"), ControlToken: os.Getenv("EVAL_CONTROL_TOKEN")}
	native.ServerUsage = map[string]evalharness.UsageTotals{}
	native.RuntimeLogs = func(ctx context.Context) error { return runtimeLogs(ctx, manifest.RunID, capture, native.ServerUsage) }
	transport := evalharness.AnswerTransport{URL: os.Getenv("DENSE_MEM_EVAL_VERIFIER_URL"), Key: os.Getenv("DENSE_MEM_EVAL_API_KEY"), Client: &http.Client{Timeout: 5 * time.Minute}, Capture: capture}
	report, err := evalharness.Run(ctx, manifest, corpus, questions, client, transport, native)
	if err != nil {
		return err
	}
	if !report.Complete {
		return errors.New("campaign incomplete; every public denominator is retained")
	}
	return nil
}
