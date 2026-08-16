package service

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func (s *SSOService) validCurrentSessionTeams(ctx context.Context, session *domain.SSOSession, allTeams []*domain.SSOTeamMembership) ([]domain.SSOTeamMembership, *domain.SSOTeamMembership, error) {
	teams := make([]domain.SSOTeamMembership, 0, len(allTeams))
	var selected *domain.SSOTeamMembership
	for _, team := range allTeams {
		if team == nil {
			continue
		}
		if _, err := s.ValidateMembership(ctx, &team.Membership); err != nil {
			if errors.Is(err, ErrSSOAccessDenied) || errors.Is(err, ErrSSOEntitlementRefreshStale) {
				continue
			}
			s.debugSSOFailure("sso current session team validation failed", err, append(ssoSessionLogAttrs(session), ssoMembershipLogAttrs(&team.Membership)...)...)
			return nil, nil, err
		}
		teams = append(teams, *team)
		if team.Membership.OwnerID == session.OwnerID {
			copy := *team
			selected = &copy
		}
	}
	return teams, selected, nil
}

func (s *SSOService) reconcileCurrentSessionMemberships(ctx context.Context, identity domain.SSOIdentity, existing []*domain.SSOTeamMembership) (bool, error) {
	provider, err := s.repo.GetProvider(ctx, identity.ProviderID)
	if err != nil {
		s.debugSSOFailure("sso current session provider lookup failed", err, ssoUUIDLogAttr("provider_id", identity.ProviderID), ssoHashLogAttr("subject", identity.Subject))
		return false, err
	}
	if provider == nil || !provider.Enabled || provider.RetiredAt != nil {
		return false, nil
	}
	directoryAuthority, err := s.repo.DirectoryAuthorityActive(ctx, identity.ProviderID)
	if err != nil {
		s.debugSSOFailure("sso current session directory authority lookup failed", err, ssoUUIDLogAttr("provider_id", identity.ProviderID), ssoHashLogAttr("subject", identity.Subject))
		return false, err
	}
	if directoryAuthority {
		return false, nil
	}
	cache, err := s.repo.GetEntitlementCache(ctx, identity.ProviderID, identity.Subject)
	if err != nil {
		s.debugSSOFailure("sso current session entitlement cache lookup failed", err, ssoUUIDLogAttr("provider_id", identity.ProviderID), ssoHashLogAttr("subject", identity.Subject))
		return false, err
	}
	if cache == nil || cache.Status != "active" || !cache.ExpiresAt.After(s.now()) {
		return false, nil
	}
	mappings, err := s.repo.ListMappingsForGroups(ctx, identity.ProviderID, cache.Groups)
	if err != nil {
		s.debugSSOFailure("sso current session mapping lookup failed", err, ssoUUIDLogAttr("provider_id", identity.ProviderID), ssoHashLogAttr("subject", identity.Subject), observability.Int("group_count", len(cache.Groups)), ssoHashesLogAttr("groups", cache.Groups))
		return false, err
	}
	entitlements, err := s.entitlementsFromMappings(identity.ProviderID, identity.Subject, mappings)
	if err != nil {
		if errors.Is(err, ErrSSOAccessDenied) {
			return false, nil
		}
		return false, err
	}
	existingTeamIDs := make(map[uuid.UUID]struct{}, len(existing))
	for _, team := range existing {
		if team == nil {
			continue
		}
		if teamID := team.Membership.TeamID; teamID != uuid.Nil {
			existingTeamIDs[teamID] = struct{}{}
		}
	}
	changed := false
	name := ssoMembershipName(identity.Email, identity.DisplayName, identity.ID)
	for _, entitlement := range entitlements {
		if entitlement.TeamID == uuid.Nil {
			continue
		}
		if _, ok := existingTeamIDs[entitlement.TeamID]; ok {
			continue
		}
		if _, err := s.repo.UpsertTeamMembershipForMapping(ctx, identity, entitlement, name); err != nil {
			s.debugSSOFailure("sso current session team membership upsert failed", err, ssoUUIDLogAttr("provider_id", identity.ProviderID), ssoHashLogAttr("subject", identity.Subject), ssoUUIDLogAttr("team_id", entitlement.TeamID), ssoHashLogAttr("group_id", entitlement.GroupID), observability.String("role", entitlement.Role))
			if errors.Is(err, repository.ErrTeamInactive) {
				continue
			}
			return false, err
		}
		existingTeamIDs[entitlement.TeamID] = struct{}{}
		changed = true
	}
	return changed, nil
}
