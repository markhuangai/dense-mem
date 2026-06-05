package ownership

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

// ErrOwnerMismatch is returned when an authenticated team profile attempts to
// mutate knowledge owned by another team profile.
var ErrOwnerMismatch = errors.New("knowledge owner mismatch")

// ActorOwnerID returns the authenticated team profile id used for owner-scoped
// write authorization. Empty means the context has no public actor and should
// be treated as a system/operator context by services.
func ActorOwnerID(ctx context.Context) string {
	ownerID, _, _ := requestctx.ActorOwner(ctx)
	return ownerID
}

// RequireOwner permits mutation only when the authenticated actor owns the
// entity. No actor context is treated as system/operator access. A public actor
// cannot mutate legacy rows that still have no owner.
func RequireOwner(ctx context.Context, ownerID string) error {
	actorOwnerID := ActorOwnerID(ctx)
	if actorOwnerID == "" {
		return nil
	}
	if ownerID == "" || ownerID != actorOwnerID {
		return ErrOwnerMismatch
	}
	return nil
}
