package repository

import (
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func lookupCanonicalCredential(tx *gorm.DB, prefix string) (*domain.APIKey, error) {
	return lookupCanonicalCredentialWhere(tx, "c.key_prefix = $1", prefix)
}

func lookupCanonicalCredentialByID(tx *gorm.DB, id uuid.UUID) (*domain.APIKey, error) {
	return lookupCanonicalCredentialWhere(tx, "c.id = $1", id)
}

func lookupCanonicalCredentialWhere(tx *gorm.DB, predicate string, value any) (*domain.APIKey, error) {
	rows, err := tx.Raw(`
		SELECT
			c.id,
			c.team_id,
			COALESCE(t.name, ''),
			c.key_hash,
			COALESCE(c.key_suffix, ''),
			c.name,
			ARRAY(
				SELECT DISTINCT scope
				FROM unnest(c.scopes) AS scope
				WHERE scope = ANY(m.maximum_grants)
				  AND EXISTS (
					SELECT 1
					FROM membership_grants g
					WHERE g.membership_id = m.id
					  AND g.grant_name = scope
				  )
				ORDER BY scope
			),
			c.rate_limit,
			CASE WHEN m.team_admin THEN 'manager' ELSE 'member' END,
			c.last_used_at,
			c.expires_at,
			c.created_at,
			c.revoked_at,
			COALESCE(c.owner_identity_id::text, '')
		FROM credentials c
		JOIN actor_identities a
		  ON a.id = c.actor_identity_id
		JOIN team_memberships m
		  ON m.team_id = c.team_id
		 AND m.actor_identity_id = c.actor_identity_id
		JOIN teams t ON t.id = c.team_id
		WHERE `+predicate+`
		  AND c.kind = 'api_key'
		  AND c.status = 'active'
		  AND c.key_hash IS NOT NULL
		  AND c.revoked_at IS NULL
		  AND (c.expires_at IS NULL OR c.expires_at > NOW())
		  AND a.active = true
		  AND m.status = 'active'
		  AND t.status = 'active'
		  AND t.deleted_at IS NULL
		LIMIT 1
	`, value).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var key domain.APIKey
	var teamID uuid.UUID
	var ownerIdentityID string
	if err := rows.Scan(
		&key.ID,
		&teamID,
		&key.TeamName,
		&key.KeyHash,
		&key.KeySuffix,
		&key.Label,
		pq.Array(&key.Scopes),
		&key.RateLimit,
		&key.Role,
		&key.LastUsedAt,
		&key.ExpiresAt,
		&key.CreatedAt,
		&key.RevokedAt,
		&ownerIdentityID,
	); err != nil {
		return nil, err
	}
	key.TeamID = teamID
	key.ProfileID = teamID
	key.Name = key.Label
	key.AuthSource = "api_key"
	key.SSOOwnerIdentityID = parseOptionalUUID(ownerIdentityID)
	return &key, nil
}
