package claimdedupe

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type fakeClaimDedupeReader struct {
	rows        []map[string]any
	err         error
	lastProfile string
	lastQuery   string
	lastParams  map[string]any
}

func (f *fakeClaimDedupeReader) ScopedRead(_ context.Context, profileID string, query string, params map[string]any) (any, []map[string]any, error) {
	f.lastProfile = profileID
	f.lastQuery = query
	f.lastParams = params
	if f.err != nil {
		return nil, nil, f.err
	}
	return nil, f.rows, nil
}

func TestByIdempotencyKeyReturnsMappedClaim(t *testing.T) {
	recordedAt := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	reader := &fakeClaimDedupeReader{rows: []map[string]any{{
		"claim_id":           "claim-1",
		"subject":            "app",
		"predicate":          "uses",
		"object":             "TypeScript",
		"modality":           string(domain.ModalityAssertion),
		"polarity":           string(domain.PolarityPositive),
		"recorded_at":        recordedAt,
		"status":             string(domain.StatusCandidate),
		"supported_by":       []any{"fragment-1", "fragment-2"},
		"owner_profile_id":   "owner-1",
		"idempotency_key":    "key-1",
		"content_hash":       "hash-1",
		"classification":     map[string]any{"domain": "frontend"},
		"source_quality":     0.9,
		"extract_conf":       0.8,
		"resolution_conf":    0.7,
		"entailment_verdict": string(domain.VerdictInsufficient),
	}}}
	lookup := NewNeo4jDedupeLookup(reader)

	got, err := lookup.ByIdempotencyKey(context.Background(), "profile-1", "key-1")

	if err != nil {
		t.Fatalf("ByIdempotencyKey returned error: %v", err)
	}
	if got == nil {
		t.Fatal("ByIdempotencyKey returned nil claim")
	}
	if got.ClaimID != "claim-1" || got.ProfileID != "profile-1" {
		t.Fatalf("claim identity = %q/%q; want claim-1/profile-1", got.ClaimID, got.ProfileID)
	}
	if got.Subject != "app" || got.Predicate != "uses" || got.Object != "TypeScript" {
		t.Fatalf("claim triple = %q/%q/%q", got.Subject, got.Predicate, got.Object)
	}
	if len(got.SupportedBy) != 2 || got.SupportedBy[0] != "fragment-1" || got.SupportedBy[1] != "fragment-2" {
		t.Fatalf("SupportedBy = %v; want [fragment-1 fragment-2]", got.SupportedBy)
	}
	if reader.lastProfile != "profile-1" {
		t.Fatalf("ScopedRead profile = %q; want profile-1", reader.lastProfile)
	}
	if reader.lastParams["key"] != "key-1" {
		t.Fatalf("key param = %v; want key-1", reader.lastParams["key"])
	}
}

func TestByContentHashUsesActorOwnerScope(t *testing.T) {
	ownerID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		ProfileID:   ownerID,
		ProfileName: "web",
	})
	reader := &fakeClaimDedupeReader{}
	lookup := NewNeo4jDedupeLookup(reader)

	got, err := lookup.ByContentHash(ctx, "profile-1", "hash-1")

	if err != nil {
		t.Fatalf("ByContentHash returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("ByContentHash = %#v; want nil miss", got)
	}
	if reader.lastParams["hash"] != "hash-1" {
		t.Fatalf("hash param = %v; want hash-1", reader.lastParams["hash"])
	}
	if reader.lastParams["ownerProfileId"] != ownerID.String() {
		t.Fatalf("ownerProfileId = %v; want %s", reader.lastParams["ownerProfileId"], ownerID.String())
	}
}

func TestDedupeLookupMissesReturnNil(t *testing.T) {
	reader := &fakeClaimDedupeReader{}
	lookup := NewNeo4jDedupeLookup(reader)

	got, err := lookup.ByIdempotencyKey(context.Background(), "profile-1", "missing-key")
	if err != nil {
		t.Fatalf("ByIdempotencyKey returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("ByIdempotencyKey = %#v; want nil miss", got)
	}
	if reader.lastParams["ownerProfileId"] != "" {
		t.Fatalf("ownerProfileId = %v; want empty owner filter without actor context", reader.lastParams["ownerProfileId"])
	}

	got, err = lookup.ByContentHash(context.Background(), "profile-1", "missing-hash")
	if err != nil {
		t.Fatalf("ByContentHash returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("ByContentHash = %#v; want nil miss", got)
	}
	if reader.lastParams["hash"] != "missing-hash" {
		t.Fatalf("hash param = %v; want missing-hash", reader.lastParams["hash"])
	}
}

func TestDedupeLookupWrapsReadErrors(t *testing.T) {
	readErr := errors.New("neo4j unavailable")
	reader := &fakeClaimDedupeReader{err: readErr}
	lookup := NewNeo4jDedupeLookup(reader)

	_, err := lookup.ByIdempotencyKey(context.Background(), "profile-1", "key-1")
	if !errors.Is(err, readErr) {
		t.Fatalf("ByIdempotencyKey err = %v; want wrapped readErr", err)
	}

	_, err = lookup.ByContentHash(context.Background(), "profile-1", "hash-1")
	if !errors.Is(err, readErr) {
		t.Fatalf("ByContentHash err = %v; want wrapped readErr", err)
	}
}
