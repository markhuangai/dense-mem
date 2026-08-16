package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// CredentialRepository is the companion interface for credential credential data access.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type CredentialRepository interface {
	CreateCredential(ctx context.Context, credential *domain.Credential) error
	ListByTeam(ctx context.Context, teamID uuid.UUID, limit, offset int) ([]*domain.Credential, error)
	CountByTeam(ctx context.Context, teamID uuid.UUID) (int64, error)
	// GetByIDForTeam returns an API credential only when it belongs to teamID. Returns nil on mismatch.
	GetByIDForTeam(ctx context.Context, teamID, id uuid.UUID) (*domain.Credential, error)
	GetSSOOwnedCredential(ctx context.Context, teamID, identityID uuid.UUID) (*domain.Credential, error)
	GetActiveByPrefix(ctx context.Context, prefix string) (*domain.Credential, error)
	// RevokeForTeam marks a credential revoked only when it belongs to teamID. Returns number of rows affected.
	RevokeForTeam(ctx context.Context, teamID, id uuid.UUID) (int64, error)
	// DeleteForTeam hard-deletes a credential only when it belongs to teamID. Returns number of rows affected.
	DeleteForTeam(ctx context.Context, teamID, id uuid.UUID) (int64, error)
	// UpdateNameForTeam renames a credential only when it belongs to teamID.
	UpdateNameForTeam(ctx context.Context, teamID, id uuid.UUID, name string) (int64, error)
	// UpdateRoleForTeam changes a credential role and scope set only when it belongs to teamID.
	UpdateRoleForTeam(ctx context.Context, teamID, id uuid.UUID, role string, scopes []string) (int64, error)
	// UpdateScopesForTeam changes a credential scope set only when it belongs to teamID.
	UpdateScopesForTeam(ctx context.Context, teamID, id uuid.UUID, scopes []string) (int64, error)
	// RotateForTeam replaces credential material for one credential in place.
	RotateForTeam(ctx context.Context, teamID, id uuid.UUID, keyHash, keyPrefix, keySuffix string, expiresAt *time.Time) (int64, error)
	TouchLastUsed(ctx context.Context, id uuid.UUID) error
}

// LastUsedUpdate is one admitted credential activity timestamp.
type LastUsedUpdate struct {
	ID uuid.UUID
	At time.Time
}

// CredentialLastUsedBatchRepository is implemented by the production repository
// so activity writers can persist coalesced timestamps in one transaction.
type CredentialLastUsedBatchRepository interface {
	TouchLastUsedBatch(ctx context.Context, updates []LastUsedUpdate) error
}

// CredentialRepositoryImpl implements the CredentialRepository interface.
// Every query runs inside an RLS-aware transaction so Postgres FORCE RLS
// policies (app.current_profile_id / app.tx_mode) enforce tenant isolation
// even if a caller ever reaches the repository without the service layer.
type CredentialRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

// Ensure CredentialRepositoryImpl implements CredentialRepository
var _ CredentialRepository = (*CredentialRepositoryImpl)(nil)

// NewCredentialRepository creates a new API credential repository instance.
// rls is required; nil causes a panic at first use. Callers should pass
// postgres.NewRLS() for production and an RLSHelper mock for unit tests.
func NewCredentialRepository(db *gorm.DB, rls postgres.RLSHelper) *CredentialRepositoryImpl {
	return &CredentialRepositoryImpl{db: db, rls: rls}
}

