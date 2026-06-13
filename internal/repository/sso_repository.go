package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type SSORepository interface {
	ListProviders(ctx context.Context) ([]*domain.SSOProvider, error)
	ListEnabledProviders(ctx context.Context) ([]*domain.SSOProvider, error)
	GetProvider(ctx context.Context, id uuid.UUID) (*domain.SSOProvider, error)
	CreateProvider(ctx context.Context, provider *domain.SSOProvider) error
	UpdateProvider(ctx context.Context, provider *domain.SSOProvider) error
	DeleteProvider(ctx context.Context, id uuid.UUID) error

	ListMappings(ctx context.Context, providerID uuid.UUID) ([]*domain.SSOGroupMapping, error)
	CreateMapping(ctx context.Context, mapping *domain.SSOGroupMapping) error
	UpdateMapping(ctx context.Context, mapping *domain.SSOGroupMapping) error
	DeleteMapping(ctx context.Context, providerID, id uuid.UUID) error
	ListMappingsForGroups(ctx context.Context, providerID uuid.UUID, groups []string) ([]*domain.SSOGroupMapping, error)

	UpsertIdentity(ctx context.Context, identity *domain.SSOIdentity) error
	GetIdentity(ctx context.Context, id uuid.UUID) (*domain.SSOIdentity, error)
	GetIdentityByProviderSubject(ctx context.Context, providerID uuid.UUID, subject string) (*domain.SSOIdentity, error)

	UpsertTeamProfileForMapping(ctx context.Context, identity domain.SSOIdentity, mapping domain.SSOGroupMapping, name string) (*domain.APIKey, error)
	ListTeamProfilesForIdentity(ctx context.Context, identityID uuid.UUID) ([]*domain.SSOTeamProfile, error)
	GetSSOProfileByID(ctx context.Context, id uuid.UUID) (*domain.APIKey, error)

	GetEntitlementCache(ctx context.Context, providerID uuid.UUID, subject string) (*domain.SSOEntitlementCache, error)
	SetEntitlementCache(ctx context.Context, cache domain.SSOEntitlementCache) error

	CreateOAuthState(ctx context.Context, state domain.SSOOAuthState) error
	ConsumeOAuthState(ctx context.Context, stateHash string) (*domain.SSOOAuthState, error)
	DeleteExpiredOAuthStates(ctx context.Context, now time.Time) error

	CreateSession(ctx context.Context, session domain.SSOSession) error
	GetSession(ctx context.Context, sessionHash string) (*domain.SSOSession, error)
	UpdateSessionTeam(ctx context.Context, sessionHash string, teamProfileID, teamID uuid.UUID) error
	DeleteSession(ctx context.Context, sessionHash string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) error
}

type SSORepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ SSORepository = (*SSORepositoryImpl)(nil)

func NewSSORepository(db *gorm.DB, rls postgres.RLSHelper) *SSORepositoryImpl {
	return &SSORepositoryImpl{db: db, rls: rls}
}

func (r *SSORepositoryImpl) ListProviders(ctx context.Context) ([]*domain.SSOProvider, error) {
	return r.listProviders(ctx, false)
}

func (r *SSORepositoryImpl) ListEnabledProviders(ctx context.Context) ([]*domain.SSOProvider, error) {
	return r.listProviders(ctx, true)
}

func (r *SSORepositoryImpl) listProviders(ctx context.Context, enabledOnly bool) ([]*domain.SSOProvider, error) {
	providers := make([]*domain.SSOProvider, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		query := `
			SELECT id, name, kind, issuer_url, client_id, client_secret_env, scopes, group_claims, groups_endpoint, groups_scopes, enabled, created_at, updated_at
			FROM sso_providers`
		if enabledOnly {
			query += ` WHERE enabled = true`
		}
		query += ` ORDER BY name ASC, id ASC`

		rows, err := tx.Raw(query).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			provider, err := scanSSOProvider(rows)
			if err != nil {
				return err
			}
			providers = append(providers, provider)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list sso providers: %w", err)
	}
	return providers, nil
}

