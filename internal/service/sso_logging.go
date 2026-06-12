package service

import (
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func (s *SSOService) debugSSOLoginClaims(message string, provider domain.SSOProvider, claims oidcLoginClaims, attrs ...observability.LogAttr) {
	if s == nil || s.logger == nil {
		return
	}
	base := append(ssoProviderLogAttrs(&provider),
		observability.String("subject", claims.Subject),
		observability.Int("group_count", len(claims.Groups)),
		observability.LogAttr{Key: "groups", Value: claims.Groups},
		observability.LogAttr{Key: "id_token_claim_names", Value: claims.IDTokenClaimNames},
		observability.LogAttr{Key: "userinfo_claim_names", Value: claims.UserInfoClaimNames},
		observability.Bool("groups_from_userinfo", claims.GroupsFromUserInfo),
	)
	if claims.UserInfoError != "" {
		base = append(base, observability.String("userinfo_error", claims.UserInfoError))
	}
	if claims.UserInfoClaimsError != "" {
		base = append(base, observability.String("userinfo_claims_error", claims.UserInfoClaimsError))
	}
	s.logger.Debug(message, append(base, attrs...)...)
}

func (s *SSOService) debugSSOLoginFailure(message string, err error, provider domain.SSOProvider, claims oidcLoginClaims, attrs ...observability.LogAttr) {
	if err != nil {
		attrs = append([]observability.LogAttr{observability.String("error", err.Error())}, attrs...)
	}
	s.debugSSOLoginClaims(message, provider, claims, attrs...)
}

func (s *SSOService) debugSSOFailure(message string, err error, attrs ...observability.LogAttr) {
	if s == nil || s.logger == nil {
		return
	}
	if err != nil {
		attrs = append([]observability.LogAttr{observability.String("error", err.Error())}, attrs...)
	}
	s.logger.Debug(message, attrs...)
}

func (s *SSOService) debugSSOProviderFailure(message string, err error, provider *domain.SSOProvider, attrs ...observability.LogAttr) {
	attrs = append(ssoProviderLogAttrs(provider), attrs...)
	s.debugSSOFailure(message, err, attrs...)
}

func (s *SSOService) debugSSOMappingFailure(message string, err error, mapping *domain.SSOGroupMapping, attrs ...observability.LogAttr) {
	attrs = append(ssoMappingLogAttrs(mapping), attrs...)
	s.debugSSOFailure(message, err, attrs...)
}

func ssoProviderLogAttrs(provider *domain.SSOProvider) []observability.LogAttr {
	if provider == nil {
		return []observability.LogAttr{observability.Bool("provider_found", false)}
	}
	return []observability.LogAttr{
		observability.Bool("provider_found", true),
		observability.String("provider_id", provider.ID.String()),
		observability.String("provider_name", provider.Name),
		observability.String("provider_kind", string(provider.Kind)),
		observability.String("issuer_url", provider.IssuerURL),
		observability.Bool("provider_enabled", provider.Enabled),
		observability.Bool("groups_endpoint_configured", strings.TrimSpace(provider.GroupsEndpoint) != ""),
		observability.Bool("client_secret_env_configured", strings.TrimSpace(provider.ClientSecretEnv) != ""),
		{Key: "configured_group_claims", Value: provider.GroupClaims},
	}
}

func ssoMappingLogAttrs(mapping *domain.SSOGroupMapping) []observability.LogAttr {
	if mapping == nil {
		return []observability.LogAttr{observability.Bool("mapping_found", false)}
	}
	return []observability.LogAttr{
		observability.Bool("mapping_found", true),
		observability.String("mapping_id", mapping.ID.String()),
		observability.String("provider_id", mapping.ProviderID.String()),
		observability.String("team_id", mapping.TeamID.String()),
		observability.String("group_id", mapping.GroupID),
		observability.String("role", mapping.Role),
		observability.Bool("mapping_enabled", mapping.Enabled),
		{Key: "scopes", Value: mapping.Scopes},
	}
}

func ssoSessionLogAttrs(session *domain.SSOSession) []observability.LogAttr {
	if session == nil {
		return []observability.LogAttr{observability.Bool("session_found", false)}
	}
	return []observability.LogAttr{
		observability.Bool("session_found", true),
		observability.String("identity_id", session.IdentityID.String()),
		observability.String("provider_id", session.ProviderID.String()),
		observability.String("team_profile_id", session.TeamProfileID.String()),
		observability.String("team_id", session.TeamID.String()),
		observability.String("expires_at", session.ExpiresAt.Format(time.RFC3339)),
	}
}

func ssoAPIKeyLogAttrs(key *domain.APIKey) []observability.LogAttr {
	if key == nil {
		return []observability.LogAttr{observability.Bool("api_key_found", false)}
	}
	attrs := []observability.LogAttr{
		observability.Bool("api_key_found", true),
		observability.String("profile_id", key.ID.String()),
		observability.String("team_id", key.GetTeamID().String()),
		observability.String("role", key.Role),
		{Key: "scopes", Value: key.Scopes},
	}
	if key.SSOProviderID != nil {
		attrs = append(attrs, observability.String("provider_id", key.SSOProviderID.String()))
	}
	if key.SSOIdentityID != nil {
		attrs = append(attrs, observability.String("identity_id", key.SSOIdentityID.String()))
	}
	if key.SSOSubject != "" {
		attrs = append(attrs, observability.String("subject", key.SSOSubject))
	}
	if key.SSOGroupID != "" {
		attrs = append(attrs, observability.String("sso_group_id", key.SSOGroupID))
	}
	if key.SSOEntitlementStatus != "" {
		attrs = append(attrs, observability.String("entitlement_status", key.SSOEntitlementStatus))
	}
	return attrs
}
