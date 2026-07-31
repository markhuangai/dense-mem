package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	conflictSystemProfileNamePrefix     = "__dense_mem_conflict_system__:"
	conflictSystemProfileCreateAttempts = 5
)

func ensureConflictSystemProfile(ctx context.Context, tx *gorm.DB, teamID string) (string, error) {
	var profileID string
	err := tx.WithContext(ctx).Raw(`
		SELECT id::text
		FROM team_profiles
		WHERE team_id = ?::uuid
		  AND is_system
		LIMIT 1
	`, teamID).Row().Scan(&profileID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if profileID == "" {
		for attempt := 0; attempt < conflictSystemProfileCreateAttempts && profileID == ""; attempt++ {
			err = tx.WithContext(ctx).Raw(`
				INSERT INTO team_profiles (
				    team_id, key_hash, key_prefix, key_suffix, name, scopes, role, rate_limit,
				    revoked_at, auth_source, is_system
				) VALUES (
				    ?::uuid, NULL, NULL, NULL, ?, ARRAY[]::text[], 'member', 0, now(), 'system', true
				)
				ON CONFLICT DO NOTHING
				RETURNING id::text
			`, teamID, newConflictSystemProfileName()).Row().Scan(&profileID)
			if err == nil {
				break
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
			err = tx.WithContext(ctx).Raw(`
				SELECT id::text
				FROM team_profiles
				WHERE team_id = ?::uuid
				  AND is_system
				LIMIT 1
			`, teamID).Row().Scan(&profileID)
			if err == nil {
				break
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
		}
		if profileID == "" {
			return "", fmt.Errorf("create conflict system profile after %d attempts", conflictSystemProfileCreateAttempts)
		}
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_team_refs (team_id)
		VALUES (?::uuid)
		ON CONFLICT (team_id) DO NOTHING
	`, teamID).Error; err != nil {
		return "", err
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_profile_refs (team_id, profile_id)
		VALUES (?::uuid, ?::uuid)
		ON CONFLICT (team_id, profile_id) DO NOTHING
	`, teamID, profileID).Error; err != nil {
		return "", err
	}
	return profileID, nil
}

func newConflictSystemProfileName() string {
	return conflictSystemProfileNamePrefix + uuid.NewString()
}
