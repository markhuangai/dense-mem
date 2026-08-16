package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var (
	ErrDirectoryIdentityNotProvisioned = errors.New("directory identity is not provisioned and active")
	ErrDirectoryManagedMapping         = errors.New("directory-managed sso mappings are read-only")
	ErrSSOIdentityConflict             = errors.New("sso identity conflicts with a different external identity")
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
	DirectoryAuthorityActive(ctx context.Context, providerID uuid.UUID) (bool, error)
	DirectoryMembershipEntitled(ctx context.Context, providerID, identityID, teamID uuid.UUID, groupID string) (bool, error)

	UpsertIdentity(ctx context.Context, identity *domain.SSOIdentity) error
	GetIdentity(ctx context.Context, id uuid.UUID) (*domain.SSOIdentity, error)
	GetIdentityByProviderSubject(ctx context.Context, providerID uuid.UUID, subject string) (*domain.SSOIdentity, error)

	UpsertTeamMembershipForMapping(ctx context.Context, identity domain.SSOIdentity, mapping domain.SSOGroupMapping, name string) (*domain.Membership, error)
	ListTeamMembershipsForIdentity(ctx context.Context, identityID uuid.UUID) ([]*domain.SSOTeamMembership, error)
	GetTeamMembershipByOwnerID(ctx context.Context, id uuid.UUID) (*domain.SSOTeamMembership, error)

	GetEntitlementCache(ctx context.Context, providerID uuid.UUID, subject string) (*domain.SSOEntitlementCache, error)
	SetEntitlementCache(ctx context.Context, cache domain.SSOEntitlementCache) error

	CreateOAuthState(ctx context.Context, state domain.SSOOAuthState) error
	ConsumeOAuthState(ctx context.Context, stateHash string) (*domain.SSOOAuthState, error)
	DeleteExpiredOAuthStates(ctx context.Context, now time.Time) error

	CreateSession(ctx context.Context, session domain.SSOSession) error
	GetSession(ctx context.Context, sessionHash string) (*domain.SSOSession, error)
	UpdateSessionTeam(ctx context.Context, sessionHash string, teamID uuid.UUID) error
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
			SELECT id, name, kind, issuer_url, tenant_id, identity_claim, client_id, client_secret_env, scopes, group_claims, groups_endpoint, groups_scopes, enabled, retired_at, created_at, updated_at
			FROM sso_providers`
		if enabledOnly {
			query += ` WHERE enabled = true AND retired_at IS NULL`
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
			SELECT id, name, kind, issuer_url, tenant_id, identity_claim, client_id, client_secret_env, scopes, group_claims, groups_endpoint, groups_scopes, enabled, retired_at, created_at, updated_at
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
			INSERT INTO sso_providers (id, name, kind, issuer_url, tenant_id, identity_claim, client_id, client_secret_env, scopes, group_claims, groups_endpoint, groups_scopes, enabled, retired_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NULL, $14, $14)
		`, provider.ID, provider.Name, string(provider.Kind), provider.IssuerURL, provider.TenantID, provider.IdentityClaim, provider.ClientID, provider.ClientSecretEnv, pq.Array(provider.Scopes), pq.Array(provider.GroupClaims), provider.GroupsEndpoint, pq.Array(provider.GroupsScopes), provider.Enabled, now).Error
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
			    tenant_id = $4,
			    identity_claim = $5,
			    client_id = $6,
			    client_secret_env = $7,
			    scopes = $8,
			    group_claims = $9,
			    groups_endpoint = $10,
			    groups_scopes = $11,
			    enabled = $12,
			    retired_at = CASE WHEN $12 THEN NULL ELSE retired_at END,
			    updated_at = $13
			WHERE id = $14
		`, provider.Name, string(provider.Kind), provider.IssuerURL, provider.TenantID, provider.IdentityClaim, provider.ClientID, provider.ClientSecretEnv, pq.Array(provider.Scopes), pq.Array(provider.GroupClaims), provider.GroupsEndpoint, pq.Array(provider.GroupsScopes), provider.Enabled, now, provider.ID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if !provider.Enabled {
			return disableDirectoryProvisioningForProviderTx(tx, provider.ID, now)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update sso provider: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) DeleteProvider(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE sso_providers
			SET enabled = false, retired_at = COALESCE(retired_at, $1), updated_at = $1
			WHERE id = $2
		`, now, id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return disableDirectoryProvisioningForProviderTx(tx, id, now)
	})
	if err != nil {
		return fmt.Errorf("failed to delete sso provider: %w", err)
	}
	return nil
}

