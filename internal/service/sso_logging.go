package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func (s *SSOService) debugSSOLoginClaims(message string, provider domain.SSOProvider, claims oidcLoginClaims, attrs ...observability.LogAttr) {
	if s == nil || s.logger == nil {
		return
	}
	base := append(ssoProviderLogAttrs(&provider),
		ssoHashLogAttr("subject", claims.Subject),
		observability.Int("group_count", len(claims.Groups)),
		ssoHashesLogAttr("groups", claims.Groups),
		observability.Int("id_token_claim_count", len(claims.IDTokenClaimNames)),
		ssoHashesLogAttr("id_token_claim_names", claims.IDTokenClaimNames),
		observability.Int("userinfo_claim_count", len(claims.UserInfoClaimNames)),
		ssoHashesLogAttr("userinfo_claim_names", claims.UserInfoClaimNames),
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
		ssoUUIDLogAttr("provider_id", provider.ID),
		ssoHashLogAttr("provider_name", provider.Name),
		observability.String("provider_kind", string(provider.Kind)),
		ssoHashLogAttr("issuer_url", provider.IssuerURL),
		observability.Bool("provider_enabled", provider.Enabled),
		observability.Bool("groups_endpoint_configured", strings.TrimSpace(provider.GroupsEndpoint) != ""),
		observability.Bool("client_secret_env_configured", strings.TrimSpace(provider.ClientSecretEnv) != ""),
		observability.Int("configured_group_claim_count", len(provider.GroupClaims)),
		ssoHashesLogAttr("configured_group_claims", provider.GroupClaims),
	}
}

func ssoMappingLogAttrs(mapping *domain.SSOGroupMapping) []observability.LogAttr {
	if mapping == nil {
		return []observability.LogAttr{observability.Bool("mapping_found", false)}
	}
	return []observability.LogAttr{
		observability.Bool("mapping_found", true),
		ssoUUIDLogAttr("mapping_id", mapping.ID),
		ssoUUIDLogAttr("provider_id", mapping.ProviderID),
		ssoUUIDLogAttr("team_id", mapping.TeamID),
		ssoHashLogAttr("group_id", mapping.GroupID),
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
		ssoUUIDLogAttr("identity_id", session.IdentityID),
		ssoUUIDLogAttr("provider_id", session.ProviderID),
		ssoUUIDLogAttr("membership_id", session.MembershipID),
		ssoUUIDLogAttr("owner_id", session.OwnerID),
		ssoUUIDLogAttr("team_id", session.TeamID),
		observability.String("expires_at", session.ExpiresAt.Format(time.RFC3339)),
	}
}

func ssoCredentialLogAttrs(key *domain.Credential) []observability.LogAttr {
	if key == nil {
		return []observability.LogAttr{observability.Bool("credential_found", false)}
	}
	attrs := []observability.LogAttr{
		observability.Bool("credential_found", true),
		ssoUUIDLogAttr("credential_id", key.ID),
		ssoUUIDLogAttr("actor_identity_id", key.ActorIdentityID),
		ssoUUIDLogAttr("membership_id", key.MembershipID),
		ssoUUIDLogAttr("owner_id", key.OwnerID),
		ssoUUIDLogAttr("team_id", key.GetTeamID()),
		observability.String("role", key.Role),
		{Key: "scopes", Value: key.Scopes},
	}
	if key.SSOProviderID != nil {
		attrs = append(attrs, ssoUUIDLogAttr("provider_id", *key.SSOProviderID))
	}
	if key.OwnerIdentityID != nil {
		attrs = append(attrs, ssoUUIDLogAttr("owner_identity_id", *key.OwnerIdentityID))
	}
	if key.SSOSubject != "" {
		attrs = append(attrs, ssoHashLogAttr("subject", key.SSOSubject))
	}
	if key.SSOGroupID != "" {
		attrs = append(attrs, ssoHashLogAttr("sso_group_id", key.SSOGroupID))
	}
	if key.SSOEntitlementStatus != "" {
		attrs = append(attrs, observability.String("entitlement_status", key.SSOEntitlementStatus))
	}
	return attrs
}

func ssoMembershipLogAttrs(membership *domain.Membership) []observability.LogAttr {
	if membership == nil {
		return []observability.LogAttr{observability.Bool("membership_found", false)}
	}
	attrs := []observability.LogAttr{
		observability.Bool("membership_found", true),
		ssoUUIDLogAttr("membership_id", membership.ID),
		ssoUUIDLogAttr("actor_identity_id", membership.ActorIdentityID),
		ssoUUIDLogAttr("owner_id", membership.OwnerID),
		ssoUUIDLogAttr("team_id", membership.TeamID),
		observability.String("role", membership.Role),
		{Key: "grants", Value: membership.Grants},
	}
	if membership.SSOProviderID != nil {
		attrs = append(attrs, ssoUUIDLogAttr("provider_id", *membership.SSOProviderID))
	}
	if membership.SSOSubject != "" {
		attrs = append(attrs, ssoHashLogAttr("subject", membership.SSOSubject))
	}
	if membership.SSOGroupID != "" {
		attrs = append(attrs, ssoHashLogAttr("sso_group_id", membership.SSOGroupID))
	}
	return attrs
}

func ssoUUIDLogAttr(key string, value uuid.UUID) observability.LogAttr {
	if value == uuid.Nil {
		return observability.String(key+"_hash", "")
	}
	return ssoHashLogAttr(key, value.String())
}

func ssoHashLogAttr(key, value string) observability.LogAttr {
	return observability.String(key+"_hash", ssoRedactedHash(value))
}

func ssoHashesLogAttr(key string, values []string) observability.LogAttr {
	return observability.LogAttr{Key: key + "_hashes", Value: ssoRedactedHashes(values)}
}

func ssoRedactedHashes(values []string) []string {
	hashes := make([]string, 0, len(values))
	for _, value := range dedupeStrings(values) {
		if hash := ssoRedactedHash(value); hash != "" {
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

func ssoRedactedHash(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:8])
}
