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

// APIKeyRepository is the companion interface for team profile key data access.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type APIKeyRepository interface {
	CreateStandardKey(ctx context.Context, key *domain.APIKey) error
	ListByProfile(ctx context.Context, profileID uuid.UUID, limit, offset int) ([]*domain.APIKey, error)
	CountByProfile(ctx context.Context, profileID uuid.UUID) (int64, error)
	// GetByIDForProfile returns an API key only when it belongs to profileID. Returns nil on mismatch.
	GetByIDForProfile(ctx context.Context, profileID, id uuid.UUID) (*domain.APIKey, error)
	GetSSOOwnedKey(ctx context.Context, profileID, identityID uuid.UUID) (*domain.APIKey, error)
	GetActiveByPrefix(ctx context.Context, prefix string) (*domain.APIKey, error)
	// RevokeForProfile marks a key revoked only when it belongs to profileID. Returns number of rows affected.
	RevokeForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error)
	// DeleteForProfile hard-deletes a key only when it belongs to profileID. Returns number of rows affected.
	DeleteForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error)
	// UpdateNameForProfile renames a team profile only when it belongs to profileID.
	UpdateNameForProfile(ctx context.Context, profileID, id uuid.UUID, name string) (int64, error)
	// UpdateRoleForProfile changes a team profile role and scope set only when it belongs to profileID.
	UpdateRoleForProfile(ctx context.Context, profileID, id uuid.UUID, role string, scopes []string) (int64, error)
	// UpdateScopesForProfile changes a team profile scope set only when it belongs to profileID.
	UpdateScopesForProfile(ctx context.Context, profileID, id uuid.UUID, scopes []string) (int64, error)
	// RotateForProfile replaces key material for one team profile in place.
	RotateForProfile(ctx context.Context, profileID, id uuid.UUID, keyHash, keyPrefix, keySuffix string, expiresAt *time.Time) (int64, error)
	TouchLastUsed(ctx context.Context, id uuid.UUID) error
}

// LastUsedUpdate is one admitted credential activity timestamp.
type LastUsedUpdate struct {
	ID uuid.UUID
	At time.Time
}

// APIKeyLastUsedBatchRepository is implemented by the production repository
// so activity writers can persist coalesced timestamps in one transaction.
type APIKeyLastUsedBatchRepository interface {
	TouchLastUsedBatch(ctx context.Context, updates []LastUsedUpdate) error
}

// APIKeyRepositoryImpl implements the APIKeyRepository interface.
// Every query runs inside an RLS-aware transaction so Postgres FORCE RLS
// policies (app.current_profile_id / app.tx_mode) enforce tenant isolation
// even if a caller ever reaches the repository without the service layer.
type APIKeyRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

// Ensure APIKeyRepositoryImpl implements APIKeyRepository
var _ APIKeyRepository = (*APIKeyRepositoryImpl)(nil)

// NewAPIKeyRepository creates a new API key repository instance.
// rls is required; nil causes a panic at first use. Callers should pass
// postgres.NewRLS() for production and an RLSHelper mock for unit tests.
func NewAPIKeyRepository(db *gorm.DB, rls postgres.RLSHelper) *APIKeyRepositoryImpl {
	return &APIKeyRepositoryImpl{db: db, rls: rls}
}

// CreateStandardKey creates one API-client identity, membership, credential, and ownership alias.
func (r *APIKeyRepositoryImpl) CreateStandardKey(ctx context.Context, key *domain.APIKey) error {
	if key.ID == uuid.Nil {
		key.ID = uuid.New()
	}
	teamID := key.GetTeamID()
	name := key.GetProfileName()

	now := time.Now().UTC()
	key.CreatedAt = now

	keyPrefix := key.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = GetKeyPrefixFromHash(key.KeyHash)
	}
	keySuffix := key.KeySuffix

	err := r.rls.WithProfileTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO actor_identities (id, kind, team_id, display_name, active, created_at, updated_at)
			VALUES ($1, 'api_client', $2, $3, true, $4, $4)
		`, key.ID, teamID, name, now).Error; err != nil {
			return err
		}
		var membershipID uuid.UUID
		if err := tx.Raw(`
			INSERT INTO team_memberships (
				actor_identity_id, team_id, status, team_admin, maximum_grants, created_at, updated_at
			) VALUES ($1, $2, 'active', $3, $4, $5, $5)
			RETURNING id
		`, key.ID, teamID, key.GetRole() == "manager", pq.Array(key.Scopes), now).Row().Scan(&membershipID); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO membership_grants (membership_id, grant_name, source)
			SELECT $1, scope, 'legacy_scope' FROM unnest($2::text[]) AS scope
		`, membershipID, pq.Array(key.Scopes)).Error; err != nil {
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
		`, key.ID, key.SSOOwnerIdentityID, teamID, key.KeyHash, keyPrefix, keySuffix, name,
			pq.Array(key.Scopes), key.RateLimit, key.ExpiresAt, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO ownership_aliases (
				team_id, legacy_owner_id, canonical_identity_id, credential_id, reason
			) VALUES ($1, $2, $2, $2, 'credential')
		`, teamID, key.ID).Error
	})

	if err != nil {
		return fmt.Errorf("failed to create standard api key: %w", err)
	}

	return nil
}

