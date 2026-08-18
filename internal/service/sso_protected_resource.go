package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

var (
	ErrOAuthTokenInvalid        = errors.New("oauth access token invalid")
	ErrOAuthTokenExpired        = errors.New("oauth access token expired")
	ErrOAuthAccessDenied        = errors.New("oauth membership access denied")
	ErrOAuthTeamRequired        = errors.New("oauth team selection required")
	ErrOAuthProviderUnavailable = errors.New("oauth provider unavailable")
)

type OAuthProtectedResourceMetadata struct {
	AuthorizationServers []string
	ScopesSupported      []string
}

func IsJWTBearer(raw string) bool {
	parts := strings.Split(raw, ".")
	return len(parts) == 3
}

func (s *SSOService) AuthenticateOAuthBearer(ctx context.Context, raw string, pathTeamID *uuid.UUID) (*domain.AuthenticatedActor, error) {
	if s == nil || s.repo == nil || !IsJWTBearer(raw) {
		return nil, ErrOAuthTokenInvalid
	}
	headerJSON, payloadJSON, err := strictJWTParts(raw)
	if err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	if err := jsonstrict.RejectDuplicateFields(headerJSON); err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	if err := jsonstrict.RejectDuplicateFields(payloadJSON); err != nil {
		return nil, ErrOAuthTokenInvalid
	}

	var unverified map[string]json.RawMessage
	if err := json.Unmarshal(payloadJSON, &unverified); err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	issuer, err := requiredExactStringClaim(unverified, "iss")
	if err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	provider, err := s.oauthProviderForIssuer(ctx, issuer)
	if err != nil {
		return nil, err
	}

	algorithms := make([]jose.SignatureAlgorithm, 0, len(provider.ProtectedResource.Algorithms))
	for _, algorithm := range provider.ProtectedResource.Algorithms {
		algorithms = append(algorithms, jose.SignatureAlgorithm(algorithm))
	}
	parsed, err := jwt.ParseSigned(raw, algorithms)
	if err != nil || len(parsed.Headers) != 1 {
		return nil, ErrOAuthTokenInvalid
	}
	header := parsed.Headers[0]
	key, err := s.oauthVerificationKey(ctx, *provider, header.KeyID, header.Algorithm)
	if err != nil {
		return nil, err
	}

	var claims jwt.Claims
	var verifiedRaw map[string]json.RawMessage
	if err := parsed.Claims(key, &claims, &verifiedRaw); err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	if claims.Subject == "" || claims.Expiry == nil || len(claims.Audience) == 0 {
		return nil, ErrOAuthTokenInvalid
	}
	validationErr := claims.ValidateWithLeeway(jwt.Expected{
		Issuer:      provider.IssuerURL,
		AnyAudience: jwt.Audience(provider.ProtectedResource.Audiences),
		Time:        s.now().UTC(),
	}, time.Minute)
	if validationErr != nil {
		if errors.Is(validationErr, jwt.ErrExpired) {
			return nil, ErrOAuthTokenExpired
		}
		return nil, ErrOAuthTokenInvalid
	}
	identitySubject := claims.Subject
	if provider.IdentityClaim != "sub" {
		identitySubject, err = requiredStringClaim(verifiedRaw, provider.IdentityClaim)
		if err != nil {
			return nil, ErrOAuthTokenInvalid
		}
	}
	externalScopes, err := oauthScopeClaim(verifiedRaw, provider.ProtectedResource.ScopeClaim)
	if err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	claimedTeamID, err := oauthTeamClaim(verifiedRaw, provider.ProtectedResource.TeamClaim)
	if err != nil {
		return nil, ErrOAuthTokenInvalid
	}

	identity, err := s.repo.GetIdentityByProviderSubject(ctx, provider.ID, identitySubject)
	if err != nil {
		return nil, err
	}
	if identity == nil || !identity.Active {
		return nil, ErrOAuthAccessDenied
	}
	memberships, err := s.repo.ListTeamMembershipsForIdentity(ctx, identity.ID)
	if err != nil {
		return nil, err
	}
	selected, err := selectOAuthMembership(memberships, pathTeamID, claimedTeamID)
	if err != nil {
		return nil, err
	}
	validated, err := s.ValidateMembership(ctx, &selected.Membership)
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
	mappedScopes := mapOAuthScopes(externalScopes, provider.ProtectedResource.ScopeMappings)
	validated.Grants = intersectOAuthGrants(mappedScopes, validated.Grants)

	return &domain.AuthenticatedActor{
		Team: selected.Team,
		Identity: domain.ActorIdentity{
			ID:          identity.ID,
			Kind:        "human",
			DisplayName: identity.DisplayName,
		},
		Membership: *validated,
		OwnerID:    validated.OwnerID,
	}, nil
}

func (s *SSOService) OAuthProtectedResourceMetadata(ctx context.Context) (OAuthProtectedResourceMetadata, error) {
	if s == nil || s.repo == nil {
		return OAuthProtectedResourceMetadata{}, nil
	}
	providers, err := s.repo.ListEnabledProviders(ctx)
	if err != nil {
		return OAuthProtectedResourceMetadata{}, err
	}
	issuerSet := make(map[string]struct{})
	scopeSet := make(map[string]struct{})
	for _, provider := range providers {
		if provider == nil || !provider.ProtectedResource.Enabled || provider.RetiredAt != nil {
			continue
		}
		copyProvider := *provider
		if err := normalizeSSOProviderForWrite(&copyProvider); err != nil {
			return OAuthProtectedResourceMetadata{}, fmt.Errorf("%w: invalid protected-resource provider", ErrOAuthProviderUnavailable)
		}
		issuerSet[copyProvider.IssuerURL] = struct{}{}
		for _, mapping := range copyProvider.ProtectedResource.ScopeMappings {
			scopeSet[mapping.ExternalScope] = struct{}{}
		}
	}
	metadata := OAuthProtectedResourceMetadata{
		AuthorizationServers: mapKeysSorted(issuerSet),
		ScopesSupported:      mapKeysSorted(scopeSet),
	}
	return metadata, nil
}

