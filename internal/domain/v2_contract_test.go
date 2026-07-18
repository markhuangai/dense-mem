package domain

import (
	"slices"
	"testing"
)

func TestV2ContractEnums(t *testing.T) {
	if V2ContractVersion != "dense-mem.v2.1" {
		t.Fatalf("V2ContractVersion = %q", V2ContractVersion)
	}
	for _, action := range []string{
		"acknowledge",
		"select_entity",
		"confirm_new_entity",
		"select_predicate",
		"accept",
		"reject",
		"correct",
		"release_quarantine",
		"forget",
	} {
		if !slices.Contains(V2ResolveActions(), action) {
			t.Fatalf("V2ResolveActions missing %s", action)
		}
	}
	for _, kind := range []string{"state", "event"} {
		if !slices.Contains(V2RelationshipKinds(), kind) {
			t.Fatalf("V2RelationshipKinds missing %s", kind)
		}
	}
	for _, cardinality := range []string{"one", "many"} {
		if !slices.Contains(V2CurrentCardinalities(), cardinality) {
			t.Fatalf("V2CurrentCardinalities missing %s", cardinality)
		}
	}
	for _, category := range []string{
		"relationship_fact",
		"relationship_validated_claim",
		"relationship_pending_evidence",
		"relationship_needs_review",
		"predicate_needs_review",
		"relationship_rejected",
		"identity_needs_review",
	} {
		if !slices.Contains(V2RelationshipOutcomeCategories(), category) {
			t.Fatalf("V2RelationshipOutcomeCategories missing %s", category)
		}
	}
	for _, tier := range []string{"candidate", "validated_claim", "fact"} {
		if !slices.Contains(V2RelationshipTiers(), tier) {
			t.Fatalf("V2RelationshipTiers missing %s", tier)
		}
	}
	for _, status := range []string{"active", "pending_evidence", "disputed", "retracted"} {
		if !slices.Contains(V2RelationshipStatuses(), status) {
			t.Fatalf("V2RelationshipStatuses missing %s", status)
		}
	}
	for _, verdict := range []string{"entailed", "contradicted", "insufficient"} {
		if !slices.Contains(V2VerificationVerdicts(), verdict) {
			t.Fatalf("V2VerificationVerdicts missing %s", verdict)
		}
	}
	for _, kind := range []string{"confirms", "challenges", "corrects", "adopts_evidence_from"} {
		if !slices.Contains(V2CrossReferenceKinds(), kind) {
			t.Fatalf("V2CrossReferenceKinds missing %s", kind)
		}
	}
	for _, status := range []string{"queued", "processing", "awaiting_review", "completed", "failed"} {
		if !slices.Contains(V2PlacementRunStatuses(), status) {
			t.Fatalf("V2PlacementRunStatuses missing %s", status)
		}
	}
	if slices.Contains(V2PlacementRunStatuses(), "stale") {
		t.Fatal("V2PlacementRunStatuses contains non-canonical stale state")
	}
	for _, state := range []string{"not_required", "pending", "current", "failed"} {
		if !slices.Contains(V2SearchProjectionStates(), state) {
			t.Fatalf("V2SearchProjectionStates missing %s", state)
		}
	}
	if slices.Contains(V2SearchProjectionStates(), "stale") {
		t.Fatal("V2SearchProjectionStates contains non-canonical stale state")
	}
	for _, category := range []string{"evidence_processed", "evidence_quarantined", "processing_failed"} {
		if !slices.Contains(V2EvidenceItemCategories(), category) {
			t.Fatalf("V2EvidenceItemCategories missing %s", category)
		}
	}
	if slices.Contains(V2EvidenceItemCategories(), "evidence_needs_review") {
		t.Fatal("V2EvidenceItemCategories contains non-canonical evidence_needs_review category")
	}
}

func TestV2HypothesisStatusesAreCanonical(t *testing.T) {
	want := []string{"proposed", "reinforced", "stale", "rejected", "submitted"}
	if got := V2HypothesisStatuses(); !slices.Equal(got, want) {
		t.Fatalf("V2HypothesisStatuses = %#v, want %#v", got, want)
	}
	for _, legacy := range []string{"candidate", "promoted_candidate"} {
		if slices.Contains(V2HypothesisStatuses(), legacy) {
			t.Fatalf("V2HypothesisStatuses contains legacy status %q", legacy)
		}
	}
}

func TestV2ContractUsesTypedPublicIDs(t *testing.T) {
	ids := []any{
		V2IngestID("ing-1"),
		V2PlacementItemID("item-1"),
		V2EvidenceID("ev-1"),
		V2ObservationID("obs-1"),
		V2EntityID("ent-1"),
		V2ValueID("value-1"),
		V2RelationshipID("rel-1"),
		V2HypothesisID("hyp-1"),
		V2CommunityID("community-1"),
		V2MemoryPackID("pack-1"),
	}
	if len(ids) != 10 {
		t.Fatalf("typed ID count = %d", len(ids))
	}
}
