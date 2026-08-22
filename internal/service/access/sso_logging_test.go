package access

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestSSOLoggingClaimDiagnostics(t *testing.T) {
	logger := &captureSSOLogger{}
	providerID := uuid.New()
	provider := domain.SSOProvider{
		ID:              providerID,
		Name:            "Zitadel",
		Kind:            domain.SSOProviderKindGenericOIDC,
		IssuerURL:       "https://issuer.example.com",
		ClientSecretEnv: "ZITADEL_CLIENT_SECRET",
		GroupClaims:     []string{"groups", "roles"},
		GroupsEndpoint:  "https://issuer.example.com/groups",
		Enabled:         true,
	}
	claims := oidcLoginClaims{
		Subject:             "subject-123",
		Groups:              []string{"dense-mem-admin"},
		IDTokenClaimNames:   []string{"aud", "sub"},
		UserInfoClaimNames:  []string{"email", "groups"},
		GroupsFromUserInfo:  true,
		UserInfoError:       "userinfo failed",
		UserInfoClaimsError: "userinfo claims failed",
	}
	svc := NewSSOService(nil, SSOConfig{Logger: logger})

	svc.debugSSOLoginClaims("sso login oidc claims read", provider, claims)

	require.Len(t, logger.debugs, 1)
	assert.Equal(t, "sso login oidc claims read", logger.debugs[0].message)
	attrs := logAttrsByKey(logger.debugs[0].attrs)
	assert.Equal(t, true, attrs["provider_found"])
	assert.Equal(t, ssoRedactedHash(providerID.String()), attrs["provider_id_hash"])
	assert.Equal(t, ssoRedactedHash("Zitadel"), attrs["provider_name_hash"])
	assert.Equal(t, "generic_oidc", attrs["provider_kind"])
	assert.Equal(t, true, attrs["provider_enabled"])
	assert.Equal(t, true, attrs["client_secret_env_configured"])
	assert.Equal(t, 2, attrs["configured_group_claim_count"])
	assert.Equal(t, []string{ssoRedactedHash("groups"), ssoRedactedHash("roles")}, attrs["configured_group_claims_hashes"])
	assert.Equal(t, ssoRedactedHash("subject-123"), attrs["subject_hash"])
	assert.Equal(t, 1, attrs["group_count"])
	assert.Equal(t, []string{ssoRedactedHash("dense-mem-admin")}, attrs["groups_hashes"])
	assert.Equal(t, 2, attrs["id_token_claim_count"])
	assert.Equal(t, []string{ssoRedactedHash("aud"), ssoRedactedHash("sub")}, attrs["id_token_claim_names_hashes"])
	assert.Equal(t, 2, attrs["userinfo_claim_count"])
	assert.Equal(t, []string{ssoRedactedHash("email"), ssoRedactedHash("groups")}, attrs["userinfo_claim_names_hashes"])
	assert.Equal(t, true, attrs["groups_from_userinfo"])
	assert.Equal(t, "userinfo failed", attrs["userinfo_error"])
	assert.Equal(t, "userinfo claims failed", attrs["userinfo_claims_error"])
	assert.NotContains(t, attrs, "subject")
	assert.NotContains(t, attrs, "groups")
	assert.NotContains(t, attrs, "id_token_claim_names")
	assert.NotContains(t, attrs, "userinfo_claim_names")
}

func TestSSOLoggingFailureHelpers(t *testing.T) {
	logger := &captureSSOLogger{}
	svc := NewSSOService(nil, SSOConfig{Logger: logger})
	provider := domain.SSOProvider{ID: uuid.New(), Name: "OIDC", Kind: domain.SSOProviderKindGenericOIDC}
	mapping := domain.SSOGroupMapping{
		ID:         uuid.New(),
		ProviderID: provider.ID,
		TeamID:     uuid.New(),
		GroupID:    "dense-mem-admin",
		Scopes:     []string{CredentialScopeRead, CredentialScopeWrite},
		Role:       CredentialRoleManager,
		Enabled:    true,
	}

	svc.debugSSOLoginFailure("login failed", ErrSSOAccessDenied, provider, oidcLoginClaims{Subject: "subject-123"})
	svc.debugSSOFailure("generic failure", errors.New("database failed"))
	svc.debugSSOProviderFailure("provider missing", ErrSSOProviderDisabled, nil)
	svc.debugSSOMappingFailure("mapping failed", errors.New("mapping invalid"), &mapping)
	svc.debugSSOMappingFailure("mapping missing", nil, nil)

	require.Len(t, logger.debugs, 5)
	assert.Equal(t, "sso access denied", logAttrsByKey(logger.debugs[0].attrs)["error"])
	assert.Equal(t, "database failed", logAttrsByKey(logger.debugs[1].attrs)["error"])
	assert.Equal(t, false, logAttrsByKey(logger.debugs[2].attrs)["provider_found"])

	mappingAttrs := logAttrsByKey(logger.debugs[3].attrs)
	assert.Equal(t, true, mappingAttrs["mapping_found"])
	assert.Equal(t, ssoRedactedHash(mapping.ID.String()), mappingAttrs["mapping_id_hash"])
	assert.Equal(t, ssoRedactedHash("dense-mem-admin"), mappingAttrs["group_id_hash"])
	assert.Equal(t, CredentialRoleManager, mappingAttrs["role"])
	assert.Equal(t, true, mappingAttrs["mapping_enabled"])
	assert.Equal(t, []string{CredentialScopeRead, CredentialScopeWrite}, mappingAttrs["scopes"])
	assert.Equal(t, false, logAttrsByKey(logger.debugs[4].attrs)["mapping_found"])

	var nilSvc *SSOService
	nilSvc.debugSSOFailure("ignored", errors.New("ignored"))
	assert.Len(t, logger.debugs, 5)
}

