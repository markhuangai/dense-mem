package requestctx

import (
	"context"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type actorContextKey struct{}
type allowedSpacesContextKey struct{}
type authenticationSecretsContextKey struct{}
type authenticatedContextKey struct{}

// Actor is the immutable authenticated identity projected into application code.
// OwnerID is the permanent semantic ownership alias; it is distinct from the
// team, identity, membership, and optional credential identifiers.
type Actor struct {
	TeamID        uuid.UUID
	TeamName      string
	IdentityID    uuid.UUID
	MembershipID  uuid.UUID
	OwnerID       uuid.UUID
	OwnerName     string
	CredentialID  *uuid.UUID
	AuthMethod    string
	Role          string
	Grants        []string
	AllowedSpaces []domain.MemorySpaceAccess
}

func WithActor(ctx context.Context, actor Actor) context.Context {
	actor.Grants = append([]string(nil), actor.Grants...)
	actor.AllowedSpaces = append([]domain.MemorySpaceAccess(nil), actor.AllowedSpaces...)
	if actor.CredentialID != nil {
		credentialID := *actor.CredentialID
		actor.CredentialID = &credentialID
	}
	return context.WithValue(ctx, actorContextKey{}, actor)
}

func ActorFromContext(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorContextKey{}).(Actor)
	actor.Grants = append([]string(nil), actor.Grants...)
	actor.AllowedSpaces = append([]domain.MemorySpaceAccess(nil), actor.AllowedSpaces...)
	if actor.CredentialID != nil {
		credentialID := *actor.CredentialID
		actor.CredentialID = &credentialID
	}
	return actor, ok
}

// WithAllowedSpaces carries trusted worker-derived memory-space access through
// repository transactions that do not have an HTTP actor context.
func WithAllowedSpaces(ctx context.Context, spaces []domain.MemorySpaceAccess) context.Context {
	return context.WithValue(ctx, allowedSpacesContextKey{}, append([]domain.MemorySpaceAccess(nil), spaces...))
}

func AllowedSpacesFromContext(ctx context.Context) []domain.MemorySpaceAccess {
	spaces, _ := ctx.Value(allowedSpacesContextKey{}).([]domain.MemorySpaceAccess)
	return append([]domain.MemorySpaceAccess(nil), spaces...)
}

func ActorOwner(ctx context.Context) (ownerID, ownerName string, ok bool) {
	actor, ok := ActorFromContext(ctx)
	if !ok || actor.OwnerID == uuid.Nil {
		return "", "", false
	}
	return actor.OwnerID.String(), actor.OwnerName, true
}

// WithAuthenticationSecrets carries presented authentication material to
// trusted operator logging for redaction. Callers may attach it before
// validation without admitting an actor; values are copied so downstream code
// cannot mutate the request context.
func WithAuthenticationSecrets(ctx context.Context, secrets ...string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, authenticationSecretsContextKey{}, append([]string(nil), secrets...))
}

func AuthenticationSecretsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	secrets, _ := ctx.Value(authenticationSecretsContextKey{}).([]string)
	return append([]string(nil), secrets...)
}

// WithAuthenticationVerified marks a context after the presented credential
// has passed its owning authenticator. It is separate from the redaction-only
// authentication secret context so log attribution cannot trust unverified
// material.
func WithAuthenticationVerified(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, authenticatedContextKey{}, true)
}

// AuthenticationVerifiedFromContext reports whether a trusted authenticator
// marked the context after validating its presented credential.
func AuthenticationVerifiedFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	verified, _ := ctx.Value(authenticatedContextKey{}).(bool)
	return verified
}