// GetKeyPrefixFromHash extracts a prefix placeholder from the key hash.
// In practice, the key_prefix should be passed separately, but this helper
// extracts the first 24 chars of the hash as a fallback.
func GetKeyPrefixFromHash(hash string) string {
	if len(hash) < 24 {
		return hash
	}
	return hash[:24]
}

// ListByProfile retrieves API keys for a profile with pagination.
// Excludes the key_hash field from results.
//
// Uses *sql.Rows + pq.Array() — see GetActiveByPrefix for the rationale.
func (r *APIKeyRepositoryImpl) ListByProfile(ctx context.Context, profileID uuid.UUID, limit, offset int) ([]*domain.APIKey, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	keys := make([]*domain.APIKey, 0)
	err := r.rls.WithProfileTx(ctx, r.db, profileID.String(), func(tx *gorm.DB) error {
		rows, rerr := tx.Raw(`
				SELECT
					c.id, c.team_id, COALESCE(c.key_suffix, ''), c.name, c.scopes,
					CASE WHEN m.team_admin THEN 'manager' ELSE 'member' END, c.rate_limit,
					c.last_used_at, c.expires_at, c.created_at, c.revoked_at, 'api_key',
					'', '', COALESCE(c.owner_identity_id::text, ''), '', '', '', '', NULL, NULL
				FROM credentials c
				JOIN team_memberships m
				  ON m.actor_identity_id = c.actor_identity_id AND m.team_id = c.team_id
				JOIN teams t ON t.id = c.team_id
				WHERE c.team_id = $1
					AND c.kind = 'api_key'
					AND c.status <> 'disabled'
					AND t.status = 'active'
					AND t.deleted_at IS NULL
					ORDER BY c.created_at DESC, c.id ASC
				LIMIT $2 OFFSET $3
		`, profileID, limit, offset).Rows()
		if rerr != nil {
			return rerr
		}
		defer rows.Close()

		for rows.Next() {
			var k domain.APIKey
			var ssoIdentityID, ssoProviderID, ssoOwnerIdentityID string
			if serr := rows.Scan(
				&k.ID,
				&k.ProfileID,
				&k.KeySuffix,
				&k.Label,
				pq.Array(&k.Scopes),
				&k.Role,
				&k.RateLimit,
				&k.LastUsedAt,
				&k.ExpiresAt,
				&k.CreatedAt,
				&k.RevokedAt,
				&k.AuthSource,
				&ssoIdentityID,
				&ssoProviderID,
				&ssoOwnerIdentityID,
				&k.SSOSubject,
				&k.SSOEmail,
				&k.SSOGroupID,
				&k.SSOEntitlementStatus,
				&k.SSOLastEntitlementCheckedAt,
				&k.SSOLastLoginAt,
			); serr != nil {
				return serr
			}
			k.SSOIdentityID = parseOptionalUUID(ssoIdentityID)
			k.SSOProviderID = parseOptionalUUID(ssoProviderID)
			k.SSOOwnerIdentityID = parseOptionalUUID(ssoOwnerIdentityID)
			k.TeamID = k.ProfileID
			k.Name = k.Label
			keys = append(keys, &k)
		}
		return rows.Err()
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list api keys: %w", err)
	}
	return keys, nil
}

// GetActiveByPrefix retrieves an active (non-revoked, non-expired) API key by its prefix.
// This is used during authentication to look up the key hash for verification.
// Includes the key_hash field for verification purposes.
//
// Uses *sql.Rows + pq.Array() rather than GORM .Scan() because the pgx driver
// (via gorm.io/driver/postgres) does not route text[] values through lib/pq's
// StringArray scanner when GORM copies columns by reflection; scopes come back
// empty and authorization fails closed.
func (r *APIKeyRepositoryImpl) GetActiveByPrefix(ctx context.Context, prefix string) (*domain.APIKey, error) {
	var canonicalKey *domain.APIKey

	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		canonicalKey, err = lookupCanonicalCredential(tx, prefix)
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get api key by prefix: %w", err)
	}
	return canonicalKey, nil
}

