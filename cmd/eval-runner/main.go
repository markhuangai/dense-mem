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
	console, closeConsole, err := evaluationConsole()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "evaluation console initialization failed")
		os.Exit(1)
	}
	defer closeConsole()
	logger := observability.NewConsoleWithProtector(slog.LevelDebug, protector, console)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := execute(ctx, os.Args[1:], logger, protector, console); err != nil {
		logger.ErrorContext(ctx, "evaluation_failed", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, logger *observability.Logger, protector *observability.CredentialProtector, console io.Writer) error {
	if len(args) == 2 && args[0] == "freeze-intake-proof" {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			return errors.New("intake proof preparation requires local Docker")
		}
		return evalharness.FreezeIntakeProofInputs("tests/eval/datasets.json", args[1])
	}
	if len(args) != 2 || (args[0] != "validate" && args[0] != "prepare" && args[0] != "run" && args[0] != "bootstrap" && args[0] != "verify-seed" && args[0] != "recover") {
		return errors.New("usage: eval-runner validate|bootstrap|prepare|verify-seed|run|recover EXECUTION_MANIFEST; workflow-internal only except local seed preparation")
	}
	if args[0] == "run" || args[0] == "recover" {
		if err := evalharness.RequireRunner(); err != nil {
			return err
		}
	}
	manifest, err := evalharness.ReadManifest(args[1])
	if err != nil {
		return err
	}
	if args[0] == "recover" {
		root := filepath.Join(os.Getenv("RUNNER_TEMP"), "dense-mem-eval-"+manifest.RunID)
		if manifest.Operation != "run" || !filepath.IsAbs(root) || filepath.Clean(args[1]) != filepath.Join(root, "manifest.json") ||
			manifest.OutputDir != filepath.Join(root, "public") || manifest.DataDir != filepath.Join(root, "data") {
			return errors.New("score recovery requires the current hosted dataset directory")
		}
	} else if args[0] != "validate" && args[0] != manifest.Operation {
		return errors.New("command differs from the workflow manifest operation")
	}
	if args[0] == "bootstrap" {
		if err := verifyRuntime(ctx, manifest); err != nil {
			return err
		}
		return bootstrapCredential(ctx, manifest)
	}
	registryPath := "tests/eval/datasets.json"
	if manifest.Mode == "fixtures" {
		registryPath = "tests/eval/fixtures/datasets.json"
	}
	hash, err := evalharness.FileHash(registryPath)
	if err != nil {
		return err
	}
	if hash != manifest.RegistryHash {
		return errors.New("registry digest differs from execution manifest")
	}
	if args[0] == "validate" && os.Getenv("EVAL_SERVER_LOG_FILE") != "" {
		return logFile(ctx, os.Getenv("EVAL_SERVER_LOG_FILE"), evalharness.NewConsoleCapture(console, logger.ContextSlog(), protector, manifest.RunID))
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
	if args[0] == "recover" {
		report, saved, err := evalharness.RecoverScores(manifest, len(corpus), questions)
		if saved {
			logger.WarnContext(ctx, "eval_scores_recovered", observability.String("run_id", manifest.RunID),
				observability.String("dataset", manifest.Dataset), observability.Int("completed_answers", report.CompletedAnswers),
				observability.Int("expected_answers", report.ExpectedAnswers), observability.String("failure", report.Failure))
		}
		return err
	}
	if manifest.IntakeProof {
		corpus, err = evalharness.ReadIntakeProofCorpus(manifest, registry.Revision, dataset, corpus)
		if err != nil {
			return err
		}
	}
	if args[0] == "validate" {
		if manifest.SkipAISecurityReview {
			directory := filepath.Dir(args[1])
			if err := evalharness.WriteSeedAdmission(filepath.Join(directory, "seed-admission.json"), filepath.Join(directory, "seed-admission-scope.json"), corpus); err != nil {
				return err
			}
		}
		logger.InfoContext(ctx, "evaluation_validated", observability.String("dataset", dataset.ID), observability.Int("corpus_rows", len(corpus)), observability.Int("questions", len(questions)))
		return nil
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
	capture := evalharness.NewConsoleCapture(console, logger.ContextSlog(), protector, manifest.RunID)
	if manifest.Operation != "run" {
		capture.RunID += "-" + manifest.Operation
	}
	client := &evalharness.HTTPClient{BaseURL: os.Getenv("EVAL_MCP_URL"), APIKey: os.Getenv("EVAL_API_KEY"), Capture: capture}
	native := &evalharness.Native{DB: database, Client: client, Scope: evalharness.Scope{TeamID: os.Getenv("EVAL_TEAM_ID"), CredentialID: os.Getenv("EVAL_CREDENTIAL_ID")}, ControlURL: os.Getenv("EVAL_CONTROL_URL"), ControlToken: os.Getenv("EVAL_CONTROL_TOKEN")}
	native.ServerUsage = map[string]evalharness.UsageTotals{}
	native.RuntimeLogs = func(ctx context.Context) error {
		requireUsage := manifest.ResumeScope == nil
		if manifest.Operation == "run" {
			requireUsage = native.RequireRuntimeUsage
		}
		return runtimeLogs(ctx, manifest.RunID, capture, native.ServerUsage, requireUsage, manifest.Operation == "run")
	}
	if manifest.Operation == "run" {
		native.StopRuntime = func(ctx context.Context, cutoff bool) error {
			return stopRuntime(ctx, manifest.RunID, cutoff)
		}
	}
	if args[0] == "prepare" {
		prepareCtx, stop := context.WithCancel(ctx)
		defer stop()
		finishResources := startResourceMonitor(prepareCtx, stop, database, manifest, capture)
		var prepareErr error
		if manifest.IntakeProof && !manifest.SkipAISecurityReview {
			prepareErr = evalharness.ObserveOrdinaryIntake(prepareCtx, manifest, corpus, client, native)
		} else {
			_, prepareErr = evalharness.Prepare(prepareCtx, manifest, corpus, client, native)
		}
		prepareErr = errors.Join(prepareErr, finishResources())
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		return errors.Join(prepareErr, native.RuntimeLogs(shutdown))
	}
	if args[0] == "verify-seed" {
		_, _, _, _, err := evalharness.VerifySeed(ctx, manifest, corpus, client, native)
		return err
	}
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
