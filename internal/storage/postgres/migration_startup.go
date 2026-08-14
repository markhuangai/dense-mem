package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type MigrationStateKind string

const (
	MigrationStateFresh      MigrationStateKind = "fresh"
	MigrationStateLegacy     MigrationStateKind = "legacy"
	MigrationStateCompatible MigrationStateKind = "compatible_v2_5"
	MigrationStateInvalid    MigrationStateKind = "invalid"
)

type MigrationState struct {
	Kind             MigrationStateKind
	DatabaseLatest   int64
	RepositoryLatest int64
	ExactLegacy      bool
	Reason           string
}

var ErrInvalidMigrationState = errors.New("postgres migration state is invalid")

// ClassifyMigrationState distinguishes an empty database, a pre-bridge
// deployment, a complete v2.5 bridge, and partial/corrupt state. It is read
// only and safe to call before migration startup.
func ClassifyMigrationState(ctx context.Context, db *sql.DB, migrationsDir string) (MigrationState, error) {
	if db == nil {
		return MigrationState{}, errors.New("migration state: database is required")
	}
	repositoryLatest, err := LatestMigrationVersion(migrationsDir)
	if err != nil {
		return MigrationState{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MigrationState{}, fmt.Errorf("migration state: begin read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		SELECT set_config('app.tx_mode', 'system', true),
		       set_config('app.current_team_id', '', true),
		       set_config('app.current_profile_id', '', true)
	`); err != nil {
		return MigrationState{}, fmt.Errorf("migration state: set system context: %w", err)
	}
	var teams, goose, bridge, identities, memberships, credentials, grants, externalLinks, aliases bool
	row := tx.QueryRowContext(ctx, `
		SELECT
			to_regclass('public.teams') IS NOT NULL,
			to_regclass('public.goose_db_version') IS NOT NULL,
			to_regclass('public.identity_compatibility_state') IS NOT NULL,
			to_regclass('public.actor_identities') IS NOT NULL,
			to_regclass('public.team_memberships') IS NOT NULL,
			to_regclass('public.credentials') IS NOT NULL,
			to_regclass('public.membership_grants') IS NOT NULL,
			to_regclass('public.identity_external_links') IS NOT NULL,
			to_regclass('public.ownership_aliases') IS NOT NULL
	`)
	if err := row.Scan(&teams, &goose, &bridge, &identities, &memberships, &credentials, &grants, &externalLinks, &aliases); err != nil {
		return MigrationState{}, fmt.Errorf("migration state: inspect schema: %w", err)
	}
	state := MigrationState{RepositoryLatest: repositoryLatest}
	if !teams && !goose {
		state.Kind = MigrationStateFresh
		return state, nil
	}
	if !teams || !goose {
		state.Kind = MigrationStateInvalid
		state.Reason = "application tables and Goose history are incomplete"
		return state, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version`).Scan(&state.DatabaseLatest); err != nil {
		return MigrationState{}, fmt.Errorf("migration state: inspect Goose version: %w", err)
	}
	if state.DatabaseLatest > repositoryLatest {
		state.Kind = MigrationStateInvalid
		state.Reason = "database migration is newer than this binary"
		return state, nil
	}
	files, err := ListMigrationFiles(migrationsDir)
	if err != nil {
		return MigrationState{}, err
	}
	if len(files) >= 2 && state.DatabaseLatest == files[len(files)-2].Version {
		state.ExactLegacy = true
	}
	if !bridge && !identities && !memberships && !credentials && !grants && !externalLinks && !aliases {
		state.Kind = MigrationStateLegacy
		return state, nil
	}
	if !(bridge && identities && memberships && credentials && grants && externalLinks && aliases) {
		state.Kind = MigrationStateInvalid
		state.Reason = "identity bridge is only partially installed"
		return state, nil
	}
	var bridgeState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM identity_compatibility_state WHERE singleton = true`).Scan(&bridgeState); err != nil {
		state.Kind = MigrationStateInvalid
		state.Reason = "identity bridge state row is missing or unreadable"
		return state, nil
	}
	switch bridgeState {
	case "bridge_active", "reconciled", "cutover_ready", "cleanup_complete":
	default:
		state.Kind = MigrationStateInvalid
		state.Reason = "identity bridge state is not recognized"
		return state, nil
	}
	state.Kind = MigrationStateCompatible
	return state, nil
}

// ValidateStartupMigrationState is called after Goose has finished. It keeps
// the application from starting on a partially applied identity bridge or on
// a database that was upgraded by a newer binary.
func ValidateStartupMigrationState(ctx context.Context, db *sql.DB, migrationsDir string) error {
	state, err := ClassifyMigrationState(ctx, db, migrationsDir)
	if err != nil {
		return err
	}
	if state.Kind == MigrationStateInvalid {
		return fmt.Errorf("%w: %s", ErrInvalidMigrationState, state.Reason)
	}
	if state.Kind == MigrationStateFresh || state.Kind == MigrationStateLegacy {
		return fmt.Errorf("%w: migration did not install the v2.5 bridge", ErrInvalidMigrationState)
	}
	if state.DatabaseLatest != state.RepositoryLatest {
		return fmt.Errorf("%w: database version %d is behind repository version %d", ErrInvalidMigrationState, state.DatabaseLatest, state.RepositoryLatest)
	}
	return nil
}
