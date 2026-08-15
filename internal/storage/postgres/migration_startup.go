package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type MigrationStateKind string

const (
	MigrationStateFresh   MigrationStateKind = "fresh"
	MigrationStateLegacy  MigrationStateKind = "legacy"
	MigrationStateBridge  MigrationStateKind = "identity_bridge_v2_5"
	MigrationStateClean   MigrationStateKind = "clean_v2_5"
	MigrationStateInvalid MigrationStateKind = "invalid"
)

type MigrationState struct {
	Kind             MigrationStateKind
	DatabaseLatest   int64
	RepositoryLatest int64
	Reason           string
}

var ErrInvalidMigrationState = errors.New("postgres migration state is invalid")

// ClassifyMigrationState distinguishes empty, legacy, bridge, clean, and corrupt catalogs.
func ClassifyMigrationState(ctx context.Context, db *sql.DB, migrationsDir string) (MigrationState, error) {
	if db == nil {
		return MigrationState{}, errors.New("migration state: database is required")
	}
	repositoryLatest, err := latestMigrationVersion(migrationsDir)
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
	var teams, goose, teamProfiles, bridgeReceipt, identities, memberships, credentials, grants, externalLinks, aliases bool
	row := tx.QueryRowContext(ctx, `
		SELECT
			to_regclass('public.teams') IS NOT NULL,
			to_regclass('public.goose_db_version') IS NOT NULL,
			to_regclass('public.team_profiles') IS NOT NULL,
			to_regclass('public.identity_compatibility_state') IS NOT NULL,
			to_regclass('public.actor_identities') IS NOT NULL,
			to_regclass('public.team_memberships') IS NOT NULL,
			to_regclass('public.credentials') IS NOT NULL,
			to_regclass('public.membership_grants') IS NOT NULL,
			to_regclass('public.identity_external_links') IS NOT NULL,
			to_regclass('public.ownership_aliases') IS NOT NULL
	`)
	if err := row.Scan(&teams, &goose, &teamProfiles, &bridgeReceipt, &identities, &memberships, &credentials, &grants, &externalLinks, &aliases); err != nil {
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
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&state.DatabaseLatest); err != nil {
		return MigrationState{}, fmt.Errorf("migration state: inspect Goose version: %w", err)
	}
	if state.DatabaseLatest > repositoryLatest {
		state.Kind = MigrationStateInvalid
		state.Reason = "database migration is newer than this binary"
		return state, nil
	}
	canonicalCount := 0
	for _, exists := range []bool{identities, memberships, credentials, grants, externalLinks, aliases} {
		if exists {
			canonicalCount++
		}
	}
	if canonicalCount == 0 && teamProfiles && !bridgeReceipt {
		state.Kind = MigrationStateLegacy
		return state, nil
	}
	if canonicalCount != 6 {
		state.Kind = MigrationStateInvalid
		state.Reason = "canonical identity tables are only partially installed"
		return state, nil
	}
	if teamProfiles && bridgeReceipt {
		var bridgeState string
		err := tx.QueryRowContext(ctx, `SELECT state FROM identity_compatibility_state WHERE singleton = true`).Scan(&bridgeState)
		if errors.Is(err, sql.ErrNoRows) {
			state.Kind = MigrationStateInvalid
			state.Reason = "identity bridge state row is missing"
			return state, nil
		}
		if err != nil {
			return MigrationState{}, fmt.Errorf("migration state: inspect identity bridge state: %w", err)
		}
		switch bridgeState {
		case "bridge_active", "reconciled", "cutover_ready", "cleanup_complete":
			state.Kind = MigrationStateBridge
			return state, nil
		default:
			state.Kind = MigrationStateInvalid
			state.Reason = "identity bridge state is not recognized"
			return state, nil
		}
	}
	if teamProfiles || bridgeReceipt {
		state.Kind = MigrationStateInvalid
		state.Reason = "legacy identity authority and bridge receipt disagree"
		return state, nil
	}
	var credentialLegacy, membershipLegacy, sessionMembership, sessionLegacy, profileTrigger, ssoTrigger bool
	if err := tx.QueryRowContext(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'credentials' AND column_name = 'legacy_profile_id'),
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'team_memberships' AND column_name = 'legacy_profile_id'),
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'sso_sessions' AND column_name = 'membership_id'),
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'sso_sessions' AND column_name = 'team_profile_id'),
			to_regprocedure('public.dense_mem_sync_legacy_profile_identity()') IS NOT NULL,
			to_regprocedure('public.dense_mem_sync_sso_identity()') IS NOT NULL
	`).Scan(&credentialLegacy, &membershipLegacy, &sessionMembership, &sessionLegacy, &profileTrigger, &ssoTrigger); err != nil {
		return MigrationState{}, fmt.Errorf("migration state: inspect clean identity catalog: %w", err)
	}
	if credentialLegacy || membershipLegacy || !sessionMembership || sessionLegacy || profileTrigger || ssoTrigger {
		state.Kind = MigrationStateInvalid
		state.Reason = "identity cleanup catalog is incomplete"
		return state, nil
	}
	state.Kind = MigrationStateClean
	return state, nil
}

func latestMigrationVersion(dir string) (int64, error) {
	sources, err := listMigrationSources(dir)
	if err != nil {
		return 0, err
	}
	if len(sources) == 0 {
		return 0, errors.New("migration history: no migrations")
	}
	return sources[len(sources)-1].version, nil
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
	if state.Kind != MigrationStateClean {
		return fmt.Errorf("%w: migration did not complete the v2.5 identity cleanup", ErrInvalidMigrationState)
	}
	if state.DatabaseLatest != state.RepositoryLatest {
		return fmt.Errorf("%w: database version %d does not match repository version %d", ErrInvalidMigrationState, state.DatabaseLatest, state.RepositoryLatest)
	}
	return nil
}
