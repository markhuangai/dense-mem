package repository

import (
	"database/sql"

	"github.com/lib/pq"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func scanSSOProvider(rows *sql.Rows) (*domain.SSOProvider, error) {
	var provider domain.SSOProvider
	var kind string
	if err := rows.Scan(
		&provider.ID,
		&provider.Name,
		&kind,
		&provider.IssuerURL,
		&provider.TenantID,
		&provider.IdentityClaim,
		&provider.ClientID,
		&provider.ClientSecretEnv,
		pq.Array(&provider.Scopes),
		pq.Array(&provider.GroupClaims),
		&provider.GroupsEndpoint,
		pq.Array(&provider.GroupsScopes),
		&provider.Enabled,
		&provider.RetiredAt,
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
		&mapping.Origin,
		&mapping.RetiredAt,
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
		&identity.ExternalID,
		&identity.Email,
		&identity.DisplayName,
		&identity.Active,
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
