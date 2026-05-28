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