// GetActiveByID retrieves the current authorization metadata for a standard
// API key profile without loading key material. The stable profile ID remains
// valid across in-place key rotation, while revocation and team state are
// checked on every portal-cookie request.
func (r *APIKeyRepositoryImpl) GetActiveByID(ctx context.Context, id uuid.UUID) (*domain.APIKey, error) {
	var canonicalKey *domain.APIKey

	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		canonicalKey, err = lookupCanonicalCredentialByID(tx, id)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get active api key by id: %w", err)
	}
	return canonicalKey, nil
}

// RevokeForProfile marks an API key as revoked only when it belongs to profileID.
// Returns the number of rows affected (0 means the id/profile combination did not match).
func (r *APIKeyRepositoryImpl) RevokeForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	now := time.Now().UTC()
	teamID := profileID

	// Profile-scoped revoke; UPDATE must satisfy api_keys_self_access.
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
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
		return tx.Exec(`
			UPDATE actor_identities
			SET active = false, updated_at = $1
			WHERE id = (SELECT actor_identity_id FROM credentials WHERE id = $2)
		`, now, id).Error
	})

	if err != nil {
		return 0, fmt.Errorf("failed to revoke api key for profile: %w", err)
	}

	return rowsAffected, nil
}

// DeleteForProfile removes an API key from supported reads while retaining its stable audit identity.
func (r *APIKeyRepositoryImpl) DeleteForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	teamID := profileID
	now := time.Now().UTC()
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
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
		return tx.Exec(`
			UPDATE actor_identities
			SET active = false, updated_at = $1
			WHERE id = (SELECT actor_identity_id FROM credentials WHERE id = $2)
		`, now, id).Error
	})

	if err != nil {
		return 0, fmt.Errorf("failed to delete api key for profile: %w", err)
	}

	return rowsAffected, nil
}

// UpdateNameForProfile renames a team profile without changing its API key.
func (r *APIKeyRepositoryImpl) UpdateNameForProfile(ctx context.Context, profileID, id uuid.UUID, name string) (int64, error) {
	now := time.Now().UTC()
	teamID := profileID
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
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
		return 0, fmt.Errorf("failed to update team profile name: %w", err)
	}
	return rowsAffected, nil
}

// UpdateRoleForProfile changes a team profile role and scopes without changing key material.
func (r *APIKeyRepositoryImpl) UpdateRoleForProfile(ctx context.Context, profileID, id uuid.UUID, role string, scopes []string) (int64, error) {
	now := time.Now().UTC()
	teamID := profileID
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
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
		return 0, fmt.Errorf("failed to update team profile role: %w", err)
	}
	return rowsAffected, nil
}

// UpdateScopesForProfile changes team profile scopes without changing key material.
func (r *APIKeyRepositoryImpl) UpdateScopesForProfile(ctx context.Context, profileID, id uuid.UUID, scopes []string) (int64, error) {
	now := time.Now().UTC()
	teamID := profileID
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
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
		return 0, fmt.Errorf("failed to update team profile scopes: %w", err)
	}
	return rowsAffected, nil
}

