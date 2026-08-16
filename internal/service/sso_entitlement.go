package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type ssoEntitlementSubject struct {
	ProviderID uuid.UUID
	IdentityID uuid.UUID
	TeamID     uuid.UUID
	Subject    string
	GroupID    string
}

func (s *SSOService) ValidateCredential(ctx context.Context, credential *domain.Credential) (*domain.Credential, error) {
	if credential == nil {
		s.debugSSOFailure("sso credential validation credential missing", ErrSSOAccessDenied, ssoCredentialLogAttrs(credential)...)
		return nil, ErrSSOAccessDenied
	}
	if credential.OwnerIdentityID == nil {
		return credential, nil
	}
	if credential.SSOProviderID == nil || strings.TrimSpace(credential.SSOSubject) == "" {
		s.debugSSOFailure("sso credential validation incomplete owner link", ErrSSOAccessDenied, ssoCredentialLogAttrs(credential)...)
		return nil, ErrSSOAccessDenied
	}

	entitlement, err := s.validateSSOEntitlement(ctx, ssoEntitlementSubject{
		ProviderID: *credential.SSOProviderID,
		IdentityID: *credential.OwnerIdentityID,
		TeamID:     credential.TeamID,
		Subject:    credential.SSOSubject,
		GroupID:    credential.SSOGroupID,
	}, ssoCredentialLogAttrs(credential))
	if err != nil {
		return nil, err
	}
	if entitlement != nil {
		credential.Scopes = intersectSSOGrants(credential.Scopes, entitlement.Scopes)
		credential.Role = restrictedSSORole(credential.Role, entitlement.Role)
		credential.SSOGroupID = entitlement.GroupID
	}
	if !hasCredentialScope(credential.Scopes, CredentialScopeRead) {
		return nil, ErrSSOAccessDenied
	}
	credential.SSOEntitlementStatus = "active"
	return credential, nil
}

func (s *SSOService) ValidateMembership(ctx context.Context, membership *domain.Membership) (*domain.Membership, error) {
	if membership == nil || membership.SSOProviderID == nil || membership.ActorIdentityID == uuid.Nil || strings.TrimSpace(membership.SSOSubject) == "" {
		s.debugSSOFailure("sso membership validation incomplete membership", ErrSSOAccessDenied, ssoMembershipLogAttrs(membership)...)
		return nil, ErrSSOAccessDenied
	}
	entitlement, err := s.validateSSOEntitlement(ctx, ssoEntitlementSubject{
		ProviderID: *membership.SSOProviderID,
		IdentityID: membership.ActorIdentityID,
		TeamID:     membership.TeamID,
		Subject:    membership.SSOSubject,
		GroupID:    membership.SSOGroupID,
	}, ssoMembershipLogAttrs(membership))
	if err != nil {
		return nil, err
	}
	if entitlement != nil {
		membership.Grants = intersectSSOGrants(membership.Grants, entitlement.Scopes)
		membership.Role = restrictedSSORole(membership.Role, entitlement.Role)
		membership.SSOGroupID = entitlement.GroupID
	}
	if !hasCredentialScope(membership.Grants, CredentialScopeRead) {
		return nil, ErrSSOAccessDenied
	}
	membership.SSOEntitlementStatus = "active"
	return membership, nil
}