// CreateCredential creates one API-client identity, membership, credential, and ownership alias.
func (r *CredentialRepositoryImpl) CreateCredential(ctx context.Context, credential *domain.Credential) error {
	if credential.ID == uuid.Nil {
		credential.ID = uuid.New()
	}
	teamID := credential.GetTeamID()
	name := credential.Name

	now := time.Now().UTC()
	credential.CreatedAt = now

	keyPrefix := credential.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = GetKeyPrefixFromHash(credential.KeyHash)
	}
	keySuffix := credential.KeySuffix
	var membershipID uuid.UUID

	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO actor_identities (id, kind, team_id, display_name, active, created_at, updated_at)
			VALUES ($1, 'api_client', $2, $3, true, $4, $4)
		`, credential.ID, teamID, name, now).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			INSERT INTO team_memberships (
				actor_identity_id, team_id, status, team_admin, maximum_grants, created_at, updated_at
			) VALUES ($1, $2, 'active', $3, $4, $5, $5)
			RETURNING id
		`, credential.ID, teamID, credential.GetRole() == "manager", pq.Array(credential.Scopes), now).Row().Scan(&membershipID); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO membership_grants (membership_id, grant_name, source)
			SELECT $1, scope, 'legacy_scope' FROM unnest($2::text[]) AS scope
		`, membershipID, pq.Array(credential.Scopes)).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO credentials (
				id, actor_identity_id, owner_identity_id, team_id, kind, key_hash, key_prefix,
				key_suffix, name, scopes, rate_limit, status, expires_at, created_at, updated_at
			) VALUES (
				$1, $1, $2, $3, 'api_key', $4, $5, NULLIF($6, ''), $7, $8, $9,
				'active', $10, $11, $11
			)
		`, credential.ID, credential.OwnerIdentityID, teamID, credential.KeyHash, keyPrefix, keySuffix, name,
			pq.Array(credential.Scopes), credential.RateLimit, credential.ExpiresAt, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO ownership_aliases (
				team_id, legacy_owner_id, canonical_identity_id, credential_id, reason
			) VALUES ($1, $2, $2, $2, 'credential')
		`, teamID, credential.ID).Error
	})

	if err != nil {
		return fmt.Errorf("failed to create standard api credential: %w", err)
	}
	credential.ActorIdentityID = credential.ID
	credential.MembershipID = membershipID
	credential.OwnerID = credential.ID

	return nil
}

// GetKeyPrefixFromHash extracts a prefix placeholder from the credential hash.
// In practice, the key_prefix should be passed separately, but this helper
// extracts the first 24 chars of the hash as a fallback.
func GetKeyPrefixFromHash(hash string) string {
	if len(hash) < 24 {
		return hash
	}
	return hash[:24]
}

// ListByTeam retrieves API credentials for a team with pagination.
// Excludes the key_hash field from results.
//
// Uses *sql.Rows + pq.Array() — see GetActiveByPrefix for the rationale.
func (r *CredentialRepositoryImpl) ListByTeam(ctx context.Context, teamID uuid.UUID, limit, offset int) ([]*domain.Credential, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	credentials := make([]*domain.Credential, 0)
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		rows, rerr := tx.Raw(`
				SELECT
					c.id, c.actor_identity_id, membership.id, alias.legacy_owner_id,
					c.team_id, COALESCE(t.name, ''), COALESCE(c.key_suffix, ''), c.name, c.scopes,
					CASE WHEN m.team_admin THEN 'manager' ELSE 'member' END, c.rate_limit,
					c.last_used_at, c.expires_at, c.created_at, c.revoked_at,
					COALESCE(c.owner_identity_id::text, ''),
					COALESCE(owner_membership.sso_provider_id::text, ''),
					COALESCE(owner_actor.subject, ''), COALESCE(owner_sso.email, ''),
					COALESCE(owner_membership.sso_group_id, ''),
					COALESCE(owner_membership.sso_entitlement_status, ''),
					owner_membership.sso_last_entitlement_checked_at,
					owner_membership.sso_last_login_at
				FROM credentials c
				JOIN team_memberships membership
				  ON membership.actor_identity_id = c.actor_identity_id AND membership.team_id = c.team_id
				JOIN team_memberships m ON m.id = membership.id
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
				WHERE c.team_id = $1
					AND c.kind = 'api_key'
					AND c.status <> 'disabled'
					AND t.status = 'active'
					AND t.deleted_at IS NULL
					ORDER BY c.created_at DESC, c.id ASC
				LIMIT $2 OFFSET $3
		`, teamID, limit, offset).Rows()
		if rerr != nil {
			return rerr
		}
		defer rows.Close()

		for rows.Next() {
			state := credentialHydrationState{}
			if serr := state.scan(rows); serr != nil {
				return serr
			}
			credentials = append(credentials, state.result())
		}
		return rows.Err()
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list api credentials: %w", err)
	}
	return credentials, nil
}

// GetActiveByPrefix retrieves an active (non-revoked, non-expired) API credential by its prefix.
// This is used during authentication to look up the credential hash for verification.
// Includes the key_hash field for verification purposes.
//
// Uses *sql.Rows + pq.Array() rather than GORM .Scan() because the pgx driver
// (via gorm.io/driver/postgres) does not route text[] values through lib/pq's
// StringArray scanner when GORM copies columns by reflection; scopes come back
// empty and authorization fails closed.
func (r *CredentialRepositoryImpl) GetActiveByPrefix(ctx context.Context, prefix string) (*domain.Credential, error) {
	var canonicalKey *domain.Credential

	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		canonicalKey, err = lookupCanonicalCredential(tx, prefix)
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get api credential by prefix: %w", err)
	}
	return canonicalKey, nil
}

// GetActiveByID retrieves the current authorization metadata for a standard
// API credential team without loading credential material. The stable team ID remains
// valid across in-place credential rotation, while revocation and team state are
// checked on every portal-cookie request.
func (r *CredentialRepositoryImpl) GetActiveByID(ctx context.Context, id uuid.UUID) (*domain.Credential, error) {
	var canonicalKey *domain.Credential

	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		canonicalKey, err = lookupCanonicalCredentialByID(tx, id)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get active api credential by id: %w", err)
	}
	return canonicalKey, nil
}

// RevokeForTeam marks an API credential as revoked only when it belongs to teamID.
// Returns the number of rows affected (0 means the id/team combination did not match).
func (r *CredentialRepositoryImpl) RevokeForTeam(ctx context.Context, teamID, id uuid.UUID) (int64, error) {
	now := time.Now().UTC()
	// Team-scoped revoke; UPDATE must satisfy api_keys_self_access.
	var rowsAffected int64
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID.String()); err != nil {
			return err
		}
		res := tx.Exec(`
			UPDATE credentials
			SET status = 'revoked', revoked_at = $1, updated_at = $1
			WHERE id = $2 AND team_id = $3 AND kind = 'api_key'
			  AND status = 'active' AND revoked_at IS NULL
		`, now, id, teamID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		if rowsAffected == 0 {
			return nil
		}
		if err := tx.Exec(`
			UPDATE team_memberships
			SET status = 'revoked', updated_at = $1
			WHERE team_id = $2
			  AND actor_identity_id = (SELECT actor_identity_id FROM credentials WHERE id = $3)
		`, now, teamID, id).Error; err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("failed to revoke api credential for team: %w", err)
	}
	if rowsAffected > 0 {
		if err := r.deactivateActorIfUnused(ctx, now, id); err != nil {
			return 0, fmt.Errorf("failed to reconcile revoked api credential actor: %w", err)
		}
	}

	return rowsAffected, nil
}

// DeleteForTeam removes an API credential from supported reads while retaining its stable audit identity.
func (r *CredentialRepositoryImpl) DeleteForTeam(ctx context.Context, teamID, id uuid.UUID) (int64, error) {
	now := time.Now().UTC()
	var rowsAffected int64
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID.String()); err != nil {
			return err
		}
		res := tx.Exec(`
			UPDATE credentials
			SET status = 'disabled', revoked_at = COALESCE(revoked_at, $1), updated_at = $1
			WHERE id = $2 AND team_id = $3 AND kind = 'api_key' AND status <> 'disabled'
		`, now, id, teamID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		if rowsAffected == 0 {
			return nil
		}
		if err := tx.Exec(`
			UPDATE team_memberships
			SET status = 'revoked', updated_at = $1
			WHERE team_id = $2
			  AND actor_identity_id = (SELECT actor_identity_id FROM credentials WHERE id = $3)
		`, now, teamID, id).Error; err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("failed to delete api credential for team: %w", err)
	}
	if rowsAffected > 0 {
		if err := r.deactivateActorIfUnused(ctx, now, id); err != nil {
			return 0, fmt.Errorf("failed to reconcile deleted api credential actor: %w", err)
		}
	}

	return rowsAffected, nil
}

func (r *CredentialRepositoryImpl) deactivateActorIfUnused(ctx context.Context, now time.Time, credentialID uuid.UUID) error {
	// Actor liveness spans teams, so reconcile it in a separate system transaction.
	return r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE actor_identities AS actor
			SET active = false, updated_at = $1
			WHERE actor.id = (SELECT actor_identity_id FROM credentials WHERE id = $2)
			  AND NOT EXISTS (
				  SELECT 1
				  FROM team_memberships AS membership
				  WHERE membership.actor_identity_id = actor.id
				    AND membership.status = 'active'
			  )
		`, now, credentialID).Error
	})
}

