package ownership

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestActorOwnerID(t *testing.T) {
	if got := ActorOwnerID(context.Background()); got != "" {
		t.Fatalf("ActorOwnerID unset = %q; want empty", got)
	}

	actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		ProfileID:   actorID,
		ProfileName: "native",
	})
	if got := ActorOwnerID(ctx); got != actorID.String() {
		t.Fatalf("ActorOwnerID = %q; want %q", got, actorID.String())
	}
}

func TestRequireOwner(t *testing.T) {
	ownerID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa").String()
	otherID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb").String()

	if err := RequireOwner(context.Background(), ""); err != nil {
		t.Fatalf("RequireOwner system context returned %v; want nil", err)
	}
	if err := RequireOwner(context.Background(), ownerID); err != nil {
		t.Fatalf("RequireOwner system context with owner returned %v; want nil", err)
	}

	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		ProfileID: uuid.MustParse(ownerID),
	})
	if err := RequireOwner(ctx, ownerID); err != nil {
		t.Fatalf("RequireOwner matching owner returned %v; want nil", err)
	}
	if err := RequireOwner(ctx, ""); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("RequireOwner empty owner = %v; want ErrOwnerMismatch", err)
	}
	if err := RequireOwner(ctx, otherID); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("RequireOwner mismatch = %v; want ErrOwnerMismatch", err)
	}
}
