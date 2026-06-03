package requestctx

import (
	"context"

	"github.com/google/uuid"
)

type actorContextKey struct{}

// ActorProfile identifies the authenticated team profile that initiated a
// request. TeamID is the knowledge scope; ProfileID is the named member/client
// identity inside that team.
type ActorProfile struct {
	TeamID      uuid.UUID
	ProfileID   uuid.UUID
	ProfileName string
}

// WithActorProfile stores authenticated actor metadata in context.
func WithActorProfile(ctx context.Context, actor ActorProfile) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actor)
}

// ActorProfileFromContext returns authenticated actor metadata when available.
func ActorProfileFromContext(ctx context.Context) (ActorProfile, bool) {
	actor, ok := ctx.Value(actorContextKey{}).(ActorProfile)
	return actor, ok
}

// ActorOwner returns the profile identity used as the ownership boundary for
// team-scoped knowledge entities. Empty values mean the caller did not carry an
// authenticated profile actor, which is treated by service code as a system
// context rather than a public user mutation.
func ActorOwner(ctx context.Context) (profileID, profileName string, ok bool) {
	actor, ok := ActorProfileFromContext(ctx)
	if !ok || actor.ProfileID == uuid.Nil {
		return "", "", false
	}
	return actor.ProfileID.String(), actor.ProfileName, true
}