func (s *SSOService) oauthProviderForIssuer(ctx context.Context, issuer string) (*domain.SSOProvider, error) {
	providers, err := s.repo.ListEnabledProviders(ctx)
	if err != nil {
		return nil, err
	}
	var matched *domain.SSOProvider
	for _, provider := range providers {
		if provider == nil || provider.RetiredAt != nil || !provider.ProtectedResource.Enabled {
			continue
		}
		if provider.IssuerURL != issuer {
			continue
		}
		copyProvider := *provider
		if err := normalizeSSOProviderForWrite(&copyProvider); err != nil {
			return nil, fmt.Errorf("%w: invalid protected-resource provider", ErrOAuthProviderUnavailable)
		}
		if matched != nil {
			return nil, ErrOAuthTokenInvalid
		}
		matched = &copyProvider
	}
	if matched == nil {
		return nil, ErrOAuthTokenInvalid
	}
	return matched, nil
}

func strictJWTParts(raw string) ([]byte, []byte, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, nil, ErrOAuthTokenInvalid
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(header) == 0 || len(header) > 16*1024 {
		return nil, nil, ErrOAuthTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) == 0 || len(payload) > 256*1024 {
		return nil, nil, ErrOAuthTokenInvalid
	}
	return header, payload, nil
}

func requiredStringClaim(raw map[string]json.RawMessage, name string) (string, error) {
	value, ok := raw[name]
	if !ok {
		return "", ErrOAuthTokenInvalid
	}
	var decoded string
	if err := json.Unmarshal(value, &decoded); err != nil || strings.TrimSpace(decoded) == "" {
		return "", ErrOAuthTokenInvalid
	}
	return strings.TrimSpace(decoded), nil
}

func requiredExactStringClaim(raw map[string]json.RawMessage, name string) (string, error) {
	value, ok := raw[name]
	if !ok {
		return "", ErrOAuthTokenInvalid
	}
	var decoded string
	if err := json.Unmarshal(value, &decoded); err != nil || decoded == "" || decoded != strings.TrimSpace(decoded) {
		return "", ErrOAuthTokenInvalid
	}
	return decoded, nil
}

func oauthScopeClaim(raw map[string]json.RawMessage, name string) ([]string, error) {
	value, ok := raw[name]
	if !ok {
		return nil, nil
	}
	var single string
	if err := json.Unmarshal(value, &single); err == nil {
		return uniqueNonEmptyStrings(strings.Fields(single)), nil
	}
	var multiple []string
	if err := json.Unmarshal(value, &multiple); err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	for _, scope := range multiple {
		if strings.TrimSpace(scope) == "" || strings.ContainsAny(scope, " \t\r\n") {
			return nil, ErrOAuthTokenInvalid
		}
	}
	return uniqueNonEmptyStrings(multiple), nil
}

func oauthTeamClaim(raw map[string]json.RawMessage, name string) (*uuid.UUID, error) {
	if name == "" {
		return nil, nil
	}
	value, ok := raw[name]
	if !ok {
		return nil, nil
	}
	var encoded string
	if err := json.Unmarshal(value, &encoded); err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	teamID, err := uuid.Parse(strings.TrimSpace(encoded))
	if err != nil || teamID == uuid.Nil {
		return nil, ErrOAuthTokenInvalid
	}
	return &teamID, nil
}

func selectOAuthMembership(memberships []*domain.SSOTeamMembership, pathTeamID, claimedTeamID *uuid.UUID) (*domain.SSOTeamMembership, error) {
	target := pathTeamID
	if pathTeamID != nil && claimedTeamID != nil && *pathTeamID != *claimedTeamID {
		return nil, ErrOAuthAccessDenied
	}
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
		return memberships[0], nil
	}
	for _, membership := range memberships {
		if membership != nil && membership.Team.ID == *target {
			return membership, nil
		}
	}
	return nil, ErrOAuthAccessDenied
}

func mapOAuthScopes(external []string, mappings []domain.OAuthScopeMapping) []string {
	externalSet := make(map[string]struct{}, len(external))
	for _, scope := range external {
		externalSet[scope] = struct{}{}
	}
	mapped := make(map[string]struct{})
	for _, mapping := range mappings {
		if _, ok := externalSet[mapping.ExternalScope]; !ok {
			continue
		}
		for _, scope := range mapping.InternalScopes {
			mapped[scope] = struct{}{}
		}
	}
	return orderedOAuthScopes(mapped)
}

func intersectOAuthGrants(mapped, membership []string) []string {
	membershipSet := make(map[string]struct{}, len(membership))
	for _, grant := range membership {
		membershipSet[grant] = struct{}{}
	}
	allowed := make(map[string]struct{})
	for _, scope := range mapped {
		if _, ok := membershipSet[scope]; ok {
			allowed[scope] = struct{}{}
		}
	}
	return orderedOAuthScopes(allowed)
}

func orderedOAuthScopes(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for _, scope := range []string{CredentialScopeRead, CredentialScopeWrite, CredentialScopeFeedbackRead} {
		if _, ok := values[scope]; ok {
			result = append(result, scope)
		}
	}
	return result
}

func uniqueNonEmptyStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	return mapKeysSorted(set)
}

func mapKeysSorted(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
