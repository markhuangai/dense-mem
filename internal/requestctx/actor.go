package requestctx

import (
	"context"

	"github.com/google/uuid"
)

type actorContextKey struct{}
type credentialContextKey struct{}

// ActorProfile identifies the authenticated team profile that initiated a
// request. TeamID is the knowledge scope; ProfileID is the named member/client
// identity inside that team.
type ActorProfile struct {
	TeamID      uuid.UUID
	TeamName    string
	ProfileID   uuid.UUID
	ProfileName string
}

// ActorCredential identifies the authenticated credential that initiated a
// request. It is stored separately from ActorProfile because service and tool
// packages should not import HTTP middleware internals.
type ActorCredential struct {
	KeyID      uuid.UUID
	AuthMethod string
	Role       string
	Scopes     []string
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

// WithActorCredential stores authenticated credential metadata in context.
func WithActorCredential(ctx context.Context, credential ActorCredential) context.Context {
	credential.Scopes = append([]string(nil), credential.Scopes...)
	return context.WithValue(ctx, credentialContextKey{}, credential)
}

// ActorCredentialFromContext returns authenticated credential metadata when available.
func ActorCredentialFromContext(ctx context.Context) (ActorCredential, bool) {
	credential, ok := ctx.Value(credentialContextKey{}).(ActorCredential)
	credential.Scopes = append([]string(nil), credential.Scopes...)
	return credential, ok
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
