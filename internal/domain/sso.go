package domain

import (
	"time"

	"github.com/google/uuid"
)

type SSOProviderKind string

const (
	SSOProviderKindAzureAD     SSOProviderKind = "azure_ad"
	SSOProviderKindPingOne     SSOProviderKind = "pingone"
	SSOProviderKindGenericOIDC SSOProviderKind = "generic_oidc"
)

type SSOProvider struct {
	ID                uuid.UUID
	Name              string
	Kind              SSOProviderKind
	IssuerURL         string
	TenantID          string
	IdentityClaim     string
	ClientID          string
	ClientSecretEnv   string
	Scopes            []string
	GroupClaims       []string
	GroupsEndpoint    string
	GroupsScopes      []string
	ProtectedResource OAuthProtectedResourceConfig
	Enabled           bool
	RetiredAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// OAuthProtectedResourceConfig extends one browser SSO provider for MCP
// access-token validation. Dense-Mem remains a protected resource; it never
// issues, refreshes, or persists OAuth tokens.
type OAuthProtectedResourceConfig struct {
	Enabled       bool                `json:"enabled"`
	Audiences     []string            `json:"audiences"`
	JWKSSource    string              `json:"jwks_source"`
	JWKSURI       string              `json:"jwks_uri"`
	Algorithms    []string            `json:"algorithms"`
	ScopeClaim    string              `json:"scope_claim"`
	ScopeMappings []OAuthScopeMapping `json:"scope_mappings"`
	TeamClaim     string              `json:"team_claim"`
}

type OAuthScopeMapping struct {
	ExternalScope  string   `json:"external_scope"`
	InternalScopes []string `json:"internal_scopes"`
}

type SSOGroupMapping struct {
	ID         uuid.UUID
	ProviderID uuid.UUID
	TeamID     uuid.UUID
	TeamName   string
	GroupID    string
	GroupName  string
	Scopes     []string
	Role       string
	Enabled    bool
	Origin     string
	RetiredAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type SSOIdentity struct {
	ID                     uuid.UUID
	ProviderID             uuid.UUID
	Subject                string
	ExternalID             string
	Email                  string
	DisplayName            string
	Active                 bool
	LastLoginAt            *time.Time
	LastEntitlementCheckAt *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type SSOEntitlementCache struct {
	ProviderID uuid.UUID
	Subject    string
	Groups     []string
	Status     string
	CheckedAt  time.Time
	ExpiresAt  time.Time
	Error      string
}

type SSOOAuthState struct {
	StateHash    string
	ProviderID   uuid.UUID
	PKCEVerifier string
	Nonce        string
	RedirectPath string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

type SSOSession struct {
	SessionHash  string
	IdentityID   uuid.UUID
	ProviderID   uuid.UUID
	MembershipID uuid.UUID
	OwnerID      uuid.UUID
	TeamID       uuid.UUID
	CSRFHash     string
	ExpiresAt    time.Time
	CreatedAt    time.Time
	LastSeenAt   time.Time
}

type Membership struct {
	ID                          uuid.UUID
	MemorySpaceID               uuid.UUID
	ActorIdentityID             uuid.UUID
	TeamID                      uuid.UUID
	OwnerID                     uuid.UUID
	Name                        string
	Grants                      []string
	Role                        string
	Status                      string
	CreatedAt                   time.Time
	SSOProviderID               *uuid.UUID
	SSOSubject                  string
	SSOEmail                    string
	SSOGroupID                  string
	SSOEntitlementStatus        string
	SSOLastEntitlementCheckedAt *time.Time
	SSOLastLoginAt              *time.Time
}

type SSOTeamMembership struct {
	Team       Team
	Membership Membership
}

type ActorIdentity struct {
	ID          uuid.UUID
	Kind        string
	DisplayName string
}

type AuthenticatedActor struct {
	Team       Team
	Identity   ActorIdentity
	Membership Membership
	OwnerID    uuid.UUID
	Credential *Credential
}
