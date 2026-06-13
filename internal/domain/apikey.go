package domain

import (
	"time"

	"github.com/google/uuid"
)

// APIKeyModel is the companion interface for APIKey.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type APIKeyModel interface {
	GetID() uuid.UUID
	GetTeamID() uuid.UUID
	GetProfileID() uuid.UUID
	GetProfileName() string
	GetLabel() string
	GetKeyHash() string
	GetKeyPrefix() string
	GetKeySuffix() string
	GetScopes() []string
	GetRole() string
	GetRateLimit() int
	GetLastUsedAt() *time.Time
	GetExpiresAt() *time.Time
	GetCreatedAt() time.Time
	GetRevokedAt() *time.Time
}

// APIKey represents an API key for authentication.
type APIKey struct {
	// ID is the stable team profile ID. The key material can rotate in place.
	ID uuid.UUID
	// ProfileID is retained as the storage tenant/team ID for compatibility
	// with existing call sites during the team migration.
	ProfileID  uuid.UUID
	TeamID     uuid.UUID
	TeamName   string
	Label      string
	Name       string
	KeyHash    string
	KeyPrefix  string
	KeySuffix  string
	Scopes     []string
	Role       string
	RateLimit  int
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	CreatedAt  time.Time
	RevokedAt  *time.Time

	AuthSource                  string
	SSOIdentityID               *uuid.UUID
	SSOProviderID               *uuid.UUID
	SSOOwnerIdentityID          *uuid.UUID
	SSOSubject                  string
	SSOEmail                    string
	SSOGroupID                  string
	SSOEntitlementStatus        string
	SSOLastEntitlementCheckedAt *time.Time
	SSOLastLoginAt              *time.Time
}

// Ensure APIKey implements APIKeyModel
var _ APIKeyModel = (*APIKey)(nil)

// Getters for APIKeyModel interface
func (k *APIKey) GetID() uuid.UUID          { return k.ID }
func (k *APIKey) GetTeamID() uuid.UUID      { return k.effectiveTeamID() }
func (k *APIKey) GetProfileID() uuid.UUID   { return k.effectiveTeamID() }
func (k *APIKey) GetProfileName() string    { return k.effectiveName() }
func (k *APIKey) GetLabel() string          { return k.effectiveName() }
func (k *APIKey) GetKeyHash() string        { return k.KeyHash }
func (k *APIKey) GetKeyPrefix() string      { return k.KeyPrefix }
func (k *APIKey) GetKeySuffix() string      { return k.KeySuffix }
func (k *APIKey) GetScopes() []string       { return k.Scopes }
func (k *APIKey) GetRole() string           { return k.effectiveRole() }
func (k *APIKey) GetRateLimit() int         { return k.RateLimit }
func (k *APIKey) GetLastUsedAt() *time.Time { return k.LastUsedAt }
func (k *APIKey) GetExpiresAt() *time.Time  { return k.ExpiresAt }
func (k *APIKey) GetCreatedAt() time.Time   { return k.CreatedAt }
func (k *APIKey) GetRevokedAt() *time.Time  { return k.RevokedAt }

func (k *APIKey) effectiveTeamID() uuid.UUID {
	if k.TeamID != uuid.Nil {
		return k.TeamID
	}
	return k.ProfileID
}

func (k *APIKey) effectiveName() string {
	if k.Name != "" {
		return k.Name
	}
	return k.Label
}

func (k *APIKey) effectiveRole() string {
	if k.Role != "" {
		return k.Role
	}
	return "member"
}