// UpdateNameForTeam renames a credential without changing its bearer material.
func (r *CredentialRepositoryImpl) UpdateNameForTeam(ctx context.Context, teamID, id uuid.UUID, name string) (int64, error) {
	now := time.Now().UTC()
	var rowsAffected int64
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID.String()); err != nil {
			return err
		}
		res := tx.Exec(`
			UPDATE credentials
			SET name = $1,
			    updated_at = $2
			WHERE id = $3 AND team_id = $4 AND kind = 'api_key' AND status <> 'disabled'
		`, name, now, id, teamID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		if rowsAffected == 0 {
			return nil
		}
		return tx.Exec(`
			UPDATE actor_identities
			SET display_name = $1, updated_at = $2
			WHERE id = (SELECT actor_identity_id FROM credentials WHERE id = $3)
		`, name, now, id).Error
	})
	if err != nil {
		return 0, fmt.Errorf("failed to update credential name: %w", err)
	}
	return rowsAffected, nil
}

// UpdateRoleForTeam changes a credential role and scopes without changing bearer material.
func (r *CredentialRepositoryImpl) UpdateRoleForTeam(ctx context.Context, teamID, id uuid.UUID, role string, scopes []string) (int64, error) {
	now := time.Now().UTC()
	var rowsAffected int64
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID.String()); err != nil {
			return err
		}
		res := tx.Exec(`
			UPDATE credentials
			SET scopes = $1, updated_at = $2
			WHERE id = $3 AND team_id = $4 AND kind = 'api_key' AND status <> 'disabled'
		`, pq.Array(scopes), now, id, teamID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		if rowsAffected == 0 {
			return nil
		}
		var membershipID uuid.UUID
		if err := tx.Raw(`
			UPDATE team_memberships
			SET team_admin = $1, maximum_grants = $2, updated_at = $3
			WHERE team_id = $4
			  AND actor_identity_id = (SELECT actor_identity_id FROM credentials WHERE id = $5)
			RETURNING id
		`, role == "manager", pq.Array(scopes), now, teamID, id).Row().Scan(&membershipID); err != nil {
			return err
		}
		return replaceLegacyMembershipGrants(tx, membershipID, scopes)
	})
	if err != nil {
		return 0, fmt.Errorf("failed to update credential role: %w", err)
	}
	return rowsAffected, nil
}

