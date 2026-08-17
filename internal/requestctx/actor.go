package requestctx

import (
	"context"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type actorContextKey struct{}

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

func ActorOwner(ctx context.Context) (ownerID, ownerName string, ok bool) {
	actor, ok := ActorFromContext(ctx)
	if !ok || actor.OwnerID == uuid.Nil {
		return "", "", false
	}
	return actor.OwnerID.String(), actor.OwnerName, true
}
