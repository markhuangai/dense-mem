package requestctx

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestActorContextPreservesCanonicalIdentityAndCopiesMutableFields(t *testing.T) {
	teamID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ownerID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	credentialID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
	actor := Actor{
		TeamID:       teamID,
		IdentityID:   credentialID,
		MembershipID: uuid.MustParse("00000000-0000-0000-0000-0000000000cc"),
		OwnerID:      ownerID,
		OwnerName:    "native",
		CredentialID: &credentialID,
		AuthMethod:   "api_key",
		Role:         "manager",
		Grants:       []string{"read", "write"},
	}
	want := actor
	want.Grants = append([]string(nil), actor.Grants...)
	wantCredentialID := credentialID
	want.CredentialID = &wantCredentialID
	ctx := WithActor(context.Background(), actor)
	actor.Grants[0] = "mutated"
	*actor.CredentialID = uuid.New()

	got, ok := ActorFromContext(ctx)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("ActorFromContext = %#v, %v; want %#v,true", got, ok, want)
	}
	got.Grants[0] = "mutated"
	*got.CredentialID = uuid.New()
	gotAgain, ok := ActorFromContext(ctx)
	if !ok || !reflect.DeepEqual(gotAgain, want) {
		t.Fatalf("ActorFromContext returned mutable fields: %#v, %v", gotAgain, ok)
	}

	resolvedOwnerID, ownerName, ok := ActorOwner(ctx)
	if !ok || resolvedOwnerID != ownerID.String() || ownerName != "native" {
		t.Fatalf("ActorOwner = %q,%q,%v; want %q,native,true", resolvedOwnerID, ownerName, ok, ownerID)
	}
}

func TestActorFromContextEmptyWhenUnsetOrWrongType(t *testing.T) {
	if got, ok := ActorFromContext(context.Background()); ok || !reflect.DeepEqual(got, Actor{}) {
		t.Fatalf("ActorFromContext unset = %#v, %v; want zero,false", got, ok)
	}

	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, Actor{OwnerName: "wrong"})
	if got, ok := ActorFromContext(ctx); ok || !reflect.DeepEqual(got, Actor{}) {
		t.Fatalf("ActorFromContext wrong key = %#v, %v; want zero,false", got, ok)
	}
}

func TestActorContextCopiesAllowedMemorySpaces(t *testing.T) {
	teamID := uuid.New()
	actor := Actor{TeamID: teamID, AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.New(), Kind: domain.MemorySpaceCredentialPrivate}}}
	ctx := WithActor(context.Background(), actor)
	actor.AllowedSpaces[0].Kind = domain.MemorySpaceTeamShared
	got, ok := ActorFromContext(ctx)
	if !ok || got.AllowedSpaces[0].Kind != domain.MemorySpaceCredentialPrivate {
		t.Fatalf("ActorFromContext allowed spaces = %#v, %v", got.AllowedSpaces, ok)
	}
}

func TestActorOwnerEmptyWithoutPermanentOwner(t *testing.T) {
	ownerID, ownerName, ok := ActorOwner(context.Background())
	if ok || ownerID != "" || ownerName != "" {
		t.Fatalf("ActorOwner unset = %q,%q,%v; want empty,false", ownerID, ownerName, ok)
	}

	ctx := WithActor(context.Background(), Actor{OwnerName: "anonymous"})
	ownerID, ownerName, ok = ActorOwner(ctx)
	if ok || ownerID != "" || ownerName != "" {
		t.Fatalf("ActorOwner nil owner = %q,%q,%v; want empty,false", ownerID, ownerName, ok)
	}
}
