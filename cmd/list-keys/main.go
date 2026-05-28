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
	teamID string
	limit  int
	offset int
}

type keyItem struct {
	ProfileID  string     `json:"profile_id"`
	Name       string     `json:"name"`
	KeySuffix  string     `json:"key_suffix"`
	RateLimit  int        `json:"rate_limit"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type output struct {
	TeamID string    `json:"team_id"`
	Items  []keyItem `json:"items"`
	Total  int64     `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
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

	keys, err := services.APIKeyService.ListByProfile(ctx, teamID, cfg.limit, cfg.offset)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}
	total, err := services.APIKeyService.CountByProfile(ctx, teamID)
	if err != nil {
		return fmt.Errorf("count profiles: %w", err)
	}

	items := make([]keyItem, 0, len(keys))
	for _, key := range keys {
		items = append(items, keyItem{
			ProfileID:  key.ID.String(),
			Name:       key.GetProfileName(),
			KeySuffix:  key.KeySuffix,
			RateLimit:  key.RateLimit,
			LastUsedAt: key.LastUsedAt,
			ExpiresAt:  key.ExpiresAt,
			CreatedAt:  key.CreatedAt,
		})
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output{
		TeamID: teamID.String(),
		Items:  items,
		Total:  total,
		Limit:  cfg.limit,
		Offset: cfg.offset,
	})
}

func parseCLI(args []string, stderr io.Writer) (cliConfig, error) {
	var cfg cliConfig

	fs := flag.NewFlagSet("list-team-profiles", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.teamID, "team-id", "", "Team UUID to inspect")
	fs.IntVar(&cfg.limit, "limit", 100, "Maximum number of profiles to return")
	fs.IntVar(&cfg.offset, "offset", 0, "Offset for pagination")

	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	cfg.teamID = strings.TrimSpace(cfg.teamID)
	if cfg.teamID == "" {
		return cliConfig{}, errors.New("--team-id is required")
	}
	return cfg, nil
}
