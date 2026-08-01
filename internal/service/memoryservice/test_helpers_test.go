package memoryservice

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func scannerPayload(parts ...string) string {
	return strings.Join(parts, "")
}

func authenticatedRememberContext(teamID, profileID, keyID uuid.UUID) context.Context {
	ctx := correlation.WithID(context.Background(), "corr-canonical")
	ctx = requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
		TeamID:      teamID,
		TeamName:    "team",
		ProfileID:   profileID,
		ProfileName: "profile",
	})
	return requestctx.WithActorCredential(ctx, requestctx.ActorCredential{
		KeyID:      keyID,
		AuthMethod: "api_key",
		Role:       "member",
	})
}
