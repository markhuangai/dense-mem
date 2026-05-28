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
)

type cliConfig struct {
	teamID    string
	profileID string
}

type output struct {
	TeamID    string `json:"team_id"`
	ProfileID string `json:"profile_id"`
	Status    string `json:"status"`
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	services, err := operatorcli.OpenServices(ctx, dsn, operatorcli.NewLogger(stderr))
	if err != nil {
		return err
	}
	defer services.Close()

	if err := services.APIKeyService.DeleteForProfile(ctx, teamID, profileID, nil, operatorcli.DefaultActorRole, operatorcli.DefaultClientIP, operatorcli.CorrelationID()); err != nil {
		return fmt.Errorf("delete profile: %w", err)
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output{
		TeamID:    teamID.String(),
		ProfileID: profileID.String(),
		Status:    "deleted",
	})
}

func parseCLI(args []string, stderr io.Writer) (cliConfig, error) {
	var cfg cliConfig

	fs := flag.NewFlagSet("delete-team-profile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.teamID, "team-id", "", "Team UUID that owns the profile")
	fs.StringVar(&cfg.profileID, "profile-id", "", "Profile UUID to delete")

	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	cfg.teamID = strings.TrimSpace(cfg.teamID)
	cfg.profileID = strings.TrimSpace(cfg.profileID)
	if cfg.teamID == "" {
		return cliConfig{}, errors.New("--team-id is required")
	}
	if cfg.profileID == "" {
		return cliConfig{}, errors.New("--profile-id is required")
	}
	return cfg, nil
}
