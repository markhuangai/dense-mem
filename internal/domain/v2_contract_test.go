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
}