func (r *SSORepositoryImpl) GetProvider(ctx context.Context, id uuid.UUID) (*domain.SSOProvider, error) {
	var provider *domain.SSOProvider
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, name, kind, issuer_url, client_id, client_secret_env, scopes, group_claims, groups_endpoint, groups_scopes, enabled, created_at, updated_at
			FROM sso_providers
			WHERE id = $1
		`, id).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanSSOProvider(rows)
		if err != nil {
			return err
		}
		provider = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get sso provider: %w", err)
	}
	return provider, nil
}

func (r *SSORepositoryImpl) CreateProvider(ctx context.Context, provider *domain.SSOProvider) error {
	if provider.ID == uuid.Nil {
		provider.ID = uuid.New()
	}
	now := time.Now().UTC()
	provider.CreatedAt = now
	provider.UpdatedAt = now
	if len(provider.Scopes) == 0 {
		provider.Scopes = []string{"openid", "profile", "email"}
	}
	if len(provider.GroupClaims) == 0 {
		provider.GroupClaims = []string{"groups"}
	}
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id, client_secret_env, scopes, group_claims, groups_endpoint, groups_scopes, enabled, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
		`, provider.ID, provider.Name, string(provider.Kind), provider.IssuerURL, provider.ClientID, provider.ClientSecretEnv, pq.Array(provider.Scopes), pq.Array(provider.GroupClaims), provider.GroupsEndpoint, pq.Array(provider.GroupsScopes), provider.Enabled, now).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create sso provider: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) UpdateProvider(ctx context.Context, provider *domain.SSOProvider) error {
	now := time.Now().UTC()
	provider.UpdatedAt = now
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE sso_providers
			SET name = $1,
			    kind = $2,
			    issuer_url = $3,
			    client_id = $4,
			    client_secret_env = $5,
			    scopes = $6,
			    group_claims = $7,
			    groups_endpoint = $8,
			    groups_scopes = $9,
			    enabled = $10,
			    updated_at = $11
			WHERE id = $12
		`, provider.Name, string(provider.Kind), provider.IssuerURL, provider.ClientID, provider.ClientSecretEnv, pq.Array(provider.Scopes), pq.Array(provider.GroupClaims), provider.GroupsEndpoint, pq.Array(provider.GroupsScopes), provider.Enabled, now, provider.ID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update sso provider: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) DeleteProvider(ctx context.Context, id uuid.UUID) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM sso_providers WHERE id = $1`, id).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete sso provider: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) ListMappings(ctx context.Context, providerID uuid.UUID) ([]*domain.SSOGroupMapping, error) {
	mappings := make([]*domain.SSOGroupMapping, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT m.id, m.provider_id, m.team_id, COALESCE(t.name, ''), m.group_id, m.group_name, m.scopes, m.role, m.enabled, m.created_at, m.updated_at
			FROM sso_group_mappings m
			LEFT JOIN teams t ON t.id = m.team_id
			WHERE m.provider_id = $1
			ORDER BY t.name ASC, m.group_name ASC, m.group_id ASC
		`, providerID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			mapping, err := scanSSOGroupMapping(rows)
			if err != nil {
				return err
			}
			mappings = append(mappings, mapping)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list sso group mappings: %w", err)
	}
	return mappings, nil
}

func (r *SSORepositoryImpl) CreateMapping(ctx context.Context, mapping *domain.SSOGroupMapping) error {
	if mapping.ID == uuid.Nil {
		mapping.ID = uuid.New()
	}
	now := time.Now().UTC()
	mapping.CreatedAt = now
	mapping.UpdatedAt = now
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_group_mappings (id, provider_id, team_id, group_id, group_name, scopes, role, enabled, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		`, mapping.ID, mapping.ProviderID, mapping.TeamID, mapping.GroupID, mapping.GroupName, pq.Array(mapping.Scopes), mapping.Role, mapping.Enabled, now).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create sso group mapping: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) UpdateMapping(ctx context.Context, mapping *domain.SSOGroupMapping) error {
	now := time.Now().UTC()
	mapping.UpdatedAt = now
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE sso_group_mappings
			SET team_id = $1,
			    group_id = $2,
			    group_name = $3,
			    scopes = $4,
			    role = $5,
			    enabled = $6,
			    updated_at = $7
			WHERE id = $8 AND provider_id = $9
		`, mapping.TeamID, mapping.GroupID, mapping.GroupName, pq.Array(mapping.Scopes), mapping.Role, mapping.Enabled, now, mapping.ID, mapping.ProviderID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update sso group mapping: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) DeleteMapping(ctx context.Context, providerID, id uuid.UUID) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM sso_group_mappings WHERE id = $1 AND provider_id = $2`, id, providerID).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete sso group mapping: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) ListMappingsForGroups(ctx context.Context, providerID uuid.UUID, groups []string) ([]*domain.SSOGroupMapping, error) {
	if len(groups) == 0 {
		return []*domain.SSOGroupMapping{}, nil
	}
	mappings := make([]*domain.SSOGroupMapping, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT m.id, m.provider_id, m.team_id, COALESCE(t.name, ''), m.group_id, m.group_name, m.scopes, m.role, m.enabled, m.created_at, m.updated_at
			FROM sso_group_mappings m
			JOIN teams t ON t.id = m.team_id
			WHERE m.provider_id = $1
				AND m.enabled = true
				AND t.deleted_at IS NULL
				AND m.group_id = ANY($2)
			ORDER BY t.name ASC, m.group_name ASC, m.group_id ASC
		`, providerID, pq.Array(groups)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			mapping, err := scanSSOGroupMapping(rows)
			if err != nil {
				return err
			}
			mappings = append(mappings, mapping)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list matching sso group mappings: %w", err)
	}
	return mappings, nil
}

func (r *SSORepositoryImpl) UpsertIdentity(ctx context.Context, identity *domain.SSOIdentity) error {
	if identity.ID == uuid.Nil {
		identity.ID = uuid.New()
	}
	now := time.Now().UTC()
	if identity.LastLoginAt == nil {
		identity.LastLoginAt = &now
	}
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw(`
			INSERT INTO sso_identities (id, provider_id, subject, email, display_name, last_login_at, last_entitlement_check_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
			ON CONFLICT (provider_id, subject) DO UPDATE
			SET email = EXCLUDED.email,
			    display_name = EXCLUDED.display_name,
			    last_login_at = EXCLUDED.last_login_at,
			    last_entitlement_check_at = EXCLUDED.last_entitlement_check_at,
			    updated_at = EXCLUDED.updated_at
			RETURNING id, created_at, updated_at
		`, identity.ID, identity.ProviderID, identity.Subject, identity.Email, identity.DisplayName, identity.LastLoginAt, identity.LastEntitlementCheckAt, now).Row().Scan(&identity.ID, &identity.CreatedAt, &identity.UpdatedAt)
	})
	if err != nil {
		return fmt.Errorf("failed to upsert sso identity: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) GetIdentity(ctx context.Context, id uuid.UUID) (*domain.SSOIdentity, error) {
	return r.getIdentity(ctx, `WHERE id = $1`, id)
}

func (r *SSORepositoryImpl) GetIdentityByProviderSubject(ctx context.Context, providerID uuid.UUID, subject string) (*domain.SSOIdentity, error) {
	return r.getIdentity(ctx, `WHERE provider_id = $1 AND subject = $2`, providerID, subject)
}

func (r *SSORepositoryImpl) getIdentity(ctx context.Context, where string, args ...any) (*domain.SSOIdentity, error) {
	var identity *domain.SSOIdentity
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, provider_id, subject, email, display_name, last_login_at, last_entitlement_check_at, created_at, updated_at
			FROM sso_identities `+where, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanSSOIdentity(rows)
		if err != nil {
			return err
		}
		identity = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get sso identity: %w", err)
	}
	return identity, nil
}

func (r *SSORepositoryImpl) UpsertTeamProfileForMapping(ctx context.Context, identity domain.SSOIdentity, mapping domain.SSOGroupMapping, name string) (*domain.APIKey, error) {
	now := time.Now().UTC()
	var key *domain.APIKey
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			INSERT INTO team_profiles (
				id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role, rate_limit,
				expires_at, revoked_at, last_used_at, created_at, updated_at, auth_source,
				sso_identity_id, sso_provider_id, sso_subject, sso_email, sso_group_id,
				sso_entitlement_status, sso_last_entitlement_checked_at, sso_last_login_at
			)
			VALUES (
				gen_random_uuid(), $1, NULL, NULL, NULL, $2, $3, $4, 0,
				NULL, NULL, NULL, $5, $5, 'sso',
				$6, $7, $8, $9, $10,
				'active', $5, $5
			)
			ON CONFLICT (sso_identity_id, team_id) WHERE sso_identity_id IS NOT NULL DO UPDATE
			SET name = EXCLUDED.name,
			    scopes = EXCLUDED.scopes,
			    role = EXCLUDED.role,
			    sso_email = EXCLUDED.sso_email,
			    sso_group_id = EXCLUDED.sso_group_id,
			    sso_entitlement_status = 'active',
			    sso_last_entitlement_checked_at = EXCLUDED.sso_last_entitlement_checked_at,
			    sso_last_login_at = EXCLUDED.sso_last_login_at,
			    revoked_at = NULL,
			    updated_at = EXCLUDED.updated_at
			RETURNING
				id, team_id, COALESCE(name, ''), scopes, role, rate_limit, last_used_at, expires_at,
				created_at, revoked_at, auth_source, sso_identity_id, sso_provider_id, sso_subject,
				sso_email, sso_group_id, sso_entitlement_status, sso_last_entitlement_checked_at, sso_last_login_at
		`, mapping.TeamID, name, pq.Array(mapping.Scopes), mapping.Role, now, identity.ID, identity.ProviderID, identity.Subject, identity.Email, mapping.GroupID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanSSOAPIKey(rows)
		if err != nil {
			return err
		}
		scanned.TeamName = mapping.TeamName
		key = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upsert sso team profile: %w", err)
	}
	return key, nil
}

func (r *SSORepositoryImpl) ListTeamProfilesForIdentity(ctx context.Context, identityID uuid.UUID) ([]*domain.SSOTeamProfile, error) {
	profiles := make([]*domain.SSOTeamProfile, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT
				t.id, t.name, t.description, t.created_at, t.updated_at,
				k.id, k.team_id, COALESCE(k.name, ''), k.scopes, k.role, k.rate_limit, k.last_used_at,
				k.expires_at, k.created_at, k.revoked_at, k.auth_source, k.sso_identity_id,
				k.sso_provider_id, k.sso_subject, k.sso_email, k.sso_group_id,
				k.sso_entitlement_status, k.sso_last_entitlement_checked_at, k.sso_last_login_at
			FROM team_profiles k
			JOIN teams t ON t.id = k.team_id
			WHERE k.sso_identity_id = $1
				AND k.auth_source = 'sso'
				AND k.revoked_at IS NULL
				AND t.deleted_at IS NULL
			ORDER BY t.name ASC, k.id ASC
		`, identityID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanSSOTeamProfile(rows)
			if err != nil {
				return err
			}
			profiles = append(profiles, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list sso team profiles: %w", err)
	}
	return profiles, nil
}

func (r *SSORepositoryImpl) GetSSOProfileByID(ctx context.Context, id uuid.UUID) (*domain.APIKey, error) {
	var key *domain.APIKey
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT
				k.id, k.team_id, COALESCE(t.name, ''), COALESCE(k.name, ''), k.scopes, k.role, k.rate_limit,
				k.last_used_at, k.expires_at, k.created_at, k.revoked_at, k.auth_source, k.sso_identity_id,
				k.sso_provider_id, k.sso_subject, k.sso_email, k.sso_group_id,
				k.sso_entitlement_status, k.sso_last_entitlement_checked_at, k.sso_last_login_at
			FROM team_profiles k
			JOIN teams t ON t.id = k.team_id
			WHERE k.id = $1
				AND k.revoked_at IS NULL
				AND k.auth_source = 'sso'
				AND t.deleted_at IS NULL
		`, id).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanSSOAPIKeyWithTeamName(rows)
		if err != nil {
			return err
		}
		key = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get sso profile: %w", err)
	}
	return key, nil
}

func (r *SSORepositoryImpl) GetEntitlementCache(ctx context.Context, providerID uuid.UUID, subject string) (*domain.SSOEntitlementCache, error) {
	var cache *domain.SSOEntitlementCache
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT provider_id, subject, groups, status, checked_at, expires_at, error
			FROM sso_entitlement_cache
			WHERE provider_id = $1 AND subject = $2
		`, providerID, subject).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanSSOEntitlementCache(rows)
		if err != nil {
			return err
		}
		cache = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get sso entitlement cache: %w", err)
	}
	return cache, nil
}

