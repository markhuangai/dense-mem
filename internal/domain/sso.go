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
	ID              uuid.UUID
	Name            string
	Kind            SSOProviderKind
	IssuerURL       string
	TenantID        string
	IdentityClaim   string
	ClientID        string
	ClientSecretEnv string
	Scopes          []string
	GroupClaims     []string
	GroupsEndpoint  string
	GroupsScopes    []string
	Enabled         bool
	RetiredAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
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
	SessionHash   string
	IdentityID    uuid.UUID
	ProviderID    uuid.UUID
	TeamProfileID uuid.UUID
	TeamID        uuid.UUID
	CSRFHash      string
	ExpiresAt     time.Time
	CreatedAt     time.Time
	LastSeenAt    time.Time
}

type SSOTeamProfile struct {
	Team    Profile
	Profile APIKey
}
