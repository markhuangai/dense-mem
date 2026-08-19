package domain

import "time"

type OAuthProtectedResourceProfile struct {
	Name              string                       `json:"name"`
	Issuer            string                       `json:"issuer"`
	ProtectedResource OAuthProtectedResourceConfig `json:"protected_resource"`
}

type OAuthProtectedResourceConfig struct {
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

// OAuthValidatedToken contains only the bounded fields needed by protected-resource policy.
// The encoded token and arbitrary provider claims never enter this value.
type OAuthValidatedToken struct {
	ProfileName string
	Issuer      string
	Subject     string
	Audiences   []string
	Scopes      []string
	Team        string
	ExpiresAt   time.Time
}