func (s *SSOService) validateSSOEntitlement(ctx context.Context, subject ssoEntitlementSubject, attrs []observability.LogAttr) (*teamEntitlement, error) {
	provider, err := s.repo.GetProvider(ctx, subject.ProviderID)
	if err != nil {
		s.debugSSOFailure("sso entitlement provider lookup failed", err, attrs...)
		return nil, err
	}
	if provider == nil || !provider.Enabled || provider.RetiredAt != nil {
		s.debugSSOProviderFailure("sso entitlement provider disabled", ErrSSOProviderDisabled, provider, attrs...)
		return nil, ErrSSOProviderDisabled
	}
	directoryAuthority, err := s.repo.DirectoryAuthorityActive(ctx, provider.ID)
	if err != nil {
		s.debugSSOProviderFailure("sso entitlement directory authority lookup failed", err, provider, attrs...)
		return nil, err
	}
	if directoryAuthority {
		entitled, err := s.repo.DirectoryMembershipEntitled(ctx, provider.ID, subject.IdentityID, subject.TeamID, subject.GroupID)
		if err != nil {
			s.debugSSOProviderFailure("sso entitlement directory membership lookup failed", err, provider, attrs...)
			return nil, err
		}
		if !entitled {
			s.debugSSOProviderFailure("sso entitlement directory membership missing", ErrSSOAccessDenied, provider, attrs...)
			return nil, ErrSSOAccessDenied
		}
		return nil, nil
	}

	cache, err := s.repo.GetEntitlementCache(ctx, provider.ID, subject.Subject)
	if err != nil {
		s.debugSSOProviderFailure("sso entitlement cache lookup failed", err, provider, attrs...)
		return nil, err
	}
	now := s.now()
	if cache == nil || !cache.ExpiresAt.After(now) {
		runtime, err := s.runtimeConfig(ctx)
		if err != nil {
			s.debugSSOProviderFailure("sso entitlement runtime config failed", err, provider, attrs...)
			return nil, err
		}
		providerCtx, cancel := s.providerContext(ctx, runtime)
		groups, refreshErr := s.resolveGroups(providerCtx, runtime, *provider, subject.Subject, "")
		cancel()
		if refreshErr != nil {
			s.debugSSOProviderFailure("sso entitlement group refresh failed", refreshErr, provider, attrs...)
			if cacheErr := s.storeEntitlementCacheWithTTL(ctx, runtime.EntitlementCacheTTL, provider.ID, subject.Subject, nil, "error", refreshErr.Error()); cacheErr != nil {
				s.debugSSOProviderFailure("sso entitlement refresh error cache store failed", cacheErr, provider, attrs...)
			}
			return nil, ErrSSOEntitlementRefreshStale
		}
		cache = &domain.SSOEntitlementCache{
			ProviderID: provider.ID,
			Subject:    subject.Subject,
			Groups:     groups,
			Status:     "active",
			CheckedAt:  now,
			ExpiresAt:  now.Add(runtime.EntitlementCacheTTL),
		}
		mappings, err := s.repo.ListMappingsForGroups(ctx, provider.ID, groups)
		if err != nil {
			s.debugSSOProviderFailure("sso entitlement mapping lookup failed", err, provider, append(attrs, observability.Int("group_count", len(groups)))...)
			return nil, err
		}
		if !hasMappingForTeam(mappings, subject.TeamID) {
			cache.Status = "denied"
		}
		if err := s.repo.SetEntitlementCache(ctx, *cache); err != nil {
			s.debugSSOProviderFailure("sso entitlement cache store failed", err, provider, append(attrs, observability.String("cache_status", cache.Status))...)
			return nil, err
		}
	}
	if cache.Status != "active" {
		s.debugSSOProviderFailure("sso entitlement cache denied", ErrSSOAccessDenied, provider, append(attrs, observability.String("cache_status", cache.Status))...)
		return nil, ErrSSOAccessDenied
	}
	mappings, err := s.repo.ListMappingsForGroups(ctx, provider.ID, cache.Groups)
	if err != nil {
		s.debugSSOProviderFailure("sso entitlement cached mapping lookup failed", err, provider, append(attrs, observability.Int("group_count", len(cache.Groups)))...)
		return nil, err
	}
	entitlement, ok := mergedEntitlementForTeam(mappings, subject.TeamID)
	if !ok {
		s.debugSSOProviderFailure("sso entitlement team mapping missing", ErrSSOAccessDenied, provider, append(attrs, observability.Int("mapping_count", len(mappings)))...)
		return nil, ErrSSOAccessDenied
	}
	return &entitlement, nil
}

func intersectSSOGrants(maximum, entitled []string) []string {
	allowed := make(map[string]struct{}, len(entitled))
	for _, grant := range entitled {
		allowed[grant] = struct{}{}
	}
	result := make([]string, 0, len(maximum))
	for _, grant := range maximum {
		if _, ok := allowed[grant]; ok {
			result = append(result, grant)
		}
	}
	return result
}

func restrictedSSORole(maximum, entitled string) string {
	if maximum == CredentialRoleManager && entitled == CredentialRoleManager {
		return CredentialRoleManager
	}
	return CredentialRoleMember
}