func (r *SSORepositoryImpl) SetEntitlementCache(ctx context.Context, cache domain.SSOEntitlementCache) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_entitlement_cache (provider_id, subject, groups, status, checked_at, expires_at, error)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (provider_id, subject) DO UPDATE
			SET groups = EXCLUDED.groups,
			    status = EXCLUDED.status,
			    checked_at = EXCLUDED.checked_at,
			    expires_at = EXCLUDED.expires_at,
			    error = EXCLUDED.error
		`, cache.ProviderID, cache.Subject, pq.Array(cache.Groups), cache.Status, cache.CheckedAt, cache.ExpiresAt, cache.Error).Error
	})
	if err != nil {
		return fmt.Errorf("failed to set sso entitlement cache: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) CreateOAuthState(ctx context.Context, state domain.SSOOAuthState) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_oauth_states (state_hash, provider_id, pkce_verifier, nonce, redirect_path, expires_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, state.StateHash, state.ProviderID, state.PKCEVerifier, state.Nonce, state.RedirectPath, state.ExpiresAt, state.CreatedAt).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create sso oauth state: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) ConsumeOAuthState(ctx context.Context, stateHash string) (*domain.SSOOAuthState, error) {
	var state *domain.SSOOAuthState
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			DELETE FROM sso_oauth_states
			WHERE state_hash = $1 AND expires_at > NOW()
			RETURNING state_hash, provider_id, pkce_verifier, nonce, redirect_path, expires_at, created_at
		`, stateHash).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanSSOOAuthState(rows)
		if err != nil {
			return err
		}
		state = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to consume sso oauth state: %w", err)
	}
	return state, nil
}

