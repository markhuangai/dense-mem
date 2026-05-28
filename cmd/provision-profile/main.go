package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/operatorcli"
	"github.com/markhuangai/dense-mem/internal/service"
)

type cliConfig struct {
	name         string
	description  string
	metadataJSON string
	configJSON   string
	rateLimit    int
	expiresAt    string
}

type provisionOutput struct {
	TeamID      string  `json:"team_id"`
	TeamName    string  `json:"team_name"`
	ProfileID   string  `json:"profile_id"`
	ProfileName string  `json:"profile_name"`
	APIKey      string  `json:"api_key"`
	ExpiresAt   *string `json:"expires_at,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	cfg, err := parseCLI(args, stderr)
	if err != nil {
		return err
	}

	metadata, err := parseOptionalJSONObject(cfg.metadataJSON)
	if err != nil {
		return fmt.Errorf("invalid --metadata-json: %w", err)
	}
	configMap, err := parseOptionalJSONObject(cfg.configJSON)
	if err != nil {
		return fmt.Errorf("invalid --config-json: %w", err)
	}

	var expiresAt *time.Time
	if strings.TrimSpace(cfg.expiresAt) != "" {
		t, err := time.Parse(time.RFC3339, cfg.expiresAt)
		if err != nil {
			return fmt.Errorf("invalid --expires-at: must be RFC3339: %w", err)
		}
		expiresAt = &t
	}

	dsn, err := resolvePostgresDSN(os.Getenv)
	if err != nil {
		return err
	}

	logger := operatorcli.NewLogger(stderr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	services, err := operatorcli.OpenServices(ctx, dsn, logger)
	if err != nil {
		return err
	}
	defer services.Close()

	correlationID := operatorcli.CorrelationID()
	team, err := services.ProfileService.Create(ctx, service.CreateProfileRequest{
		Name:        cfg.name,
		Description: cfg.description,
		Metadata:    metadata,
		Config:      configMap,
	}, nil, operatorcli.DefaultActorRole, operatorcli.DefaultClientIP, correlationID)
	if err != nil {
		return fmt.Errorf("create team: %w", err)
	}

	teamProfile, rawKey, err := services.APIKeyService.CreateStandardKey(ctx, team.ID, service.CreateAPIKeyRequest{
		Name:      "default profile",
		RateLimit: cfg.rateLimit,
		ExpiresAt: expiresAt,
	}, nil, operatorcli.DefaultActorRole, operatorcli.DefaultClientIP, correlationID)
	if err != nil {
		cleanupErr := services.ProfileService.Delete(ctx, team.ID, nil, operatorcli.DefaultActorRole, operatorcli.DefaultClientIP, correlationID)
		if cleanupErr != nil {
			return fmt.Errorf("create api key: %w (cleanup failed: %v)", err, cleanupErr)
		}
		return fmt.Errorf("create api key: %w", err)
	}

	var expiresAtStr *string
	if expiresAt != nil {
		formatted := expiresAt.UTC().Format(time.RFC3339)
		expiresAtStr = &formatted
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(provisionOutput{
		TeamID:      team.ID.String(),
		TeamName:    team.Name,
		ProfileID:   teamProfile.ID.String(),
		ProfileName: teamProfile.GetProfileName(),
		APIKey:      rawKey,
		ExpiresAt:   expiresAtStr,
	})
}

func parseCLI(args []string, stderr io.Writer) (cliConfig, error) {
	var cfg cliConfig

	fs := flag.NewFlagSet("provision-team", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.StringVar(&cfg.name, "name", "", "Team name (required)")
	fs.StringVar(&cfg.description, "description", "", "Team description")
	fs.StringVar(&cfg.metadataJSON, "metadata-json", "", "Optional team metadata JSON object")
	fs.StringVar(&cfg.configJSON, "config-json", "", "Optional team config JSON object")
	fs.IntVar(&cfg.rateLimit, "rate-limit", 0, "Per-key rate limit override")
	fs.StringVar(&cfg.expiresAt, "expires-at", "", "Optional RFC3339 expiration for the generated key")

	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}

	cfg.name = strings.TrimSpace(cfg.name)
	cfg.description = strings.TrimSpace(cfg.description)
	cfg.metadataJSON = strings.TrimSpace(cfg.metadataJSON)
	cfg.configJSON = strings.TrimSpace(cfg.configJSON)
	cfg.expiresAt = strings.TrimSpace(cfg.expiresAt)

	if cfg.name == "" {
		return cliConfig{}, errors.New("--name is required")
	}

	return cfg, nil
}

func parseOptionalJSONObject(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, errors.New("must be a JSON object")
	}
	return parsed, nil
}

func resolvePostgresDSN(getenv func(string) string) (string, error) {
	return operatorcli.ResolvePostgresDSN(getenv)
}
