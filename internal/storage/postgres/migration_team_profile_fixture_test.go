//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func insertMigrationTeamProfile(t *testing.T, ctx context.Context, db *sql.DB) (string, string) {
	t.Helper()
	teamID, profileID := uuid.NewString(), uuid.NewString()
	keyPrefix := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	var hasLegacyProfiles bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT to_regclass('public.team_profiles') IS NOT NULL
	`).Scan(&hasLegacyProfiles))
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_team_id', $1, true), set_config('app.current_profile_id', $2, true)`, teamID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES ($1::uuid, $2, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, "migration-team-"+teamID); err != nil {
			return err
		}
		if !hasLegacyProfiles {
			for _, statement := range []struct {
				query string
				args  []any
			}{
				{
					query: `
						INSERT INTO actor_identities (id, kind, team_id, display_name)
						VALUES ($1::uuid, 'api_client', $2::uuid, $3)
					`,
					args: []any{profileID, teamID, "migration-profile-" + profileID},
				},
				{
					query: `
						INSERT INTO team_memberships (actor_identity_id, team_id, status, team_admin, maximum_grants)
						VALUES ($1::uuid, $2::uuid, 'active', false, ARRAY['read', 'write']::text[])
					`,
					args: []any{profileID, teamID},
				},
				{
					query: `
						INSERT INTO credentials (
							id, actor_identity_id, owner_identity_id, team_id, kind, key_hash,
							key_prefix, key_suffix, name, scopes, rate_limit, status
						) VALUES (
							$1::uuid, $1::uuid, $1::uuid, $2::uuid, 'api_key', $3,
							$4, $5, $6, ARRAY['read', 'write']::text[], 100, 'active'
						)
					`,
					args: []any{profileID, teamID, "hash-" + profileID, keyPrefix, keyPrefix[:6], "migration-profile-" + profileID},
				},
				{
					query: `
						INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, credential_id)
						VALUES ($1::uuid, $2::uuid, $2::uuid, $2::uuid)
					`,
					args: []any{teamID, profileID},
				},
			} {
				if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
					return err
				}
			}
			return nil
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (
				id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role
			) VALUES (
				$1::uuid, $2::uuid, $3, $4, $5, $6, ARRAY['read','write']::text[], 'member'
			)
		`, profileID, teamID, "hash-"+profileID, keyPrefix, keyPrefix[:6], "migration-profile-"+profileID)
		return err
	}))
	return teamID, profileID
}
