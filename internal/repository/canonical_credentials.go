package repository

import (
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func lookupCanonicalCredential(tx *gorm.DB, prefix string) (*domain.Credential, error) {
	return lookupCanonicalCredentialWhere(tx, "c.key_prefix = $1", prefix)
}

func lookupCanonicalCredentialByID(tx *gorm.DB, id uuid.UUID) (*domain.Credential, error) {
	return lookupCanonicalCredentialWhere(tx, "c.id = $1", id)
}

func lookupCanonicalCredentialWhere(tx *gorm.DB, predicate string, value any) (*domain.Credential, error) {
	rows, err := tx.Raw(`
		SELECT
			c.id,
			c.actor_identity_id,
			m.id,
			alias.legacy_owner_id,
			c.team_id,
			COALESCE(t.name, ''),
			c.key_hash,
			c.key_prefix,
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
			COALESCE(c.owner_identity_id::text, ''),
			COALESCE(owner_membership.sso_provider_id::text, ''),
			COALESCE(owner_actor.subject, ''),
			COALESCE(owner_sso.email, ''),
			COALESCE(owner_membership.sso_group_id, ''),
			COALESCE(owner_membership.sso_entitlement_status, ''),
			owner_membership.sso_last_entitlement_checked_at,
			owner_membership.sso_last_login_at,
				COALESCE(c.memory_binding, 'shared_only'),
				COALESCE(c.memory_space_id::text, ''),
				COALESCE((
					SELECT memory_space.generation
					FROM memory_spaces AS memory_space
					WHERE memory_space.id = c.memory_space_id
					  AND memory_space.team_id = c.team_id
					LIMIT 1
				), 0),
				COALESCE((
				SELECT shared_space.id::text
				FROM memory_spaces AS shared_space
				WHERE shared_space.team_id = c.team_id
				  AND shared_space.kind = 'team_shared'
				LIMIT 1
				), ''),
				COALESCE((
					SELECT shared_space.generation
					FROM memory_spaces AS shared_space
					WHERE shared_space.team_id = c.team_id
					  AND shared_space.kind = 'team_shared'
					LIMIT 1
				), 0)
		FROM credentials c
		JOIN actor_identities a
		  ON a.id = c.actor_identity_id
		JOIN team_memberships m
		  ON m.team_id = c.team_id
			 AND m.actor_identity_id = c.actor_identity_id
		JOIN ownership_aliases alias
		  ON alias.team_id = c.team_id
		 AND alias.canonical_identity_id = c.actor_identity_id
		 AND alias.credential_id = c.id
		JOIN teams t ON t.id = c.team_id
		LEFT JOIN actor_identities owner_actor ON owner_actor.id = c.owner_identity_id
		LEFT JOIN team_memberships owner_membership
		  ON owner_membership.actor_identity_id = c.owner_identity_id
		 AND owner_membership.team_id = c.team_id
		LEFT JOIN sso_identities owner_sso ON owner_sso.id = c.owner_identity_id
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
	var key domain.Credential
	var ownerIdentityID, ssoProviderID string
	var memoryBinding, memorySpaceID, teamSharedSpaceID string
	var memorySpaceGeneration, teamSharedSpaceGeneration int64
	if err := rows.Scan(
		&key.ID,
		&key.ActorIdentityID,
		&key.MembershipID,
		&key.OwnerID,
		&key.TeamID,
		&key.TeamName,
		&key.KeyHash,
		&key.KeyPrefix,
		&key.KeySuffix,
		&key.Name,
		pq.Array(&key.Scopes),
		&key.RateLimit,
		&key.Role,
		&key.LastUsedAt,
		&key.ExpiresAt,
		&key.CreatedAt,
		&key.RevokedAt,
		&ownerIdentityID,
		&ssoProviderID,
		&key.SSOSubject,
		&key.SSOEmail,
		&key.SSOGroupID,
		&key.SSOEntitlementStatus,
		&key.SSOLastEntitlementCheckedAt,
		&key.SSOLastLoginAt,
		&memoryBinding,
		&memorySpaceID,
		&memorySpaceGeneration,
		&teamSharedSpaceID,
		&teamSharedSpaceGeneration,
	); err != nil {
		return nil, err
	}
	key.OwnerIdentityID = parseOptionalUUID(ownerIdentityID)
	key.SSOProviderID = parseOptionalUUID(ssoProviderID)
	if err := applyCredentialMemoryFields(&key, memoryBinding, memorySpaceID, memorySpaceGeneration, teamSharedSpaceID, teamSharedSpaceGeneration); err != nil {
		return nil, err
	}
	return &key, nil
}
