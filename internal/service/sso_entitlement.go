package service

import (
	"context"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func (s *SSOService) ValidateAPIKeyPrincipal(ctx context.Context, key *domain.APIKey) (*domain.APIKey, error) {
	if key == nil {
		s.debugSSOFailure("sso api key validation key missing", ErrSSOAccessDenied, ssoAPIKeyLogAttrs(key)...)
		return nil, ErrSSOAccessDenied
	}
	if key.SSOProviderID == nil || strings.TrimSpace(key.SSOSubject) == "" {
		if ssoKeyRequiresEntitlementValidation(key) {
			s.debugSSOFailure("sso api key validation incomplete sso link", ErrSSOAccessDenied, ssoAPIKeyLogAttrs(key)...)
			return nil, ErrSSOAccessDenied
		}
		return key, nil
	}
	provider, err := s.repo.GetProvider(ctx, *key.SSOProviderID)
	if err != nil {
		s.debugSSOFailure("sso api key validation provider lookup failed", err, ssoAPIKeyLogAttrs(key)...)
		return nil, err
	}
	if provider == nil || !provider.Enabled {
		s.debugSSOProviderFailure("sso api key validation provider disabled", ErrSSOProviderDisabled, provider, ssoAPIKeyLogAttrs(key)...)
		return nil, ErrSSOProviderDisabled
	}
	directoryAuthority, err := s.repo.DirectoryAuthorityActive(ctx, provider.ID)
	if err != nil {
		s.debugSSOProviderFailure("sso api key validation directory authority lookup failed", err, provider, ssoAPIKeyLogAttrs(key)...)
		return nil, err
	}
	if directoryAuthority {
		if key.SSOIdentityID == nil {
			s.debugSSOProviderFailure("sso api key validation directory identity missing", ErrSSOAccessDenied, provider, ssoAPIKeyLogAttrs(key)...)
			return nil, ErrSSOAccessDenied
		}
		entitled, err := s.repo.DirectoryTeamProfileEntitled(ctx, key.ID, provider.ID, *key.SSOIdentityID, key.GetTeamID(), key.SSOGroupID)
		if err != nil {
			s.debugSSOProviderFailure("sso api key validation directory membership lookup failed", err, provider, ssoAPIKeyLogAttrs(key)...)
			return nil, err
		}
		if !entitled {
			s.debugSSOProviderFailure("sso api key validation directory team entitlement missing", ErrSSOAccessDenied, provider, ssoAPIKeyLogAttrs(key)...)
			return nil, ErrSSOAccessDenied
		}
		key.SSOEntitlementStatus = "active"
		return key, nil
	}

	cache, err := s.repo.GetEntitlementCache(ctx, provider.ID, key.SSOSubject)
	if err != nil {
		s.debugSSOProviderFailure("sso api key validation entitlement cache lookup failed", err, provider, ssoAPIKeyLogAttrs(key)...)
		return nil, err
	}
	now := s.now()
	if cache == nil || cache.ExpiresAt.Before(now) || cache.ExpiresAt.Equal(now) {
		runtime, err := s.runtimeConfig(ctx)
		if err != nil {
			s.debugSSOProviderFailure("sso api key validation runtime config failed", err, provider, ssoAPIKeyLogAttrs(key)...)
			return nil, err
		}
		providerCtx, cancel := s.providerContext(ctx, runtime)
		groups, refreshErr := s.resolveGroups(providerCtx, runtime, *provider, key.SSOSubject, "")
		cancel()
		if refreshErr != nil {
			s.debugSSOProviderFailure("sso api key validation group refresh failed", refreshErr, provider, ssoAPIKeyLogAttrs(key)...)
			if cacheErr := s.storeEntitlementCacheWithTTL(ctx, runtime.EntitlementCacheTTL, provider.ID, key.SSOSubject, nil, "error", refreshErr.Error()); cacheErr != nil {
				s.debugSSOProviderFailure("sso api key validation refresh error cache store failed", cacheErr, provider, ssoAPIKeyLogAttrs(key)...)
			}
			return nil, ErrSSOEntitlementRefreshStale
		}
		cache = &domain.SSOEntitlementCache{
			ProviderID: provider.ID,
			Subject:    key.SSOSubject,
			Groups:     groups,
			Status:     "active",
			CheckedAt:  now,
			ExpiresAt:  now.Add(runtime.EntitlementCacheTTL),
		}
		mappings, err := s.repo.ListMappingsForGroups(ctx, provider.ID, groups)
		if err != nil {
			s.debugSSOProviderFailure("sso api key validation mapping lookup failed", err, provider, append(ssoAPIKeyLogAttrs(key), observability.Int("group_count", len(groups)), ssoHashesLogAttr("groups", groups))...)
			return nil, err
		}
		if !hasMappingForTeam(mappings, key.GetTeamID()) {
			cache.Status = "denied"
		}
		if err := s.repo.SetEntitlementCache(ctx, *cache); err != nil {
			s.debugSSOProviderFailure("sso api key validation entitlement cache store failed", err, provider, append(ssoAPIKeyLogAttrs(key), observability.String("cache_status", cache.Status), observability.Int("group_count", len(cache.Groups)), ssoHashesLogAttr("groups", cache.Groups))...)
			return nil, err
		}
	}
	if cache.Status != "active" {
		s.debugSSOProviderFailure("sso api key validation cache denied", ErrSSOAccessDenied, provider, append(ssoAPIKeyLogAttrs(key), observability.String("cache_status", cache.Status), observability.Int("group_count", len(cache.Groups)), ssoHashesLogAttr("groups", cache.Groups))...)
		return nil, ErrSSOAccessDenied
	}
	mappings, err := s.repo.ListMappingsForGroups(ctx, provider.ID, cache.Groups)
	if err != nil {
		s.debugSSOProviderFailure("sso api key validation cached mapping lookup failed", err, provider, append(ssoAPIKeyLogAttrs(key), observability.Int("group_count", len(cache.Groups)), ssoHashesLogAttr("groups", cache.Groups))...)
		return nil, err
	}
	entitlement, ok := mergedEntitlementForTeam(mappings, key.GetTeamID())
	if !ok {
		s.debugSSOProviderFailure("sso api key validation team entitlement missing", ErrSSOAccessDenied, provider, append(ssoAPIKeyLogAttrs(key), observability.Int("mapping_count", len(mappings)), observability.Int("group_count", len(cache.Groups)), ssoHashesLogAttr("groups", cache.Groups))...)
		return nil, ErrSSOAccessDenied
	}
	key.Scopes = entitlement.Scopes
	key.Role = entitlement.Role
	key.SSOGroupID = entitlement.GroupID
	key.SSOEntitlementStatus = "active"
	return key, nil
}