// UpdateScopesForTeam changes credential scopes without changing bearer material.
func (r *CredentialRepositoryImpl) UpdateScopesForTeam(ctx context.Context, teamID, id uuid.UUID, scopes []string) (int64, error) {
	now := time.Now().UTC()
	var rowsAffected int64
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID.String()); err != nil {
			return err
		}
		res := tx.Exec(`
			UPDATE credentials
			SET scopes = $1, updated_at = $2
			WHERE id = $3 AND team_id = $4 AND kind = 'api_key' AND status <> 'disabled'
		`, pq.Array(scopes), now, id, teamID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		if rowsAffected == 0 {
			return nil
		}
		var membershipID uuid.UUID
		if err := tx.Raw(`
			UPDATE team_memberships
			SET maximum_grants = $1, updated_at = $2
			WHERE team_id = $3
			  AND actor_identity_id = (SELECT actor_identity_id FROM credentials WHERE id = $4)
			RETURNING id
		`, pq.Array(scopes), now, teamID, id).Row().Scan(&membershipID); err != nil {
			return err
		}
		return replaceLegacyMembershipGrants(tx, membershipID, scopes)
	})
	if err != nil {
		return 0, fmt.Errorf("failed to update credential scopes: %w", err)
	}
	return rowsAffected, nil
}

