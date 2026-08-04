package domain

import (
	"slices"
	"testing"
)

func TestContractEnums(t *testing.T) {
	if ContractVersion != "dense-mem.v2.4" {
		t.Fatalf("ContractVersion = %q", ContractVersion)
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
		if !slices.Contains(ResolveActions(), action) {
			t.Fatalf("ResolveActions missing %s", action)
		}
	}
	for _, kind := range []string{"state", "event"} {
		if !slices.Contains(RelationshipKinds(), kind) {
			t.Fatalf("RelationshipKinds missing %s", kind)
		}
	}
	for _, cardinality := range []string{"one", "many"} {
		if !slices.Contains(CurrentCardinalities(), cardinality) {
			t.Fatalf("CurrentCardinalities missing %s", cardinality)
		}
	}
	for _, state := range []string{"active", "deprecated", "retired"} {
		if !slices.Contains(PredicateLifecycleStates(), state) {
			t.Fatalf("PredicateLifecycleStates missing %s", state)
		}
	}
	for _, action := range []string{"reuse", "create", "ambiguous"} {
		if !slices.Contains(EntityResolutionActions(), action) {
			t.Fatalf("EntityResolutionActions missing %s", action)
		}
	}
	for _, decision := range []string{"grant", "revoke", "reinstate"} {
		if !slices.Contains(SupportDecisions(), decision) {
			t.Fatalf("SupportDecisions missing %s", decision)
		}
	}
	for _, action := range []string{"merge", "split"} {
		if !slices.Contains(EntityCorrectionActions(), action) {
			t.Fatalf("EntityCorrectionActions missing %s", action)
		}
	}
	for _, category := range []string{
		"relationship_accepted",
		"relationship_pending_evidence",
		"relationship_needs_review",
		"predicate_needs_review",
		"relationship_rejected",
		"identity_needs_review",
	} {
		if !slices.Contains(RelationshipOutcomeCategories(), category) {
			t.Fatalf("RelationshipOutcomeCategories missing %s", category)
		}
	}
	for _, status := range []string{"accepted", "review_required", "quarantined", "rejected", "retryable", "terminal_failure"} {
		if !slices.Contains(SemanticReviewStatuses(), status) {
			t.Fatalf("SemanticReviewStatuses missing %s", status)
		}
	}
	for _, status := range []string{"active", "pending_evidence", "disputed", "retracted"} {
		if !slices.Contains(RelationshipStatuses(), status) {
			t.Fatalf("RelationshipStatuses missing %s", status)
		}
	}
	for _, verdict := range []string{"entailed", "contradicted", "insufficient"} {
		if !slices.Contains(VerificationVerdicts(), verdict) {
			t.Fatalf("VerificationVerdicts missing %s", verdict)
		}
	}
	for _, kind := range []string{"confirms", "challenges", "corrects", "adopts_evidence_from"} {
		if !slices.Contains(CrossReferenceKinds(), kind) {
			t.Fatalf("CrossReferenceKinds missing %s", kind)
		}
	}
	for _, status := range []string{"open", "overdue", "resolved", "dismissed"} {
		if !slices.Contains(RelationshipConflictStatuses(), status) {
			t.Fatalf("RelationshipConflictStatuses missing %s", status)
		}
	}
	for _, kind := range []string{"cross_profile_current_state"} {
		if !slices.Contains(RelationshipConflictKinds(), kind) {
			t.Fatalf("RelationshipConflictKinds missing %s", kind)
		}
	}
	for _, disposition := range []string{"candidate", "preferred", "suppressed_current"} {
		if !slices.Contains(RelationshipConflictPositionDispositions(), disposition) {
			t.Fatalf("RelationshipConflictPositionDispositions missing %s", disposition)
		}
	}
	for _, status := range []string{"queued", "guarded", "quarantined", "processing", "awaiting_review", "completed", "failed"} {
		if !slices.Contains(PlacementRunStatuses(), status) {
			t.Fatalf("PlacementRunStatuses missing %s", status)
		}
	}
	if slices.Contains(PlacementRunStatuses(), "stale") {
		t.Fatal("PlacementRunStatuses contains non-canonical stale state")
	}
	for _, state := range []string{"not_required", "pending", "current", "failed"} {
		if !slices.Contains(SearchProjectionStates(), state) {
			t.Fatalf("SearchProjectionStates missing %s", state)
		}
	}
	for _, status := range []string{"queued", "guarded", "quarantined", "processing", "completed", "failed"} {
		if !slices.Contains(PlacementRunStatuses(), status) {
			t.Fatalf("PlacementRunStatuses missing %s", status)
		}
	}
	if slices.Contains(SearchProjectionStates(), "stale") {
		t.Fatal("SearchProjectionStates contains non-canonical stale state")
	}
	for _, state := range []string{"building", "active", "failed", "deprecated", "retired"} {
		if !slices.Contains(SearchIndexGenerationStates(), state) {
			t.Fatalf("SearchIndexGenerationStates missing %s", state)
		}
	}
	for _, strategy := range []string{"exact", "vector_hnsw", "halfvec_hnsw", "binary_hnsw"} {
		if !slices.Contains(VectorIndexStrategies(), strategy) {
			t.Fatalf("VectorIndexStrategies missing %s", strategy)
		}
	}
	if got := VectorDistanceMetrics(); !slices.Equal(got, []string{"cosine"}) {
		t.Fatalf("VectorDistanceMetrics = %#v", got)
	}
	for _, kind := range []string{"person", "organization", "project", "product", "place", "document", "concept", "other"} {
		if !slices.Contains(EntityKinds(), kind) {
			t.Fatalf("EntityKinds missing %s", kind)
		}
	}
	for _, valueType := range []string{"string", "number", "boolean", "date", "date_time"} {
		if !slices.Contains(ValueTypes(), valueType) {
			t.Fatalf("ValueTypes missing %s", valueType)
		}
	}
	for _, status := range []string{"queued", "processing", "completed", "failed", "stale", "cancelled"} {
		if !slices.Contains(EmbeddingJobStatuses(), status) {
			t.Fatalf("EmbeddingJobStatuses missing %s", status)
		}
	}
	for _, category := range []string{"evidence_processed", "evidence_quarantined", "processing_failed"} {
		if !slices.Contains(EvidenceItemCategories(), category) {
			t.Fatalf("EvidenceItemCategories missing %s", category)
		}
	}
	if slices.Contains(EvidenceItemCategories(), "evidence_needs_review") {
		t.Fatal("EvidenceItemCategories contains non-canonical evidence_needs_review category")
	}
}

func TestHypothesisStatusesAreCanonical(t *testing.T) {
	want := []string{"proposed", "reinforced", "stale", "rejected", "submitted"}
	if got := HypothesisStatuses(); !slices.Equal(got, want) {
		t.Fatalf("HypothesisStatuses = %#v, want %#v", got, want)
	}
	for _, legacy := range []string{"candidate", "promoted_candidate"} {
		if slices.Contains(HypothesisStatuses(), legacy) {
			t.Fatalf("HypothesisStatuses contains legacy status %q", legacy)
		}
	}
}

func TestContractUsesTypedPublicIDs(t *testing.T) {
	ids := []any{
		IngestID("ing-1"),
		PlacementItemID("item-1"),
		EvidenceID("ev-1"),
		ObservationID("obs-1"),
		EntityID("ent-1"),
		ValueID("value-1"),
		RelationshipID("rel-1"),
		HypothesisID("hyp-1"),
		CommunityID("community-1"),
		MemoryPackID("pack-1"),
	}
	if len(ids) != 10 {
		t.Fatalf("typed ID count = %d", len(ids))
	}
}