func disableDirectoryProvisioningForProviderTx(tx *gorm.DB, providerID uuid.UUID, now time.Time) error {
	if err := tx.Exec(`
		UPDATE sso_directory_connectors
		SET status = 'disabled',
		    reconcile_version = reconcile_version + 1,
		    updated_at = $1
		WHERE provider_id = $2
	`, now, providerID).Error; err != nil {
		return err
	}
	if err := tx.Exec(`
		DELETE FROM sso_directory_oauth_tokens
		WHERE connector_id IN (
			SELECT id
			FROM sso_directory_connectors
			WHERE provider_id = $1
		)
	`, providerID).Error; err != nil {
		return err
	}
	if err := tx.Exec(`
		UPDATE sso_group_mappings
		SET enabled = false,
		    retired_at = COALESCE(retired_at, $1),
		    updated_at = $1
		WHERE provider_id = $2
		  AND origin = 'directory'
	`, now, providerID).Error; err != nil {
		return err
	}
	if err := tx.Exec(`
			UPDATE team_memberships membership
			SET status = 'revoked',
			    sso_entitlement_status = 'denied',
			    sso_last_entitlement_checked_at = $1,
			    updated_at = $1
			WHERE membership.sso_provider_id = $2
			  AND membership.status = 'active'
			  AND EXISTS (
				SELECT 1
				FROM sso_group_mappings m
				WHERE m.provider_id = membership.sso_provider_id
				  AND m.team_id = membership.team_id
				  AND m.group_id = membership.sso_group_id
				  AND m.origin = 'directory'
			  )
	`, now, providerID).Error; err != nil {
		return err
	}
	return nil
}