// RotateForTeam replaces one credential's bearer secret without changing its identity.
func (r *CredentialRepositoryImpl) RotateForTeam(ctx context.Context, teamID, id uuid.UUID, keyHash, keyPrefix, keySuffix string, expiresAt *time.Time) (int64, error) {
	now := time.Now().UTC()
	var rowsAffected int64
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID.String()); err != nil {
			return err
		}
		res := tx.Exec(`
			UPDATE credentials
			SET key_hash = $1,
			    key_prefix = $2,
			    key_suffix = NULLIF($3, ''),
			    expires_at = $4,
			    status = 'active',
			    revoked_at = NULL,
			    last_used_at = NULL,
			    updated_at = $5
			WHERE id = $6 AND team_id = $7 AND kind = 'api_key' AND status <> 'disabled'
		`, keyHash, keyPrefix, keySuffix, expiresAt, now, id, teamID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		if rowsAffected == 0 {
			return nil
		}
		if err := tx.Exec(`
			UPDATE team_memberships
			SET status = 'active', updated_at = $1
			WHERE team_id = $2
			  AND actor_identity_id = (SELECT actor_identity_id FROM credentials WHERE id = $3)
		`, now, teamID, id).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE actor_identities
			SET active = true, updated_at = $1
			WHERE id = (SELECT actor_identity_id FROM credentials WHERE id = $2)
		`, now, id).Error
	})
	if err != nil {
		return 0, fmt.Errorf("failed to rotate credential for credential: %w", err)
	}
	return rowsAffected, nil
}

func replaceLegacyMembershipGrants(tx *gorm.DB, membershipID uuid.UUID, scopes []string) error {
	if err := tx.Exec(`
		DELETE FROM membership_grants
		WHERE membership_id = $1
		  AND source = 'legacy_scope'
		  AND NOT (grant_name = ANY($2::text[]))
	`, membershipID, pq.Array(scopes)).Error; err != nil {
		return err
	}
	return tx.Exec(`
		INSERT INTO membership_grants (membership_id, grant_name, source)
		SELECT $1, scope, 'legacy_scope' FROM unnest($2::text[]) AS scope
		ON CONFLICT (membership_id, grant_name) DO NOTHING
	`, membershipID, pq.Array(scopes)).Error
}

const credentialHydrationSelect = `
		c.id, c.actor_identity_id, membership.id, alias.legacy_owner_id,
		c.team_id, COALESCE(t.name, ''), COALESCE(c.key_suffix, ''), c.name, c.scopes,
		CASE WHEN membership.team_admin THEN 'manager' ELSE 'member' END, c.rate_limit,
		c.last_used_at, c.expires_at, c.created_at, c.revoked_at,
		COALESCE(c.owner_identity_id::text, ''),
		COALESCE(owner_membership.sso_provider_id::text, ''),
		COALESCE(owner_actor.subject, ''), COALESCE(owner_sso.email, ''),
		COALESCE(owner_membership.sso_group_id, ''),
		COALESCE(owner_membership.sso_entitlement_status, ''),
		owner_membership.sso_last_entitlement_checked_at,
		owner_membership.sso_last_login_at
`

type credentialHydrationState struct {
	credential                     domain.Credential
	ownerIdentityID, ssoProviderID string
}

func (s *credentialHydrationState) scan(rows *sql.Rows) error {
	return rows.Scan(
		&s.credential.ID,
		&s.credential.ActorIdentityID,
		&s.credential.MembershipID,
		&s.credential.OwnerID,
		&s.credential.TeamID,
		&s.credential.TeamName,
		&s.credential.KeySuffix,
		&s.credential.Name,
		pq.Array(&s.credential.Scopes),
		&s.credential.Role,
		&s.credential.RateLimit,
		&s.credential.LastUsedAt,
		&s.credential.ExpiresAt,
		&s.credential.CreatedAt,
		&s.credential.RevokedAt,
		&s.ownerIdentityID,
		&s.ssoProviderID,
		&s.credential.SSOSubject,
		&s.credential.SSOEmail,
		&s.credential.SSOGroupID,
		&s.credential.SSOEntitlementStatus,
		&s.credential.SSOLastEntitlementCheckedAt,
		&s.credential.SSOLastLoginAt,
	)
}

func (s *credentialHydrationState) result() *domain.Credential {
	s.credential.OwnerIdentityID = parseOptionalUUID(s.ownerIdentityID)
	s.credential.SSOProviderID = parseOptionalUUID(s.ssoProviderID)
	return &s.credential
}

func (r *CredentialRepositoryImpl) getOneHydratedCredential(ctx context.Context, teamID uuid.UUID, query string, args ...any) (*domain.Credential, error) {
	state := &credentialHydrationState{}
	found := false
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		rows, err := tx.Raw(query, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			found = true
			if err := state.scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return state.result(), nil
}

// GetByIDForTeam retrieves an API credential by ID only when it belongs to teamID.
func (r *CredentialRepositoryImpl) GetByIDForTeam(ctx context.Context, teamID, id uuid.UUID) (*domain.Credential, error) {
	credential, err := r.getOneHydratedCredential(ctx, teamID, `
		SELECT `+credentialHydrationSelect+`
		FROM credentials c
		JOIN team_memberships membership
		  ON membership.actor_identity_id = c.actor_identity_id AND membership.team_id = c.team_id
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
		WHERE c.id = $1 AND c.team_id = $2 AND c.kind = 'api_key' AND c.status <> 'disabled'
		  AND t.status = 'active' AND t.deleted_at IS NULL
	`, id, teamID)
	if err != nil {
		return nil, fmt.Errorf("failed to get api credential for team: %w", err)
	}
	return credential, nil
}

// GetSSOOwnedCredential returns the active normal API credential owned by an SSO identity for one team.
func (r *CredentialRepositoryImpl) GetSSOOwnedCredential(ctx context.Context, teamID, identityID uuid.UUID) (*domain.Credential, error) {
	credential, err := r.getOneHydratedCredential(ctx, teamID, `
		SELECT `+credentialHydrationSelect+`
		FROM credentials c
		JOIN team_memberships membership
		  ON membership.actor_identity_id = c.actor_identity_id AND membership.team_id = c.team_id
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
		WHERE c.team_id = $1 AND c.owner_identity_id = $2
		  AND c.kind = 'api_key' AND c.status = 'active' AND c.revoked_at IS NULL
		  AND t.status = 'active' AND t.deleted_at IS NULL
		ORDER BY c.created_at DESC, c.id ASC
		LIMIT 1
	`, teamID, identityID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sso-owned api credential for team: %w", err)
	}
	return credential, nil
}

func parseOptionalUUID(raw string) *uuid.UUID {
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	return &id
}

// CountByTeam returns the total number of API credentials for a team.
// Used to populate pagination totals without a second full-result scan.
func (r *CredentialRepositoryImpl) CountByTeam(ctx context.Context, teamID uuid.UUID) (int64, error) {
	var count int64
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM credentials c
			JOIN teams t ON t.id = c.team_id
			WHERE c.team_id = $1
			  AND c.kind = 'api_key'
			  AND c.status <> 'disabled'
			  AND t.status = 'active'
			  AND t.deleted_at IS NULL
		`, teamID).Scan(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("failed to count api credentials for team: %w", err)
	}
	return count, nil
}

