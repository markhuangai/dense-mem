package observability

import (
	"context"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func trustedContextAttrs(ctx context.Context, protector credentialSnapshotter) map[string]string {
	attrs := make(map[string]string, 3)
	if ctx == nil {
		return attrs
	}
	_, hasActor := requestctx.ActorFromContext(ctx)
	if id := correlation.FromContext(ctx); id != "" &&
		(!correlation.IsClientProvided(ctx) || correlation.IsSafeClientID(id)) &&
		(!correlation.IsClientProvided(ctx) || hasActor || requestctx.AuthenticationVerifiedFromContext(ctx)) {
		attrs["correlation_id"] = protectTrustedCorrelationID(id, protector)
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok {
		if actor.TeamID != uuid.Nil {
			attrs["team_id"] = actor.TeamID.String()
		}
		if actor.OwnerID != uuid.Nil {
			attrs["profile_id"] = actor.OwnerID.String()
		}
		if actor.CredentialID != nil && *actor.CredentialID != uuid.Nil {
			attrs["credential_id"] = actor.CredentialID.String()
		}
	}
	return attrs
}
