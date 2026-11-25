package access

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

var (
	ErrOAuthAccessDenied error = oauthAccessDeniedError{}
	ErrOAuthTeamRequired error = oauthTeamRequiredError{}
)

const oauthValidatorReconfigurationLimit = 4

type oauthAccessDeniedError struct{}

func (oauthAccessDeniedError) Error() string { return "oauth membership access denied" }

type oauthTeamRequiredError struct{}

func (oauthTeamRequiredError) Error() string { return "oauth team selection required" }

type OAuthProtectedResourceMetadata struct {
	AuthorizationServers []string
	ScopesSupported      []string
}

type oauthProtectedResourceState struct {
	mu          sync.RWMutex
	validator   *OAuthProtectedResourceValidator
	fingerprint [sha256.Size]byte
	configured  bool
}

type oauthProviderSnapshot struct {
	profiles           []domain.OAuthProtectedResourceProfile
	providers          map[string]*domain.SSOProvider
	unavailableIssuers map[string]struct{}
	fingerprint        [sha256.Size]byte
}

func IsJWTBearer(raw string) bool {
	parts := strings.Split(raw, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

func (s *SSOService) MCPPublicBaseURL(ctx context.Context) (string, error) {
	cfg, err := s.runtimeConfig(ctx)
	if err != nil {
		s.debugSSOFailure("mcp public base url runtime config failed", err)
		return "", err
	}
	return cfg.MCPPublicBaseURL, nil
}

func (s *SSOService) AuthenticateOAuthBearer(ctx context.Context, raw string, pathTeamID *uuid.UUID) (*domain.AuthenticatedActor, error) {
	if s == nil || s.repo == nil || !IsJWTBearer(raw) {
		return nil, ErrOAuthTokenInvalid
	}
	issuer, err := unverifiedOAuthIssuer(raw)
	if err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	snapshot, err := s.oauthProviderSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, unavailable := snapshot.unavailableIssuers[issuer]; unavailable {
		return nil, ErrOAuthProviderUnavailable
	}
	if len(snapshot.profiles) == 0 {
		return nil, ErrOAuthTokenInvalid
	}

	validated, provider, err := s.validateOAuthBearer(ctx, raw, snapshot)
	if err != nil {
		return nil, err
	}
	identitySubject, err := validatedOAuthIdentityClaim(raw, provider.IdentityClaim)
	if err != nil {
		return nil, err
	}
	identity, err := s.repo.GetIdentityByProviderSubject(ctx, provider.ID, identitySubject)
	if err != nil {
		return nil, err
	}
	if identity == nil || !identity.Active || identity.ProviderID != provider.ID || identity.Subject != identitySubject {
		return nil, ErrOAuthAccessDenied
	}

	memberships, err := s.repo.ListTeamMembershipsForIdentity(ctx, identity.ID)
	if err != nil {
		return nil, err
	}
	claimedTeamID, err := oauthValidatedTeamID(validated.Team)
	if err != nil {
		return nil, err
	}
	selected, err := selectOAuthMembership(memberships, pathTeamID, claimedTeamID)
	if err != nil {
		return nil, err
	}
	if selected.Membership.ActorIdentityID != identity.ID ||
		selected.Membership.TeamID != selected.Team.ID ||
		selected.Membership.SSOProviderID == nil ||
		*selected.Membership.SSOProviderID != provider.ID ||
		selected.Membership.SSOSubject != identity.Subject {
		return nil, ErrOAuthAccessDenied
	}

	membership, err := s.ValidateMembership(ctx, &selected.Membership)
	if err != nil {
		switch {
		case errors.Is(err, ErrSSOAccessDenied), errors.Is(err, ErrSSOProviderDisabled):
			return nil, ErrOAuthAccessDenied
		case errors.Is(err, ErrSSOEntitlementRefreshStale):
			return nil, ErrOAuthProviderUnavailable
		default:
			return nil, err
		}
	}
	membership.Grants = intersectOAuthGrants(validated.Scopes, membership.Grants)

	return &domain.AuthenticatedActor{
		Team: selected.Team,
		Identity: domain.ActorIdentity{
			ID:          identity.ID,
			Kind:        "human",
			DisplayName: identity.DisplayName,
		},
		Membership: *membership,
		OwnerID:    membership.OwnerID,
	}, nil
}

func (s *SSOService) OAuthProtectedResourceMetadata(ctx context.Context) (OAuthProtectedResourceMetadata, error) {
	if s == nil || s.repo == nil {
		return OAuthProtectedResourceMetadata{}, nil
	}
	snapshot, err := s.oauthProviderSnapshot(ctx)
	if err != nil {
		return OAuthProtectedResourceMetadata{}, err
	}
	if len(snapshot.profiles) == 0 && len(snapshot.unavailableIssuers) > 0 {
		return OAuthProtectedResourceMetadata{}, ErrOAuthProviderUnavailable
	}
	issuers := make(map[string]struct{}, len(snapshot.profiles))
	scopes := make(map[string]struct{})
	for _, profile := range snapshot.profiles {
		issuers[profile.Issuer] = struct{}{}
		for _, mapping := range profile.ProtectedResource.ScopeMappings {
			scopes[mapping.ExternalScope] = struct{}{}
		}
	}
	return OAuthProtectedResourceMetadata{
		AuthorizationServers: sortedOAuthKeys(issuers),
		ScopesSupported:      sortedOAuthKeys(scopes),
	}, nil
}

func (s *SSOService) oauthProviderSnapshot(ctx context.Context) (oauthProviderSnapshot, error) {
	providers, err := s.repo.ListEnabledProviders(ctx)
	if err != nil {
		return oauthProviderSnapshot{}, ErrOAuthProviderUnavailable
	}
	profilesByIssuer := make(map[string]domain.OAuthProtectedResourceProfile, len(providers))
	byName := make(map[string]*domain.SSOProvider, len(providers))
	unavailableIssuers := make(map[string]struct{})
	for _, provider := range providers {
		if provider == nil || provider.RetiredAt != nil || !provider.ProtectedResource.Enabled {
			continue
		}
		copyProvider := *provider
		if err := normalizeSSOProviderForWrite(&copyProvider); err != nil {
			if issuer := strings.TrimSpace(provider.IssuerURL); issuer != "" {
				unavailableIssuers[issuer] = struct{}{}
				if existing, ok := profilesByIssuer[issuer]; ok {
					delete(byName, existing.Name)
					delete(profilesByIssuer, issuer)
				}
			}
			continue
		}
		profile := oauthProfileFromSSOProvider(&copyProvider)
		if _, unavailable := unavailableIssuers[profile.Issuer]; unavailable {
			continue
		}
		if existing, duplicated := profilesByIssuer[profile.Issuer]; duplicated {
			delete(byName, existing.Name)
			delete(profilesByIssuer, profile.Issuer)
			unavailableIssuers[profile.Issuer] = struct{}{}
			continue
		}
		profilesByIssuer[profile.Issuer] = profile
		byName[profile.Name] = &copyProvider
	}
	profiles := make([]domain.OAuthProtectedResourceProfile, 0, len(profilesByIssuer))
	for _, profile := range profilesByIssuer {
		profiles = append(profiles, profile)
	}
	if len(profiles) == 0 {
		return oauthProviderSnapshot{providers: byName, unavailableIssuers: unavailableIssuers}, nil
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	if _, err := validateOAuthProfiles(profiles); err != nil {
		return oauthProviderSnapshot{}, ErrOAuthProviderUnavailable
	}
	encoded, err := json.Marshal(profiles)
	if err != nil {
		return oauthProviderSnapshot{}, ErrOAuthProviderUnavailable
	}
	return oauthProviderSnapshot{
		profiles:           profiles,
		providers:          byName,
		unavailableIssuers: unavailableIssuers,
		fingerprint:        sha256.Sum256(encoded),
	}, nil
}

func (s *SSOService) validateOAuthBearer(ctx context.Context, raw string, snapshot oauthProviderSnapshot) (*domain.OAuthValidatedToken, *domain.SSOProvider, error) {
	for attempt := 0; attempt < oauthValidatorReconfigurationLimit; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		s.oauth.mu.RLock()
		if s.oauth.configured && s.oauth.validator != nil && s.oauth.fingerprint == snapshot.fingerprint {
			validated, err := s.oauth.validator.Validate(ctx, raw)
			if err != nil {
				s.oauth.mu.RUnlock()
				return nil, nil, err
			}
			provider := snapshot.providers[validated.ProfileName]
			s.oauth.mu.RUnlock()
			if provider == nil || provider.IssuerURL != validated.Issuer {
				return nil, nil, ErrOAuthTokenInvalid
			}
			return validated, provider, nil
		}
		s.oauth.mu.RUnlock()

		s.oauth.mu.Lock()
		if err := ctx.Err(); err != nil {
			s.oauth.mu.Unlock()
			return nil, nil, err
		}
		if !s.oauth.configured || s.oauth.validator == nil || s.oauth.fingerprint != snapshot.fingerprint {
			if s.oauth.validator == nil {
				validator, err := NewOAuthProtectedResourceValidator(snapshot.profiles, OAuthProtectedResourceValidatorOptions{
					HTTPClient: s.httpClient,
					Now:        s.now,
				})
				if err != nil {
					s.oauth.mu.Unlock()
					return nil, nil, ErrOAuthProviderUnavailable
				}
				s.oauth.validator = validator
			} else if err := s.oauth.validator.Configure(snapshot.profiles); err != nil {
				s.oauth.mu.Unlock()
				return nil, nil, ErrOAuthProviderUnavailable
			}
			s.oauth.fingerprint = snapshot.fingerprint
			s.oauth.configured = true
		}
		s.oauth.mu.Unlock()
	}
	return nil, nil, ErrOAuthProviderUnavailable
}

func validatedOAuthIdentityClaim(raw, claimName string) (string, error) {
	_, claimsJSON, err := strictOAuthJWTParts(raw)
	if err != nil {
		return "", ErrOAuthTokenInvalid
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(claimsJSON, &claims); err != nil || claims == nil {
		return "", ErrOAuthTokenInvalid
	}
	return requiredExactOAuthString(claims, claimName)
}

func unverifiedOAuthIssuer(raw string) (string, error) {
	headerJSON, claimsJSON, err := strictOAuthJWTParts(raw)
	if err != nil || jsonstrict.RejectDuplicateFields(headerJSON) != nil || jsonstrict.RejectDuplicateFields(claimsJSON) != nil {
		return "", ErrOAuthTokenInvalid
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(claimsJSON, &claims); err != nil || claims == nil {
		return "", ErrOAuthTokenInvalid
	}
	return requiredExactOAuthString(claims, "iss")
}

func oauthValidatedTeamID(raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	teamID, err := uuid.Parse(raw)
	if err != nil || teamID == uuid.Nil {
		return nil, ErrOAuthTokenInvalid
	}
	return &teamID, nil
}

func selectOAuthMembership(memberships []*domain.SSOTeamMembership, pathTeamID, claimedTeamID *uuid.UUID) (*domain.SSOTeamMembership, error) {
	if pathTeamID != nil && claimedTeamID != nil && *pathTeamID != *claimedTeamID {
		return nil, ErrOAuthAccessDenied
	}
	target := pathTeamID
	if target == nil {
		target = claimedTeamID
	}
	if target == nil {
		if len(memberships) == 0 {
			return nil, ErrOAuthAccessDenied
		}
		if len(memberships) != 1 {
			return nil, ErrOAuthTeamRequired
		}
		if memberships[0] == nil {
			return nil, ErrOAuthAccessDenied
		}
		return memberships[0], nil
	}
	for _, membership := range memberships {
		if membership != nil && membership.Team.ID == *target {
			return membership, nil
		}
	}
	return nil, ErrOAuthAccessDenied
}

func intersectOAuthGrants(mapped, membership []string) []string {
	membershipSet := make(map[string]struct{}, len(membership))
	for _, grant := range membership {
		membershipSet[grant] = struct{}{}
	}
	mappedSet := make(map[string]struct{}, len(mapped))
	for _, grant := range mapped {
		if _, ok := membershipSet[grant]; ok {
			mappedSet[grant] = struct{}{}
		}
	}
	result := make([]string, 0, len(mappedSet))
	for _, grant := range []string{CredentialScopeRead, CredentialScopeWrite, CredentialScopeFeedbackRead} {
		if _, ok := mappedSet[grant]; ok {
			result = append(result, grant)
		}
	}
	return result
}

func sortedOAuthKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