// TouchLastUsed updates the last_used_at timestamp for an API credential.
// This should be called asynchronously to avoid blocking the request.
func (r *CredentialRepositoryImpl) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	return r.TouchLastUsedBatch(ctx, []LastUsedUpdate{{ID: id, At: time.Now().UTC()}})
}

// TouchLastUsedBatch persists the newest activity timestamp per credential in one
// system transaction. Older queued events cannot move a timestamp backwards.
func (r *CredentialRepositoryImpl) TouchLastUsedBatch(ctx context.Context, updates []LastUsedUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	updates = coalesceLastUsedUpdates(updates)
	if len(updates) == 0 {
		return nil
	}
	values := make([]string, 0, len(updates))
	args := make([]interface{}, 0, len(updates)*2)
	for _, update := range updates {
		values = append(values, "(?::uuid, ?::timestamptz)")
		args = append(args, update.ID, update.At.UTC())
	}
	if len(values) == 0 {
		return nil
	}

	// Auth-path update: callers only know the credential ID from bearer authentication,
	// so this write runs without a team-scoped transaction.
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		query := `
			UPDATE credentials AS credential
			SET last_used_at = updates.last_used_at
			FROM (VALUES ` + strings.Join(values, ",") + `) AS updates(id, last_used_at)
			WHERE credential.id = updates.id
			  AND credential.kind = 'api_key'
			  AND (credential.last_used_at IS NULL OR credential.last_used_at < updates.last_used_at)
		`
		return tx.Exec(query, args...).Error
	})

	if err != nil {
		return fmt.Errorf("failed to touch last used: %w", err)
	}

	return nil
}

func coalesceLastUsedUpdates(updates []LastUsedUpdate) []LastUsedUpdate {
	latest := make(map[uuid.UUID]time.Time, len(updates))
	for _, update := range updates {
		if update.ID == uuid.Nil || update.At.IsZero() {
			continue
		}
		at := update.At.UTC()
		if previous, ok := latest[update.ID]; !ok || at.After(previous) {
			latest[update.ID] = at
		}
	}
	coalesced := make([]LastUsedUpdate, 0, len(latest))
	for id, at := range latest {
		coalesced = append(coalesced, LastUsedUpdate{ID: id, At: at})
	}
	return coalesced
}
