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

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/operatorcli"
	"github.com/markhuangai/dense-mem/internal/service"
)

type cliConfig struct {
	teamID    string
	profileID string
	expiresAt string
}

type output struct {
	TeamID    string   `json:"team_id"`
	ProfileID string   `json:"profile_id"`
	Scopes    []string `json:"scopes"`
	APIKey    string   `json:"api_key"`
	ExpiresAt *string  `json:"expires_at,omitempty"`
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

	teamID, err := uuid.Parse(cfg.teamID)
	if err != nil {
		return fmt.Errorf("invalid --team-id: %w", err)
	}
	profileID, err := uuid.Parse(cfg.profileID)
	if err != nil {
		return fmt.Errorf("invalid --profile-id: %w", err)
	}

	dsn, err := operatorcli.ResolvePostgresDSN(os.Getenv)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	services, err := operatorcli.OpenServices(ctx, dsn, operatorcli.NewLogger(stderr))
	if err != nil {
		return err
	}
	defer services.Close()

	existing, err := services.APIKeyService.GetByIDForProfile(ctx, teamID, profileID)
	if err != nil {
		return fmt.Errorf("load existing profile: %w", err)
	}

	expiresAt := existing.ExpiresAt
	if cfg.expiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, cfg.expiresAt)
		if err != nil {
			return fmt.Errorf("invalid --expires-at: must be RFC3339: %w", err)
		}
		expiresAt = &parsed
	}

	correlationID := operatorcli.CorrelationID()
	rotated, rawKey, err := services.APIKeyService.RotateForProfile(ctx, teamID, profileID, service.CreateAPIKeyRequest{
		Name:      existing.GetProfileName(),
		RateLimit: existing.RateLimit,
		ExpiresAt: expiresAt,
	}, nil, operatorcli.DefaultActorRole, operatorcli.DefaultClientIP, correlationID)
	if err != nil {
		return fmt.Errorf("rotate key: %w", err)
	}

	var expiresAtStr *string
	if expiresAt != nil {
		formatted := expiresAt.UTC().Format(time.RFC3339)
		expiresAtStr = &formatted
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output{
		TeamID:    teamID.String(),
		ProfileID: rotated.ID.String(),
		Scopes:    append([]string(nil), rotated.Scopes...),
		APIKey:    rawKey,
		ExpiresAt: expiresAtStr,
	})
}

func parseCLI(args []string, stderr io.Writer) (cliConfig, error) {
	var cfg cliConfig

	fs := flag.NewFlagSet("rotate-team-profile-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.teamID, "team-id", "", "Team UUID that owns the profile")
	fs.StringVar(&cfg.profileID, "profile-id", "", "Profile UUID to rotate")
	fs.StringVar(&cfg.expiresAt, "expires-at", "", "Optional RFC3339 expiration override for the replacement key")

	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	cfg.teamID = strings.TrimSpace(cfg.teamID)
	cfg.profileID = strings.TrimSpace(cfg.profileID)
	cfg.expiresAt = strings.TrimSpace(cfg.expiresAt)

	if cfg.teamID == "" {
		return cliConfig{}, errors.New("--team-id is required")
	}
	if cfg.profileID == "" {
		return cliConfig{}, errors.New("--profile-id is required")
	}

	return cfg, nil
}
