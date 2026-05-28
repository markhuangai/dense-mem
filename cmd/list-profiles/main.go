package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/markhuangai/dense-mem/internal/operatorcli"
)

type cliConfig struct {
	limit  int
	offset int
}

type teamItem struct {
	TeamID      string         `json:"team_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Config      map[string]any `json:"config,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type output struct {
	Items  []teamItem `json:"items"`
	Total  int64      `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
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

	teams, err := services.ProfileService.List(ctx, cfg.limit, cfg.offset)
	if err != nil {
		return fmt.Errorf("list teams: %w", err)
	}
	total, err := services.ProfileService.Count(ctx)
	if err != nil {
		return fmt.Errorf("count teams: %w", err)
	}

	items := make([]teamItem, 0, len(teams))
	for _, team := range teams {
		items = append(items, teamItem{
			TeamID:      team.ID.String(),
			Name:        team.Name,
			Description: team.Description,
			Metadata:    team.Metadata,
			Config:      team.Config,
			CreatedAt:   team.CreatedAt,
			UpdatedAt:   team.UpdatedAt,
		})
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output{
		Items:  items,
		Total:  total,
		Limit:  cfg.limit,
		Offset: cfg.offset,
	})
}

func parseCLI(args []string, stderr io.Writer) (cliConfig, error) {
	var cfg cliConfig

	fs := flag.NewFlagSet("list-teams", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(&cfg.limit, "limit", 100, "Maximum number of teams to return")
	fs.IntVar(&cfg.offset, "offset", 0, "Offset for pagination")

	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	return cfg, nil
}