// RotateForProfile replaces the bearer secret for one team profile without
// changing the profile identity.
func (r *APIKeyRepositoryImpl) RotateForProfile(ctx context.Context, profileID, id uuid.UUID, keyHash, keyPrefix, keySuffix string, expiresAt *time.Time) (int64, error) {
	now := time.Now().UTC()
	teamID := profileID
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
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
		return 0, fmt.Errorf("failed to rotate key for team profile: %w", err)
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

const apiKeyHydrationSelect = `
		c.id, c.team_id, COALESCE(c.key_suffix, ''), c.name, c.scopes,
		CASE WHEN m.team_admin THEN 'manager' ELSE 'member' END, c.rate_limit,
		c.last_used_at, c.expires_at, c.created_at, c.revoked_at, 'api_key',
		'', '', COALESCE(c.owner_identity_id::text, ''), '', '', '', '', NULL, NULL
`

type apiKeyHydrationState struct {
	key                          domain.APIKey
	rowProfileID                 *uuid.UUID
	ssoIdentityID, ssoProviderID string
	ssoOwnerIdentityID           string
}

func (s *apiKeyHydrationState) scan(rows *sql.Rows) error {
	return rows.Scan(
		&s.key.ID, &s.rowProfileID, &s.key.KeySuffix, &s.key.Label,
		pq.Array(&s.key.Scopes), &s.key.Role, &s.key.RateLimit,
		&s.key.LastUsedAt, &s.key.ExpiresAt, &s.key.CreatedAt, &s.key.RevokedAt,
		&s.key.AuthSource, &s.ssoIdentityID, &s.ssoProviderID, &s.ssoOwnerIdentityID,
		&s.key.SSOSubject, &s.key.SSOEmail, &s.key.SSOGroupID,
		&s.key.SSOEntitlementStatus, &s.key.SSOLastEntitlementCheckedAt, &s.key.SSOLastLoginAt,
	)
}

func (s *apiKeyHydrationState) result() *domain.APIKey {
	if s.rowProfileID != nil {
		s.key.ProfileID = *s.rowProfileID
		s.key.TeamID = *s.rowProfileID
	}
	s.key.Name = s.key.Label
	s.key.SSOIdentityID = parseOptionalUUID(s.ssoIdentityID)
	s.key.SSOProviderID = parseOptionalUUID(s.ssoProviderID)
	s.key.SSOOwnerIdentityID = parseOptionalUUID(s.ssoOwnerIdentityID)
	return &s.key
}

func (r *APIKeyRepositoryImpl) getOneHydratedAPIKey(ctx context.Context, profileID uuid.UUID, query string, args ...any) (*domain.APIKey, error) {
	state := &apiKeyHydrationState{}
	found := false
	err := r.rls.WithProfileTx(ctx, r.db, profileID.String(), func(tx *gorm.DB) error {
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

// GetByIDForProfile retrieves an API key by ID only when it belongs to profileID.
func (r *APIKeyRepositoryImpl) GetByIDForProfile(ctx context.Context, profileID, id uuid.UUID) (*domain.APIKey, error) {
	key, err := r.getOneHydratedAPIKey(ctx, profileID, `
		SELECT `+apiKeyHydrationSelect+`
		FROM credentials c
		JOIN team_memberships m
		  ON m.actor_identity_id = c.actor_identity_id AND m.team_id = c.team_id
		JOIN teams t ON t.id = c.team_id
		WHERE c.id = $1 AND c.team_id = $2 AND c.kind = 'api_key' AND c.status <> 'disabled'
		  AND t.status = 'active' AND t.deleted_at IS NULL
	`, id, profileID)
	if err != nil {
		return nil, fmt.Errorf("failed to get api key for profile: %w", err)
	}
	return key, nil
}

// GetSSOOwnedKey returns the active normal API key owned by an SSO identity for one team.
func (r *APIKeyRepositoryImpl) GetSSOOwnedKey(ctx context.Context, profileID, identityID uuid.UUID) (*domain.APIKey, error) {
	key, err := r.getOneHydratedAPIKey(ctx, profileID, `
		SELECT `+apiKeyHydrationSelect+`
		FROM credentials c
		JOIN team_memberships m
		  ON m.actor_identity_id = c.actor_identity_id AND m.team_id = c.team_id
		JOIN teams t ON t.id = c.team_id
		WHERE c.team_id = $1 AND c.owner_identity_id = $2
		  AND c.kind = 'api_key' AND c.status = 'active' AND c.revoked_at IS NULL
		  AND t.status = 'active' AND t.deleted_at IS NULL
		ORDER BY c.created_at DESC, c.id ASC
		LIMIT 1
	`, profileID, identityID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sso-owned api key for profile: %w", err)
	}
	return key, nil
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

// CountByProfile returns the total number of API keys for a profile.
// Used to populate pagination totals without a second full-result scan.
func (r *APIKeyRepositoryImpl) CountByProfile(ctx context.Context, profileID uuid.UUID) (int64, error) {
	var count int64
	err := r.rls.WithProfileTx(ctx, r.db, profileID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM credentials c
			JOIN teams t ON t.id = c.team_id
			WHERE c.team_id = $1
			  AND c.kind = 'api_key'
			  AND c.status <> 'disabled'
			  AND t.status = 'active'
			  AND t.deleted_at IS NULL
		`, profileID).Scan(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("failed to count api keys for profile: %w", err)
	}
	return count, nil
}

// TouchLastUsed updates the last_used_at timestamp for an API key.
// This should be called asynchronously to avoid blocking the request.
func (r *APIKeyRepositoryImpl) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	return r.TouchLastUsedBatch(ctx, []LastUsedUpdate{{ID: id, At: time.Now().UTC()}})
}

// TouchLastUsedBatch persists the newest activity timestamp per key in one
// system transaction. Older queued events cannot move a timestamp backwards.
func (r *APIKeyRepositoryImpl) TouchLastUsedBatch(ctx context.Context, updates []LastUsedUpdate) error {
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

	// Auth-path update: callers only know the key ID from bearer authentication,
	// so this write runs without a profile-scoped transaction.
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
