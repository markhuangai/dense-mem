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
}
