package repository

import (
	"context"
	"fmt"
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
	GetActiveByPrefix(ctx context.Context, prefix string) (*domain.APIKey, error)
	// RevokeForProfile marks a key revoked only when it belongs to profileID. Returns number of rows affected.
	RevokeForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error)
	// DeleteForProfile hard-deletes a key only when it belongs to profileID. Returns number of rows affected.
	DeleteForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error)
	// UpdateNameForProfile renames a team profile only when it belongs to profileID.
	UpdateNameForProfile(ctx context.Context, profileID, id uuid.UUID, name string) (int64, error)
	// UpdateRoleForProfile changes a team profile role only when it belongs to profileID.
	UpdateRoleForProfile(ctx context.Context, profileID, id uuid.UUID, role string) (int64, error)
	// RotateForProfile replaces key material for one team profile in place.
	RotateForProfile(ctx context.Context, profileID, id uuid.UUID, keyHash, keyPrefix, keySuffix string, expiresAt *time.Time) (int64, error)
	TouchLastUsed(ctx context.Context, id uuid.UUID) error
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

// CreateStandardKey creates a new standard API key associated with a team profile.
func (r *APIKeyRepositoryImpl) CreateStandardKey(ctx context.Context, key *domain.APIKey) error {
	if key.ID == uuid.Nil {
		key.ID = uuid.New()
	}
	teamID := key.GetTeamID()
	name := key.GetProfileName()

	now := time.Now().UTC()
	key.CreatedAt = now

	// Standard keys must have a team_id.
	// Use the KeyPrefix field from the domain object (derived from raw key)
	keyPrefix := key.KeyPrefix
	if keyPrefix == "" {
		// Fallback: derive from hash (incorrect but legacy support)
		keyPrefix = GetKeyPrefixFromHash(key.KeyHash)
	}
	keySuffix := key.KeySuffix

	// INSERT must satisfy team_profiles_self_access (team_id = app.current_team_id);
	// set the session to the owning team so the RLS WITH CHECK passes.
	// Scopes must be wrapped in pq.Array — the pgx driver (via gorm.io/driver/postgres)
	// does not encode a naked []string as Postgres text[]; it writes NULL and the
	// authorization layer later sees an empty scope set.
	err := r.rls.WithProfileTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_profiles (id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role, rate_limit, expires_at, revoked_at, last_used_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9, $10, NULL, NULL, $11, $11)
		`, key.ID, teamID, key.KeyHash, keyPrefix, keySuffix, name, pq.Array(key.Scopes), key.GetRole(), key.RateLimit, key.ExpiresAt, now).Error
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
					id, team_id, COALESCE(key_suffix, ''), name, scopes, role, rate_limit,
					last_used_at, expires_at, created_at, revoked_at, auth_source,
					COALESCE(sso_identity_id::text, ''), COALESCE(sso_provider_id::text, ''),
					COALESCE(sso_subject, ''), sso_email, sso_group_id, sso_entitlement_status,
					sso_last_entitlement_checked_at, sso_last_login_at
				FROM team_profiles
				WHERE team_id = $1
				ORDER BY created_at DESC, id ASC
			LIMIT $2 OFFSET $3
		`, profileID, limit, offset).Rows()
		if rerr != nil {
			return rerr
		}
		defer rows.Close()

		for rows.Next() {
			var k domain.APIKey
			var ssoIdentityID, ssoProviderID string
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
	var key domain.APIKey
	var teamID *uuid.UUID
	var ssoIdentityID, ssoProviderID string
	found := false

	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, rerr := tx.Raw(`
			SELECT
				k.id,
				k.team_id,
				COALESCE(t.name, ''),
					k.key_hash,
					COALESCE(k.key_suffix, ''),
					k.name,
					k.scopes,
					k.role,
					k.rate_limit,
					k.last_used_at,
					k.expires_at,
					k.created_at,
					k.revoked_at,
					k.auth_source,
					COALESCE(k.sso_identity_id::text, ''),
					COALESCE(k.sso_provider_id::text, ''),
					COALESCE(k.sso_subject, ''),
					k.sso_email,
					k.sso_group_id,
					k.sso_entitlement_status,
					k.sso_last_entitlement_checked_at,
					k.sso_last_login_at
				FROM team_profiles k
				LEFT JOIN teams t ON t.id = k.team_id
				WHERE k.key_prefix = $1
					AND k.key_hash IS NOT NULL
					AND k.revoked_at IS NULL
					AND (k.expires_at IS NULL OR k.expires_at > NOW())
			`, prefix).Rows()
		if rerr != nil {
			return rerr
		}
		defer rows.Close()

		if rows.Next() {
			found = true
			return rows.Scan(
				&key.ID,
				&teamID,
				&key.TeamName,
				&key.KeyHash,
				&key.KeySuffix,
				&key.Label,
				pq.Array(&key.Scopes),
				&key.Role,
				&key.RateLimit,
				&key.LastUsedAt,
				&key.ExpiresAt,
				&key.CreatedAt,
				&key.RevokedAt,
				&key.AuthSource,
				&ssoIdentityID,
				&ssoProviderID,
				&key.SSOSubject,
				&key.SSOEmail,
				&key.SSOGroupID,
				&key.SSOEntitlementStatus,
				&key.SSOLastEntitlementCheckedAt,
				&key.SSOLastLoginAt,
			)
		}
		return rows.Err()
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get api key by prefix: %w", err)
	}
	if !found {
		return nil, nil
	}
	if teamID != nil {
		key.ProfileID = *teamID
		key.TeamID = *teamID
	}
	key.Name = key.Label
	key.SSOIdentityID = parseOptionalUUID(ssoIdentityID)
	key.SSOProviderID = parseOptionalUUID(ssoProviderID)
	return &key, nil
}

