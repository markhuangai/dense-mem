package memoryservice

import (
	"context"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func authenticatedRememberContext(teamID, profileID, credentialID uuid.UUID) context.Context {
	ctx := correlation.WithID(context.Background(), "corr-canonical")
	return requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, TeamName: "team", IdentityID: credentialID, MembershipID: credentialID,
		OwnerID: profileID, OwnerName: "owner", CredentialID: &credentialID,
		AuthMethod: "api_key", Role: "member", Grants: []string{"read", "write"},
	})
}