func (r *SSORepositoryImpl) DeleteExpiredOAuthStates(ctx context.Context, now time.Time) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM sso_oauth_states WHERE expires_at <= $1`, now).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete expired sso oauth states: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) CreateSession(ctx context.Context, session domain.SSOSession) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_sessions (session_hash, identity_id, provider_id, team_profile_id, team_id, csrf_hash, expires_at, created_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		`, session.SessionHash, session.IdentityID, session.ProviderID, session.TeamProfileID, session.TeamID, session.CSRFHash, session.ExpiresAt, session.CreatedAt).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create sso session: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) GetSession(ctx context.Context, sessionHash string) (*domain.SSOSession, error) {
	var session *domain.SSOSession
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT session_hash, identity_id, provider_id, team_profile_id, team_id, csrf_hash, expires_at, created_at, last_seen_at
			FROM sso_sessions
			WHERE session_hash = $1 AND expires_at > NOW()
		`, sessionHash).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanSSOSession(rows)
		if err != nil {
			return err
		}
		session = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get sso session: %w", err)
	}
	return session, nil
}

func (r *SSORepositoryImpl) UpdateSessionTeam(ctx context.Context, sessionHash string, teamProfileID, teamID uuid.UUID) error {
	now := time.Now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE sso_sessions
			SET team_profile_id = $1,
			    team_id = $2,
			    last_seen_at = $3
			WHERE session_hash = $4 AND expires_at > $3
		`, teamProfileID, teamID, now, sessionHash)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update sso session team: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) DeleteSession(ctx context.Context, sessionHash string) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM sso_sessions WHERE session_hash = $1`, sessionHash).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete sso session: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM sso_sessions WHERE expires_at <= $1`, now).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete expired sso sessions: %w", err)
	}
	return nil
}

