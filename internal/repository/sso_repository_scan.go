package repository

import (
	"bytes"
	"database/sql"

	"github.com/lib/pq"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

const maximumSSOProtectedResourceConfigBytes = 64 * 1024

func scanSSOProvider(rows *sql.Rows) (*domain.SSOProvider, error) {
	var provider domain.SSOProvider
	var kind string
	var protectedResource []byte
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
		&protectedResource,
		&provider.Enabled,
		&provider.RetiredAt,
		&provider.CreatedAt,
		&provider.UpdatedAt,
	); err != nil {
		return nil, err
	}
	provider.Kind = domain.SSOProviderKind(kind)
	if err := jsonstrict.Decode(bytes.NewReader(protectedResource), &provider.ProtectedResource, maximumSSOProtectedResourceConfigBytes); err != nil {
		return nil, err
	}
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
		&session.MembershipID,
		&session.OwnerID,
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

func scanSSOTeamMembership(rows *sql.Rows) (*domain.SSOTeamMembership, error) {
	var item domain.SSOTeamMembership
	if err := rows.Scan(
		&item.Team.ID,
		&item.Team.Name,
		&item.Team.Description,
		&item.Team.CreatedAt,
		&item.Team.UpdatedAt,
		&item.Membership.ID,
		&item.Membership.MemorySpaceID,
		&item.Membership.MemorySpaceGeneration,
		&item.Membership.ActorIdentityID,
		&item.Membership.TeamID,
		&item.Membership.OwnerID,
		&item.Membership.Name,
		pq.Array(&item.Membership.Grants),
		&item.Membership.Role,
		&item.Membership.Status,
		&item.Membership.CreatedAt,
		&item.Membership.SSOProviderID,
		&item.Membership.SSOSubject,
		&item.Membership.SSOEmail,
		&item.Membership.SSOGroupID,
		&item.Membership.SSOEntitlementStatus,
		&item.Membership.SSOLastEntitlementCheckedAt,
		&item.Membership.SSOLastLoginAt,
	); err != nil {
		return nil, err
	}
	return &item, nil
}