func lockDirectoryConnectorForProviderTx(tx *gorm.DB, providerID uuid.UUID) error {
	// The selected rows are intentionally discarded; the FOR UPDATE locks serialize mapping changes with reconciliation.
	rows, err := tx.Raw(`
		SELECT id
		FROM sso_directory_connectors
		WHERE provider_id = $1
		FOR UPDATE
	`, providerID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func bumpDirectoryConnectorReconcileVersionForProviderTx(tx *gorm.DB, providerID uuid.UUID, now time.Time) error {
	return tx.Exec(`
		UPDATE sso_directory_connectors
		SET reconcile_version = reconcile_version + 1,
		    updated_at = $1
		WHERE provider_id = $2
	`, now, providerID).Error
}

func (r *SSORepositoryImpl) ListMappings(ctx context.Context, providerID uuid.UUID) ([]*domain.SSOGroupMapping, error) {
	mappings := make([]*domain.SSOGroupMapping, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT m.id, m.provider_id, m.team_id, COALESCE(t.name, ''), m.group_id, m.group_name, m.scopes, m.role, m.enabled, m.origin, m.retired_at, m.created_at, m.updated_at
			FROM sso_group_mappings m
			LEFT JOIN teams t ON t.id = m.team_id
			WHERE m.provider_id = $1
				AND m.retired_at IS NULL
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
		if err := lockDirectoryConnectorForProviderTx(tx, mapping.ProviderID); err != nil {
			return err
		}
		if err := setActiveSSOTeamMutationScope(ctx, tx, mapping.TeamID.String()); err != nil {
			return err
		}
		rows, err := tx.Raw(`
			INSERT INTO sso_group_mappings (id, provider_id, team_id, group_id, group_name, scopes, role, enabled, origin, retired_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'manual', CASE WHEN $8 THEN NULL ELSE $9::timestamptz END, $9, $9)
			ON CONFLICT (provider_id, team_id, group_id) DO UPDATE
			SET group_name = EXCLUDED.group_name,
			    scopes = EXCLUDED.scopes,
			    role = EXCLUDED.role,
			    enabled = EXCLUDED.enabled,
			    retired_at = CASE WHEN EXCLUDED.enabled THEN NULL ELSE COALESCE(sso_group_mappings.retired_at, EXCLUDED.updated_at) END,
			    updated_at = EXCLUDED.updated_at
			WHERE sso_group_mappings.origin = 'manual'
			RETURNING id, created_at, updated_at, retired_at
		`, mapping.ID, mapping.ProviderID, mapping.TeamID, mapping.GroupID, mapping.GroupName, pq.Array(mapping.Scopes), mapping.Role, mapping.Enabled, now).Rows()
		if err != nil {
			return err
		}
		if rows.Next() {
			if err := rows.Scan(&mapping.ID, &mapping.CreatedAt, &mapping.UpdatedAt, &mapping.RetiredAt); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			mapping.Origin = "manual"
			return bumpDirectoryConnectorReconcileVersionForProviderTx(tx, mapping.ProviderID, now)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		var origin string
		if err := tx.Raw(`
			SELECT origin
			FROM sso_group_mappings
			WHERE provider_id = $1 AND team_id = $2 AND group_id = $3
		`, mapping.ProviderID, mapping.TeamID, mapping.GroupID).Row().Scan(&origin); err != nil {
			return err
		}
		if origin != "manual" {
			return ErrDirectoryManagedMapping
		}
		return gorm.ErrRecordNotFound
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
		if err := lockDirectoryConnectorForProviderTx(tx, mapping.ProviderID); err != nil {
			return err
		}
		var currentTeamID string
		var origin string
		err := tx.Raw(`
			SELECT team_id::text, origin
			FROM sso_group_mappings
			WHERE id = $1
			  AND provider_id = $2
			FOR UPDATE
		`, mapping.ID, mapping.ProviderID).Row().Scan(&currentTeamID, &origin)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return gorm.ErrRecordNotFound
			}
			return err
		}
		if origin != "manual" {
			return ErrDirectoryManagedMapping
		}
		if err := setActiveSSOTeamMutationScope(ctx, tx, currentTeamID); err != nil {
			return err
		}
		if err := setActiveSSOTeamMutationScope(ctx, tx, mapping.TeamID.String()); err != nil {
			return err
		}
		res := tx.Exec(`
			UPDATE sso_group_mappings
			SET team_id = $1,
			    group_id = $2,
			    group_name = $3,
			    scopes = $4,
			    role = $5,
			    enabled = $6,
			    retired_at = CASE WHEN $6 THEN NULL ELSE COALESCE(retired_at, $7) END,
			    updated_at = $7
			WHERE id = $8 AND provider_id = $9
		`, mapping.TeamID, mapping.GroupID, mapping.GroupName, pq.Array(mapping.Scopes), mapping.Role, mapping.Enabled, now, mapping.ID, mapping.ProviderID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return bumpDirectoryConnectorReconcileVersionForProviderTx(tx, mapping.ProviderID, now)
	})
	if err != nil {
		return fmt.Errorf("failed to update sso group mapping: %w", err)
	}
	return nil
}

func (r *SSORepositoryImpl) DeleteMapping(ctx context.Context, providerID, id uuid.UUID) error {
	now := time.Now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := lockDirectoryConnectorForProviderTx(tx, providerID); err != nil {
			return err
		}
		var teamID string
		var origin string
		if err := tx.Raw(`
			SELECT team_id::text, origin
			FROM sso_group_mappings
			WHERE id = $1 AND provider_id = $2
			FOR UPDATE
		`, id, providerID).Row().Scan(&teamID, &origin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return gorm.ErrRecordNotFound
			}
			return err
		}
		if origin != "manual" {
			return ErrDirectoryManagedMapping
		}
		if err := setActiveSSOTeamMutationScope(ctx, tx, teamID); err != nil {
			return err
		}
		res := tx.Exec(`
			UPDATE sso_group_mappings
			SET enabled = false, retired_at = COALESCE(retired_at, $1), updated_at = $1
			WHERE id = $2 AND provider_id = $3
		`, now, id, providerID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return bumpDirectoryConnectorReconcileVersionForProviderTx(tx, providerID, now)
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
			SELECT m.id, m.provider_id, m.team_id, COALESCE(t.name, ''), m.group_id, m.group_name, m.scopes, m.role, m.enabled, m.origin, m.retired_at, m.created_at, m.updated_at
			FROM sso_group_mappings m
			JOIN teams t ON t.id = m.team_id
			WHERE m.provider_id = $1
				AND m.enabled = true
				AND m.retired_at IS NULL
				AND t.status = 'active'
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

func (r *SSORepositoryImpl) UpsertTeamMembershipForMapping(ctx context.Context, identity domain.SSOIdentity, mapping domain.SSOGroupMapping, name string) (*domain.Membership, error) {
	now := time.Now().UTC()
	var result *domain.Membership
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := setActiveSSOTeamMutationScope(ctx, tx, mapping.TeamID.String()); err != nil {
			return err
		}
		membershipID, ownerID, createdAt, err := upsertCanonicalSSOMembershipTx(tx, canonicalSSOMembershipInput{
			IdentityID: identity.ID, ProviderID: identity.ProviderID, TeamID: mapping.TeamID,
			Scopes: mapping.Scopes, Role: mapping.Role, GroupID: mapping.GroupID, MembershipName: name,
			LastEntitlementCheckedAt: &now, LastLoginAt: identity.LastLoginAt, Now: now,
		})
		if err != nil {
			return err
		}
		providerID := identity.ProviderID
		result = &domain.Membership{
			ID: membershipID, ActorIdentityID: identity.ID, TeamID: mapping.TeamID, OwnerID: ownerID,
			Name: name, Grants: append([]string(nil), mapping.Scopes...), Role: mapping.Role,
			Status: "active", CreatedAt: createdAt, SSOProviderID: &providerID,
			SSOSubject: identity.Subject, SSOEmail: identity.Email, SSOGroupID: mapping.GroupID,
			SSOEntitlementStatus: "active", SSOLastEntitlementCheckedAt: &now,
			SSOLastLoginAt: identity.LastLoginAt,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upsert sso team membership: %w", err)
	}
	return result, nil
}

type canonicalSSOMembershipInput struct {
	IdentityID               uuid.UUID
	ProviderID               uuid.UUID
	TeamID                   uuid.UUID
	Scopes                   []string
	Role                     string
	GroupID                  string
	MembershipName           string
	LastEntitlementCheckedAt *time.Time
	LastLoginAt              *time.Time
	Now                      time.Time
}

func upsertCanonicalSSOMembershipTx(tx *gorm.DB, input canonicalSSOMembershipInput) (uuid.UUID, uuid.UUID, time.Time, error) {
	var membershipID uuid.UUID
	var createdAt time.Time
	if err := tx.Raw(`
		INSERT INTO team_memberships (
			actor_identity_id, team_id, status, team_admin, maximum_grants,
			sso_provider_id, sso_group_id, sso_entitlement_status,
			sso_profile_name, sso_last_entitlement_checked_at, sso_last_login_at, created_at, updated_at
		) VALUES ($1, $2, 'active', $3, $4, $5, $6, 'active', $7, $8, $9, $10, $10)
		ON CONFLICT (actor_identity_id, team_id) DO UPDATE SET
			status = 'active',
			team_admin = EXCLUDED.team_admin,
			maximum_grants = EXCLUDED.maximum_grants,
			sso_provider_id = EXCLUDED.sso_provider_id,
			sso_group_id = EXCLUDED.sso_group_id,
			sso_entitlement_status = 'active',
			sso_profile_name = CASE WHEN EXCLUDED.sso_profile_name <> '' THEN EXCLUDED.sso_profile_name ELSE team_memberships.sso_profile_name END,
			sso_last_entitlement_checked_at = EXCLUDED.sso_last_entitlement_checked_at,
			sso_last_login_at = COALESCE(EXCLUDED.sso_last_login_at, team_memberships.sso_last_login_at),
			updated_at = EXCLUDED.updated_at
		RETURNING id, created_at
	`, input.IdentityID, input.TeamID, input.Role == "manager", pq.Array(input.Scopes),
		input.ProviderID, input.GroupID, input.MembershipName, input.LastEntitlementCheckedAt, input.LastLoginAt, input.Now).Row().Scan(&membershipID, &createdAt); err != nil {
		return uuid.Nil, uuid.Nil, time.Time{}, err
	}
	if err := replaceLegacyMembershipGrants(tx, membershipID, input.Scopes); err != nil {
		return uuid.Nil, uuid.Nil, time.Time{}, err
	}
	aliasID := uuid.Nil
	if err := tx.Raw(`
		SELECT legacy_owner_id
		FROM ownership_aliases
		WHERE team_id = $1 AND canonical_identity_id = $2 AND credential_id IS NULL
		ORDER BY created_at, legacy_owner_id
		LIMIT 1
	`, input.TeamID, input.IdentityID).Row().Scan(&aliasID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, uuid.Nil, time.Time{}, err
	}
	if aliasID == uuid.Nil {
		aliasID = membershipID
		if err := tx.Exec(`
			INSERT INTO ownership_aliases (
				team_id, legacy_owner_id, canonical_identity_id, credential_id, reason
			) VALUES ($1, $2, $3, NULL, 'membership')
		`, input.TeamID, aliasID, input.IdentityID).Error; err != nil {
			return uuid.Nil, uuid.Nil, time.Time{}, err
		}
	}
	return membershipID, aliasID, createdAt, nil
}

func setActiveSSOTeamMutationScope(ctx context.Context, tx *gorm.DB, teamID string) error {
	// Row locks on teams apply mutation RLS, so system SSO transactions also need the target team scope.
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_team_id', ?, true)", teamID).Error; err != nil {
		return fmt.Errorf("failed to set sso team mutation context: %w", err)
	}
	return ensureActiveTeamForMutation(ctx, tx, teamID)
}

func (r *SSORepositoryImpl) ListTeamMembershipsForIdentity(ctx context.Context, identityID uuid.UUID) ([]*domain.SSOTeamMembership, error) {
	memberships := make([]*domain.SSOTeamMembership, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT
				t.id, t.name, t.description, t.created_at, t.updated_at,
				membership.id, membership.actor_identity_id, membership.team_id,
				alias.legacy_owner_id, COALESCE(NULLIF(membership.sso_profile_name, ''), actor.display_name, ''),
				membership.maximum_grants, CASE WHEN membership.team_admin THEN 'manager' ELSE 'member' END,
				membership.status, membership.created_at, membership.sso_provider_id,
				actor.subject, COALESCE(identity.email, ''), membership.sso_group_id,
				membership.sso_entitlement_status, membership.sso_last_entitlement_checked_at, membership.sso_last_login_at
			FROM team_memberships membership
			JOIN actor_identities actor ON actor.id = membership.actor_identity_id
			JOIN LATERAL (
				SELECT legacy_owner_id
				FROM ownership_aliases
				WHERE team_id = membership.team_id
				  AND canonical_identity_id = membership.actor_identity_id
				  AND credential_id IS NULL
				ORDER BY created_at, legacy_owner_id
				LIMIT 1
			) alias ON true
			JOIN sso_identities identity ON identity.id = membership.actor_identity_id
			JOIN teams t ON t.id = membership.team_id
			WHERE membership.actor_identity_id = $1
				AND membership.status = 'active'
				AND membership.sso_entitlement_status = 'active'
				AND actor.active = true
				AND t.status = 'active'
				AND t.deleted_at IS NULL
			ORDER BY t.name ASC, alias.legacy_owner_id ASC
		`, identityID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanSSOTeamMembership(rows)
			if err != nil {
				return err
			}
			memberships = append(memberships, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list sso team memberships: %w", err)
	}
	return memberships, nil
}

func (r *SSORepositoryImpl) GetTeamMembershipByOwnerID(ctx context.Context, id uuid.UUID) (*domain.SSOTeamMembership, error) {
	var result *domain.SSOTeamMembership
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT
				t.id, t.name, t.description, t.created_at, t.updated_at,
				membership.id, membership.actor_identity_id, membership.team_id,
				alias.legacy_owner_id, COALESCE(NULLIF(membership.sso_profile_name, ''), actor.display_name, ''),
				membership.maximum_grants, CASE WHEN membership.team_admin THEN 'manager' ELSE 'member' END,
				membership.status, membership.created_at, membership.sso_provider_id,
				actor.subject, COALESCE(identity.email, ''), membership.sso_group_id,
				membership.sso_entitlement_status, membership.sso_last_entitlement_checked_at, membership.sso_last_login_at
			FROM ownership_aliases alias
			JOIN team_memberships membership
			  ON membership.team_id = alias.team_id
			 AND membership.actor_identity_id = alias.canonical_identity_id
			JOIN actor_identities actor ON actor.id = membership.actor_identity_id
			JOIN sso_identities identity ON identity.id = membership.actor_identity_id
			JOIN teams t ON t.id = membership.team_id
			WHERE alias.legacy_owner_id = $1
				AND alias.credential_id IS NULL
				AND membership.status = 'active'
				AND membership.sso_entitlement_status = 'active'
				AND actor.active = true
				AND t.status = 'active'
				AND t.deleted_at IS NULL
		`, id).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanSSOTeamMembership(rows)
		if err != nil {
			return err
		}
		result = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get sso team membership: %w", err)
	}
	return result, nil
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
		result := tx.Exec(`
			INSERT INTO sso_sessions (session_hash, identity_id, provider_id, membership_id, team_id, csrf_hash, expires_at, created_at, last_seen_at)
			SELECT $1, $2, $3, membership.id, $5, $6, $7, $8, $8
			FROM ownership_aliases alias
			JOIN team_memberships membership
			  ON membership.team_id = alias.team_id
			 AND membership.actor_identity_id = alias.canonical_identity_id
			WHERE alias.team_id = $5 AND alias.legacy_owner_id = $4
			  AND alias.credential_id IS NULL
		`, session.SessionHash, session.IdentityID, session.ProviderID, session.OwnerID, session.TeamID, session.CSRFHash, session.ExpiresAt, session.CreatedAt)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
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
			SELECT session.session_hash, session.identity_id, session.provider_id,
			       session.membership_id, alias.legacy_owner_id, session.team_id, session.csrf_hash,
			       session.expires_at, session.created_at, session.last_seen_at
			FROM sso_sessions session
			JOIN team_memberships membership ON membership.id = session.membership_id
			JOIN ownership_aliases alias
			  ON alias.team_id = membership.team_id
			 AND alias.canonical_identity_id = membership.actor_identity_id
			 AND alias.credential_id IS NULL
			WHERE session.session_hash = $1 AND session.expires_at > NOW()
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

func (r *SSORepositoryImpl) UpdateSessionTeam(ctx context.Context, sessionHash string, teamID uuid.UUID) error {
	now := time.Now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE sso_sessions session
			SET membership_id = membership.id,
			    team_id = $1,
			    last_seen_at = $2
			FROM team_memberships membership
			WHERE membership.team_id = $1
			  AND membership.actor_identity_id = session.identity_id
			  AND membership.status = 'active'
			  AND membership.sso_entitlement_status = 'active'
			  AND session.session_hash = $3
			  AND session.expires_at > $2
		`, teamID, now, sessionHash)
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