func scanSSOProvider(rows *sql.Rows) (*domain.SSOProvider, error) {
	var provider domain.SSOProvider
	var kind string
	if err := rows.Scan(
		&provider.ID,
		&provider.Name,
		&kind,
		&provider.IssuerURL,
		&provider.ClientID,
		&provider.ClientSecretEnv,
		pq.Array(&provider.Scopes),
		pq.Array(&provider.GroupClaims),
		&provider.GroupsEndpoint,
		pq.Array(&provider.GroupsScopes),
		&provider.Enabled,
		&provider.CreatedAt,
		&provider.UpdatedAt,
	); err != nil {
		return nil, err
	}
	provider.Kind = domain.SSOProviderKind(kind)
	return &provider, nil
}

func scanSSOGroupMapping(rows *sql.Rows) (*domain.SSOGroupMapping, error) {
	var mapping domain.SSOGroupMapping
	if err := rows.Scan(
		&mapping.ID,
		&mapping.ProviderID,
		&mapping.TeamID,
		&mapping.TeamName,
		&mapping.GroupID,
		&mapping.GroupName,
		pq.Array(&mapping.Scopes),
		&mapping.Role,
		&mapping.Enabled,
		&mapping.CreatedAt,
		&mapping.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &mapping, nil
}

func scanSSOIdentity(rows *sql.Rows) (*domain.SSOIdentity, error) {
	var identity domain.SSOIdentity
	if err := rows.Scan(
		&identity.ID,
		&identity.ProviderID,
		&identity.Subject,
		&identity.Email,
		&identity.DisplayName,
		&identity.LastLoginAt,
		&identity.LastEntitlementCheckAt,
		&identity.CreatedAt,
		&identity.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &identity, nil
}

func scanSSOEntitlementCache(rows *sql.Rows) (*domain.SSOEntitlementCache, error) {
	var cache domain.SSOEntitlementCache
	if err := rows.Scan(
		&cache.ProviderID,
		&cache.Subject,
		pq.Array(&cache.Groups),
		&cache.Status,
		&cache.CheckedAt,
		&cache.ExpiresAt,
		&cache.Error,
	); err != nil {
		return nil, err
	}
	return &cache, nil
}

func scanSSOOAuthState(rows *sql.Rows) (*domain.SSOOAuthState, error) {
	var state domain.SSOOAuthState
	if err := rows.Scan(
		&state.StateHash,
		&state.ProviderID,
		&state.PKCEVerifier,
		&state.Nonce,
		&state.RedirectPath,
		&state.ExpiresAt,
		&state.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &state, nil
}

func scanSSOSession(rows *sql.Rows) (*domain.SSOSession, error) {
	var session domain.SSOSession
	if err := rows.Scan(
		&session.SessionHash,
		&session.IdentityID,
		&session.ProviderID,
		&session.TeamProfileID,
		&session.TeamID,
		&session.CSRFHash,
		&session.ExpiresAt,
		&session.CreatedAt,
		&session.LastSeenAt,
	); err != nil {
		return nil, err
	}
	return &session, nil
}

func scanSSOAPIKey(rows *sql.Rows) (*domain.APIKey, error) {
	var key domain.APIKey
	if err := rows.Scan(
		&key.ID,
		&key.TeamID,
		&key.Label,
		pq.Array(&key.Scopes),
		&key.Role,
		&key.RateLimit,
		&key.LastUsedAt,
		&key.ExpiresAt,
		&key.CreatedAt,
		&key.RevokedAt,
		&key.AuthSource,
		&key.SSOIdentityID,
		&key.SSOProviderID,
		&key.SSOSubject,
		&key.SSOEmail,
		&key.SSOGroupID,
		&key.SSOEntitlementStatus,
		&key.SSOLastEntitlementCheckedAt,
		&key.SSOLastLoginAt,
	); err != nil {
		return nil, err
	}
	key.ProfileID = key.TeamID
	key.Name = key.Label
	return &key, nil
}

func scanSSOAPIKeyWithTeamName(rows *sql.Rows) (*domain.APIKey, error) {
	var key domain.APIKey
	if err := rows.Scan(
		&key.ID,
		&key.TeamID,
		&key.TeamName,
		&key.Label,
		pq.Array(&key.Scopes),
		&key.Role,
		&key.RateLimit,
		&key.LastUsedAt,
		&key.ExpiresAt,
		&key.CreatedAt,
		&key.RevokedAt,
		&key.AuthSource,
		&key.SSOIdentityID,
		&key.SSOProviderID,
		&key.SSOSubject,
		&key.SSOEmail,
		&key.SSOGroupID,
		&key.SSOEntitlementStatus,
		&key.SSOLastEntitlementCheckedAt,
		&key.SSOLastLoginAt,
	); err != nil {
		return nil, err
	}
	key.ProfileID = key.TeamID
	key.Name = key.Label
	return &key, nil
}

func scanSSOTeamProfile(rows *sql.Rows) (*domain.SSOTeamProfile, error) {
	var item domain.SSOTeamProfile
	if err := rows.Scan(
		&item.Team.ID,
		&item.Team.Name,
		&item.Team.Description,
		&item.Team.CreatedAt,
		&item.Team.UpdatedAt,
		&item.Profile.ID,
		&item.Profile.TeamID,
		&item.Profile.Label,
		pq.Array(&item.Profile.Scopes),
		&item.Profile.Role,
		&item.Profile.RateLimit,
		&item.Profile.LastUsedAt,
		&item.Profile.ExpiresAt,
		&item.Profile.CreatedAt,
		&item.Profile.RevokedAt,
		&item.Profile.AuthSource,
		&item.Profile.SSOIdentityID,
		&item.Profile.SSOProviderID,
		&item.Profile.SSOSubject,
		&item.Profile.SSOEmail,
		&item.Profile.SSOGroupID,
		&item.Profile.SSOEntitlementStatus,
		&item.Profile.SSOLastEntitlementCheckedAt,
		&item.Profile.SSOLastLoginAt,
	); err != nil {
		return nil, err
	}
	item.Profile.ProfileID = item.Profile.TeamID
	item.Profile.TeamName = item.Team.Name
	item.Profile.Name = item.Profile.Label
	return &item, nil
}