// RevokeForProfile marks an API key as revoked only when it belongs to profileID.
// Returns the number of rows affected (0 means the id/profile combination did not match).
func (r *APIKeyRepositoryImpl) RevokeForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	now := time.Now().UTC()

	// Profile-scoped revoke; UPDATE must satisfy api_keys_self_access.
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, profileID.String(), func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE team_profiles
			SET revoked_at = $1, updated_at = $1
			WHERE id = $2 AND team_id = $3 AND revoked_at IS NULL
		`, now, id, profileID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("failed to revoke api key for profile: %w", err)
	}

	return rowsAffected, nil
}

// DeleteForProfile hard-deletes an API key only when it belongs to profileID.
func (r *APIKeyRepositoryImpl) DeleteForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, profileID.String(), func(tx *gorm.DB) error {
		res := tx.Exec(`
			DELETE FROM team_profiles
			WHERE id = $1 AND team_id = $2
		`, id, profileID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("failed to delete api key for profile: %w", err)
	}

	return rowsAffected, nil
}

// UpdateNameForProfile renames a team profile without changing its API key.
func (r *APIKeyRepositoryImpl) UpdateNameForProfile(ctx context.Context, profileID, id uuid.UUID, name string) (int64, error) {
	now := time.Now().UTC()
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, profileID.String(), func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE team_profiles
			SET name = $1,
			    updated_at = $2
			WHERE id = $3 AND team_id = $4
		`, name, now, id, profileID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to update team profile name: %w", err)
	}
	return rowsAffected, nil
}

