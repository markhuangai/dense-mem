package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func resolveStringEnvironment(flags *flag.FlagSet, name, variable string, target *string) {
	explicit := false
	flags.Visit(func(value *flag.Flag) {
		if value.Name == name {
			explicit = true
		}
	})
	if !explicit {
		*target = env(variable, *target)
	}
}

func runTokenDiagnostics(ctx context.Context, metricsURL, out, apiKey string) error {
	cfg, err := config.Load()
	if err != nil {
		var validation *config.ValidationError
		if errors.As(err, &validation) {
			return fmt.Errorf("token diagnostics: invalid runtime configuration; check %s", validation.Field)
		}
		return errors.New("token diagnostics: invalid runtime configuration")
	}
	secrets, err := cfg.OperationalSecrets()
	if err != nil {
		return errors.New("token diagnostics: invalid operational credential configuration; check POSTGRES_DSN")
	}
	secrets = append(secrets, apiKey, os.Getenv("DENSE_MEM_API_KEY"), os.Getenv("DENSE_MEM_CONTROL_TOKEN"))
	body, err := evalharness.CollectTokenDiagnostics(ctx, evalharness.TokenDiagnosticsOptions{
		MetricsURL: metricsURL, ScrapeToken: cfg.TelemetryScrapeToken, Protector: observability.NewCredentialProtector(secrets...),
	})
	if err != nil {
		return err
	}
	if out == "" {
		out = "tests/eval/runs/" + time.Now().UTC().Format("20060102T150405Z") + "_token-diagnostics"
	}
	return writeTokenDiagnostics(out, body)
}

func writeTokenDiagnostics(out string, body []byte) error {
	if err := os.MkdirAll(out, 0o700); err != nil {
		return errors.New("token diagnostics: cannot create output directory")
	}
	path := filepath.Join(out, "token_usage.json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return errors.New("token diagnostics: output already exists; choose a new output directory")
	}
	if err != nil {
		return errors.New("token diagnostics: cannot create output file")
	}
	_, writeErr := file.Write(body)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		if err := os.Remove(path); err != nil {
			return errors.New("token diagnostics: output write failed and incomplete file could not be removed")
		}
		return errors.New("token diagnostics: output write failed")
	}
	return nil
}
