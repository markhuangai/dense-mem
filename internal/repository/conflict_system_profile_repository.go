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
		FROM actor_identities
		WHERE team_id = ?::uuid
		  AND kind = 'system'
		LIMIT 1
	`, teamID).Row().Scan(&profileID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if profileID == "" {
		for attempt := 0; attempt < conflictSystemProfileCreateAttempts && profileID == ""; attempt++ {
			candidateID := uuid.NewString()
			result := tx.WithContext(ctx).Exec(`
				INSERT INTO actor_identities (
					id, kind, team_id, display_name, active, created_at, updated_at
				) VALUES (?::uuid, 'system', ?::uuid, ?, false, now(), now())
				ON CONFLICT DO NOTHING
			`, candidateID, teamID, newConflictSystemProfileName())
			if result.Error != nil {
				return "", result.Error
			}
			if result.RowsAffected == 1 {
				if err := tx.WithContext(ctx).Exec(`
					INSERT INTO team_memberships (
						actor_identity_id, team_id, status, team_admin, maximum_grants
					) VALUES (?::uuid, ?::uuid, 'revoked', false, ARRAY[]::text[])
				`, candidateID, teamID).Error; err != nil {
					return "", err
				}
				if err := tx.WithContext(ctx).Exec(`
					INSERT INTO ownership_aliases (
						team_id, legacy_owner_id, canonical_identity_id, credential_id, reason
					) VALUES (?::uuid, ?::uuid, ?::uuid, NULL, 'system')
				`, teamID, candidateID, candidateID).Error; err != nil {
					return "", err
				}
				profileID = candidateID
				break
			}
			err = tx.WithContext(ctx).Raw(`
				SELECT id::text
				FROM actor_identities
				WHERE team_id = ?::uuid
				  AND kind = 'system'
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