// UpdateRoleForProfile changes a team profile role without changing key material.
func (r *APIKeyRepositoryImpl) UpdateRoleForProfile(ctx context.Context, profileID, id uuid.UUID, role string) (int64, error) {
	now := time.Now().UTC()
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, profileID.String(), func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE team_profiles
			SET role = $1,
			    updated_at = $2
			WHERE id = $3 AND team_id = $4
		`, role, now, id, profileID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to update team profile role: %w", err)
	}
	return rowsAffected, nil
}

// RotateForProfile replaces the bearer secret for one team profile without
// changing the profile identity.
func (r *APIKeyRepositoryImpl) RotateForProfile(ctx context.Context, profileID, id uuid.UUID, keyHash, keyPrefix, keySuffix string, expiresAt *time.Time) (int64, error) {
	now := time.Now().UTC()
	var rowsAffected int64
	err := r.rls.WithProfileTx(ctx, r.db, profileID.String(), func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE team_profiles
			SET key_hash = $1,
			    key_prefix = $2,
			    key_suffix = NULLIF($3, ''),
			    expires_at = $4,
			    revoked_at = NULL,
			    last_used_at = NULL,
			    updated_at = $5
			WHERE id = $6 AND team_id = $7
		`, keyHash, keyPrefix, keySuffix, expiresAt, now, id, profileID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to rotate key for team profile: %w", err)
	}
	return rowsAffected, nil
}

// GetByIDForProfile retrieves an API key by ID only when it belongs to profileID.
// Returns nil when the id/profile combination does not match (prevents existence oracle).
// Excludes the key_hash field from results.
//
// Uses *sql.Rows + pq.Array() — see GetActiveByPrefix for the rationale.
func (r *APIKeyRepositoryImpl) GetByIDForProfile(ctx context.Context, profileID, id uuid.UUID) (*domain.APIKey, error) {
	var key domain.APIKey
	var rowProfileID *uuid.UUID
	var ssoIdentityID, ssoProviderID string
	found := false

	err := r.rls.WithProfileTx(ctx, r.db, profileID.String(), func(tx *gorm.DB) error {
		rows, rerr := tx.Raw(`
				SELECT
					id, team_id, COALESCE(key_suffix, ''), name, scopes, role, rate_limit,
					last_used_at, expires_at, created_at, revoked_at, auth_source,
					COALESCE(sso_identity_id::text, ''), COALESCE(sso_provider_id::text, ''),
					COALESCE(sso_subject, ''), sso_email, sso_group_id, sso_entitlement_status,
					sso_last_entitlement_checked_at, sso_last_login_at
				FROM team_profiles
				WHERE id = $1 AND team_id = $2
			`, id, profileID).Rows()
		if rerr != nil {
			return rerr
		}
		defer rows.Close()

		if rows.Next() {
			found = true
			return rows.Scan(
				&key.ID,
				&rowProfileID,
				&key.KeySuffix,
				&key.Label,
				pq.Array(&key.Scopes),
				&key.Role,
				&key.RateLimit,
				&key.LastUsedAt,
				&key.ExpiresAt,
				&key.CreatedAt,
				&key.RevokedAt,
				&key.AuthSource,
				&ssoIdentityID,
				&ssoProviderID,
				&key.SSOSubject,
				&key.SSOEmail,
				&key.SSOGroupID,
				&key.SSOEntitlementStatus,
				&key.SSOLastEntitlementCheckedAt,
				&key.SSOLastLoginAt,
			)
		}
		return rows.Err()
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get api key for profile: %w", err)
	}
	if !found {
		return nil, nil
	}
	if rowProfileID != nil {
		key.ProfileID = *rowProfileID
		key.TeamID = *rowProfileID
	}
	key.Name = key.Label
	key.SSOIdentityID = parseOptionalUUID(ssoIdentityID)
	key.SSOProviderID = parseOptionalUUID(ssoProviderID)
	return &key, nil
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
			SELECT COUNT(*) FROM team_profiles WHERE team_id = $1
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
	now := time.Now().UTC()

	// Auth-path update: callers only know the key ID from bearer authentication,
	// so this write runs without a profile-scoped transaction.
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE team_profiles
			SET last_used_at = $1
			WHERE id = $2
		`, now, id).Error
	})

	if err != nil {
		return fmt.Errorf("failed to touch last used: %w", err)
	}

	return nil
}
