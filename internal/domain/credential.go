package domain

import (
	"time"

	"github.com/google/uuid"
)

// CredentialModel is the companion interface for Credential.
type CredentialModel interface {
	GetID() uuid.UUID
	GetActorIdentityID() uuid.UUID
	GetMembershipID() uuid.UUID
	GetOwnerID() uuid.UUID
	GetTeamID() uuid.UUID
	GetName() string
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

// Credential is a stable API credential whose bearer material can rotate in place.
type Credential struct {
	ID              uuid.UUID
	ActorIdentityID uuid.UUID
	MembershipID    uuid.UUID
	OwnerID         uuid.UUID
	TeamID          uuid.UUID
	TeamName        string
	Name            string
	KeyHash         string
	KeyPrefix       string
	KeySuffix       string
	Scopes          []string
	Role            string
	RateLimit       int
	LastUsedAt      *time.Time
	ExpiresAt       *time.Time
	CreatedAt       time.Time
	RevokedAt       *time.Time

	OwnerIdentityID             *uuid.UUID
	SSOProviderID               *uuid.UUID
	SSOSubject                  string
	SSOEmail                    string
	SSOGroupID                  string
	SSOEntitlementStatus        string
	SSOLastEntitlementCheckedAt *time.Time
	SSOLastLoginAt              *time.Time
}

// Ensure Credential implements CredentialModel.
var _ CredentialModel = (*Credential)(nil)

// Getters for CredentialModel.
func (c *Credential) GetID() uuid.UUID              { return c.ID }
func (c *Credential) GetActorIdentityID() uuid.UUID { return c.ActorIdentityID }
func (c *Credential) GetMembershipID() uuid.UUID    { return c.MembershipID }
func (c *Credential) GetOwnerID() uuid.UUID         { return c.OwnerID }
func (c *Credential) GetTeamID() uuid.UUID          { return c.TeamID }
func (c *Credential) GetName() string               { return c.Name }
func (c *Credential) GetKeyHash() string            { return c.KeyHash }
func (c *Credential) GetKeyPrefix() string          { return c.KeyPrefix }
func (c *Credential) GetKeySuffix() string          { return c.KeySuffix }
func (c *Credential) GetScopes() []string           { return c.Scopes }
func (c *Credential) GetRole() string {
	if c.Role != "" {
		return c.Role
	}
	return "member"
}
func (c *Credential) GetRateLimit() int         { return c.RateLimit }
func (c *Credential) GetLastUsedAt() *time.Time { return c.LastUsedAt }
func (c *Credential) GetExpiresAt() *time.Time  { return c.ExpiresAt }
func (c *Credential) GetCreatedAt() time.Time   { return c.CreatedAt }
func (c *Credential) GetRevokedAt() *time.Time  { return c.RevokedAt }
