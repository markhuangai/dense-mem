package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

// These digests bind the runtime bridge check to the function bodies shipped
// by the v2.5 identity migration. The migration history check separately
// protects the SQL file itself from edits after deployment.
const (
	legacyProfileIdentityBridgeFunctionSHA256 = "6d3b719dfaf5cdd7d905c2d6f553d061b5c0f445e65939e5af70fdaaa7a0005f"
	ssoIdentityBridgeFunctionSHA256           = "fef326cd83370c48e93998d62aff5a74906ae3a5edbf38350d78aca4110ea7b5"
)

func migrationFunctionBodyMatches(source, expectedSHA256 string) bool {
	if source == "" {
		return false
	}
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:]) == expectedSHA256
}

// IdentityBridgeFunctionBodiesMatch applies the same release-bound function
// body digests used by startup validation to runtime cleanup preflight.
func IdentityBridgeFunctionBodiesMatch(legacySource, ssoSource string) bool {
	return migrationFunctionBodyMatches(legacySource, legacyProfileIdentityBridgeFunctionSHA256) &&
		migrationFunctionBodyMatches(ssoSource, ssoIdentityBridgeFunctionSHA256)
}

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
	var legacyBridgeFunctionSource string
	var legacyBridgeTrigger bool
	var ssoBridgeFunctionSource string
	var ssoBridgeTrigger bool
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
			to_regclass('public.ownership_aliases') IS NOT NULL,
			COALESCE((
				SELECT p.prosrc
				FROM pg_proc p
				JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = 'public'
				  AND p.proname = 'dense_mem_sync_legacy_profile_identity'
				  AND p.prokind = 'f'
				  AND pg_get_function_identity_arguments(p.oid) = ''
			  LIMIT 1
			), ''),
			EXISTS (
				SELECT 1
				FROM pg_trigger t
				JOIN pg_class c ON c.oid = t.tgrelid
				JOIN pg_namespace n ON n.oid = c.relnamespace
				JOIN pg_proc p ON p.oid = t.tgfoid
				JOIN pg_namespace pn ON pn.oid = p.pronamespace
				WHERE n.nspname = 'public'
				  AND c.relname = 'team_profiles'
				  AND t.tgname = 'team_profiles_identity_bridge'
				  AND NOT t.tgisinternal
				  AND t.tgqual IS NULL
				  AND t.tgenabled IN ('O', 'A')
				  AND (t.tgtype & 1) <> 0
				  AND (t.tgtype & 2) = 0
				  AND (t.tgtype & 4) <> 0
				  AND (t.tgtype & 8) <> 0
				  AND (t.tgtype & 16) <> 0
				  AND (
						SELECT COALESCE(array_agg(a.attname::text ORDER BY a.attname), ARRAY[]::text[])
						FROM pg_attribute a
						WHERE a.attrelid = t.tgrelid
						  AND a.attnum = ANY(t.tgattr)
						  AND NOT a.attisdropped
					) = ARRAY[
						'auth_source', 'expires_at', 'is_system', 'key_hash', 'key_prefix', 'key_suffix',
						'last_used_at', 'name', 'rate_limit', 'revoked_at', 'role', 'scopes', 'sso_email',
						'sso_entitlement_status', 'sso_group_id', 'sso_identity_id', 'sso_owner_identity_id',
						'sso_provider_id', 'sso_subject', 'team_id'
					]::text[]
				  AND pn.nspname = 'public'
				  AND p.proname = 'dense_mem_sync_legacy_profile_identity'
				  AND pg_get_function_identity_arguments(p.oid) = ''
			),
			COALESCE((
				SELECT p.prosrc
				FROM pg_proc p
				JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = 'public'
				  AND p.proname = 'dense_mem_sync_sso_identity'
				  AND p.prokind = 'f'
				  AND pg_get_function_identity_arguments(p.oid) = ''
			  LIMIT 1
			), ''),
			EXISTS (
				SELECT 1
				FROM pg_trigger t
				JOIN pg_class c ON c.oid = t.tgrelid
				JOIN pg_namespace n ON n.oid = c.relnamespace
				JOIN pg_proc p ON p.oid = t.tgfoid
				JOIN pg_namespace pn ON pn.oid = p.pronamespace
				WHERE n.nspname = 'public'
				  AND c.relname = 'sso_identities'
				  AND t.tgname = 'sso_identities_identity_bridge'
				  AND NOT t.tgisinternal
				  AND t.tgqual IS NULL
				  AND t.tgenabled IN ('O', 'A')
				  AND (t.tgtype & 1) <> 0
				  AND (t.tgtype & 2) = 0
				  AND (t.tgtype & 4) <> 0
				  AND (t.tgtype & 8) <> 0
				  AND (t.tgtype & 16) <> 0
				  AND (
						SELECT COALESCE(array_agg(a.attname::text ORDER BY a.attname), ARRAY[]::text[])
						FROM pg_attribute a
						WHERE a.attrelid = t.tgrelid
						  AND a.attnum = ANY(t.tgattr)
						  AND NOT a.attisdropped
					) = ARRAY['active', 'display_name', 'external_id', 'provider_id', 'subject']::text[]
				  AND pn.nspname = 'public'
				  AND p.proname = 'dense_mem_sync_sso_identity'
				  AND pg_get_function_identity_arguments(p.oid) = ''
			)
	`)
	if err := row.Scan(&teams, &goose, &bridge, &identities, &memberships, &credentials, &grants, &externalLinks, &aliases, &legacyBridgeFunctionSource, &legacyBridgeTrigger, &ssoBridgeFunctionSource, &ssoBridgeTrigger); err != nil {
		return MigrationState{}, fmt.Errorf("migration state: inspect schema: %w", err)
	}
	legacyBridgeFunction := migrationFunctionBodyMatches(legacyBridgeFunctionSource, legacyProfileIdentityBridgeFunctionSHA256)
	ssoBridgeFunction := migrationFunctionBodyMatches(ssoBridgeFunctionSource, ssoIdentityBridgeFunctionSHA256)
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
	files, err := ListMigrationFiles(migrationsDir)
	if err != nil {
		return MigrationState{}, err
	}
	if len(files) >= 2 && state.DatabaseLatest == files[len(files)-2].Version {
		state.ExactLegacy = true
	}
	if !bridge && !identities && !memberships && !credentials && !grants && !externalLinks && !aliases && !legacyBridgeFunction && !legacyBridgeTrigger && !ssoBridgeFunction && !ssoBridgeTrigger {
		state.Kind = MigrationStateLegacy
		return state, nil
	}
	if !(bridge && identities && memberships && credentials && grants && externalLinks && aliases && legacyBridgeFunction && legacyBridgeTrigger && ssoBridgeFunction && ssoBridgeTrigger) {
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
