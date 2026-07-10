package requestctx

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestActorProfileContext(t *testing.T) {
	teamID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	profileID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	actor := ActorProfile{
		TeamID:      teamID,
		ProfileID:   profileID,
		ProfileName: "native",
	}
	ctx := WithActorProfile(context.Background(), actor)

	got, ok := ActorProfileFromContext(ctx)
	if !ok {
		t.Fatal("ActorProfileFromContext ok = false; want true")
	}
	if got != actor {
		t.Fatalf("ActorProfileFromContext = %#v; want %#v", got, actor)
	}

	ownerID, ownerName, ok := ActorOwner(ctx)
	if !ok {
		t.Fatal("ActorOwner ok = false; want true")
	}
	if ownerID != profileID.String() {
		t.Fatalf("ActorOwner profileID = %q; want %q", ownerID, profileID.String())
	}
	if ownerName != "native" {
		t.Fatalf("ActorOwner profileName = %q; want native", ownerName)
	}
}

func TestActorProfileFromContext_EmptyWhenUnsetOrWrongType(t *testing.T) {
	if got, ok := ActorProfileFromContext(context.Background()); ok || got != (ActorProfile{}) {
		t.Fatalf("ActorProfileFromContext unset = %#v, %v; want zero,false", got, ok)
	}

	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, ActorProfile{ProfileName: "wrong"})
	if got, ok := ActorProfileFromContext(ctx); ok || got != (ActorProfile{}) {
		t.Fatalf("ActorProfileFromContext wrong key = %#v, %v; want zero,false", got, ok)
	}
}

func TestActorCredentialContext(t *testing.T) {
	keyID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
	credential := ActorCredential{
		KeyID:      keyID,
		AuthMethod: "api_key",
		Role:       "manager",
	}
	ctx := WithActorCredential(context.Background(), credential)

	got, ok := ActorCredentialFromContext(ctx)
	if !ok {
		t.Fatal("ActorCredentialFromContext ok = false; want true")
	}
	if got != credential {
		t.Fatalf("ActorCredentialFromContext = %#v; want %#v", got, credential)
	}

	if got, ok := ActorCredentialFromContext(context.Background()); ok || got != (ActorCredential{}) {
		t.Fatalf("ActorCredentialFromContext unset = %#v, %v; want zero,false", got, ok)
	}
}

func TestTrustedMemoryAuthorityContext(t *testing.T) {
	if TrustedMemoryAuthority(context.Background()) {
		t.Fatal("TrustedMemoryAuthority(background) = true, want false")
	}
	if !TrustedMemoryAuthority(WithTrustedMemoryAuthority(context.Background())) {
		t.Fatal("TrustedMemoryAuthority(trusted context) = false, want true")
	}
}

func TestActorOwner_EmptyForMissingOrNilProfile(t *testing.T) {
	profileID, profileName, ok := ActorOwner(context.Background())
	if ok || profileID != "" || profileName != "" {
		t.Fatalf("ActorOwner unset = %q,%q,%v; want empty,false", profileID, profileName, ok)
	}

	ctx := WithActorProfile(context.Background(), ActorProfile{ProfileName: "anonymous"})
	profileID, profileName, ok = ActorOwner(ctx)
	if ok || profileID != "" || profileName != "" {
		t.Fatalf("ActorOwner nil profile = %q,%q,%v; want empty,false", profileID, profileName, ok)
	}
}