func TestSSOLogAttrHelpersDescribeOptionalObjects(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	identityID := uuid.New()
	providerID := uuid.New()
	teamProfileID := uuid.New()
	teamID := uuid.New()
	session := &domain.SSOSession{
		IdentityID:   identityID,
		ProviderID:   providerID,
		MembershipID: teamProfileID,
		OwnerID:      teamProfileID,
		TeamID:       teamID,
		ExpiresAt:    now,
	}

	sessionAttrs := logAttrsByKey(ssoSessionLogAttrs(session))
	assert.Equal(t, true, sessionAttrs["session_found"])
	assert.Equal(t, ssoRedactedHash(identityID.String()), sessionAttrs["identity_id_hash"])
	assert.Equal(t, ssoRedactedHash(providerID.String()), sessionAttrs["provider_id_hash"])
	assert.Equal(t, ssoRedactedHash(teamProfileID.String()), sessionAttrs["membership_id_hash"])
	assert.Equal(t, ssoRedactedHash(teamProfileID.String()), sessionAttrs["owner_id_hash"])
	assert.Equal(t, ssoRedactedHash(teamID.String()), sessionAttrs["team_id_hash"])
	assert.Equal(t, now.Format(time.RFC3339), sessionAttrs["expires_at"])
	assert.Equal(t, false, logAttrsByKey(ssoSessionLogAttrs(nil))["session_found"])

	key := &domain.Credential{
		ID:                   teamProfileID,
		TeamID:               teamID,
		Role:                 CredentialRoleManager,
		Scopes:               []string{CredentialScopeRead},
		OwnerIdentityID:      &identityID,
		SSOProviderID:        &providerID,
		SSOSubject:           "subject-123",
		SSOGroupID:           "dense-mem-admin",
		SSOEntitlementStatus: "active",
	}
	keyAttrs := logAttrsByKey(ssoCredentialLogAttrs(key))
	assert.Equal(t, true, keyAttrs["credential_found"])
	assert.Equal(t, ssoRedactedHash(teamProfileID.String()), keyAttrs["credential_id_hash"])
	assert.Equal(t, ssoRedactedHash(teamID.String()), keyAttrs["team_id_hash"])
	assert.Equal(t, CredentialRoleManager, keyAttrs["role"])
	assert.Equal(t, []string{CredentialScopeRead}, keyAttrs["scopes"])
	assert.Equal(t, ssoRedactedHash(identityID.String()), keyAttrs["owner_identity_id_hash"])
	assert.Equal(t, ssoRedactedHash(providerID.String()), keyAttrs["provider_id_hash"])
	assert.Equal(t, ssoRedactedHash("subject-123"), keyAttrs["subject_hash"])
	assert.Equal(t, ssoRedactedHash("dense-mem-admin"), keyAttrs["sso_group_id_hash"])
	assert.Equal(t, "active", keyAttrs["entitlement_status"])
	assert.NotContains(t, keyAttrs, "subject")
	assert.NotContains(t, keyAttrs, "sso_group_id")
	assert.Equal(t, false, logAttrsByKey(ssoCredentialLogAttrs(nil))["credential_found"])
}

type captureSSOLogger struct {
	debugs []captureSSODebugLog
}

type captureSSODebugLog struct {
	message string
	attrs   []observability.LogAttr
}

func (l *captureSSOLogger) Info(string, ...observability.LogAttr) {}

func (l *captureSSOLogger) Error(string, error, ...observability.LogAttr) {}

func (l *captureSSOLogger) Warn(string, ...observability.LogAttr) {}

func (l *captureSSOLogger) Debug(message string, attrs ...observability.LogAttr) {
	l.debugs = append(l.debugs, captureSSODebugLog{message: message, attrs: append([]observability.LogAttr(nil), attrs...)})
}

func (l *captureSSOLogger) With(...observability.LogAttr) observability.LogProvider {
	return l
}

func logAttrsByKey(attrs []observability.LogAttr) map[string]any {
	values := make(map[string]any, len(attrs))
	for _, attr := range attrs {
		values[attr.Key] = attr.Value
	}
	return values
}
