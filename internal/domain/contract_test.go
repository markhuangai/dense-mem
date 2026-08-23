package domain

import (
	"slices"
	"testing"
)

func TestContractEnums(t *testing.T) {
	if ContractVersion != "dense-mem.v2.6" {
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
	for _, status := range []string{"accepted", "quarantined", "rejected", "retryable", "terminal_failure", "superseded"} {
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
	for _, class := range []string{"transient", "provider_action_required", "permanent"} {
		if !slices.Contains(EmbeddingFailureClasses(), class) {
			t.Fatalf("EmbeddingFailureClasses missing %s", class)
		}
	}
	wantFailureCodes := []string{
		"provider_rate_limited", "provider_timeout", "provider_network_error", "provider_server_error",
		"provider_quota_exhausted", "provider_authentication_failed", "provider_permission_denied",
		"provider_contract_rejected", "provider_response_invalid", "embedding_input_rejected",
		"embedding_contract_mismatch", "unknown_embedding_failure",
	}
	if got := EmbeddingFailureCodes(); !slices.Equal(got, wantFailureCodes) {
		t.Fatalf("EmbeddingFailureCodes = %#v, want %#v", got, wantFailureCodes)
	}
}

func TestEmbeddingFailureContractValidatesClassCodePairs(t *testing.T) {
	for _, test := range []struct {
		class string
		code  string
		want  bool
	}{
		{"transient", "provider_timeout", true},
		{"provider_action_required", "provider_quota_exhausted", true},
		{"permanent", "embedding_input_rejected", true},
		{"transient", "provider_quota_exhausted", false},
		{"permanent", "provider_timeout", false},
		{"unknown", "unknown_embedding_failure", false},
	} {
		if got := EmbeddingFailureContractValid(test.class, test.code); got != test.want {
			t.Fatalf("EmbeddingFailureContractValid(%q, %q) = %t, want %t", test.class, test.code, got, test.want)
		}
	}
	if !EmbeddingFailureCodeValid("provider_timeout") || EmbeddingFailureCodeValid("provider_broken") {
		t.Fatal("EmbeddingFailureCodeValid accepted an invalid code")
	}
}

func TestCombineSearchProjectionStatesUsesDegradedPrecedence(t *testing.T) {
	for _, test := range []struct {
		name        string
		left, right string
		want        string
	}{
		{name: "failed left", left: "failed", right: "current", want: "failed"},
		{name: "failed right", left: "current", right: "failed", want: "failed"},
		{name: "pending", left: "current", right: "pending", want: "pending"},
		{name: "current", left: "not_required", right: "current", want: "current"},
		{name: "not required", left: "not_required", right: "", want: "not_required"},
		{name: "empty left", left: "", right: "custom", want: "custom"},
		{name: "unknown left", left: "custom", right: "other", want: "custom"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := CombineSearchProjectionStates(test.left, test.right); got != test.want {
				t.Fatalf("CombineSearchProjectionStates(%q, %q) = %q, want %q", test.left, test.right, got, test.want)
			}
		})
	}
}

func TestEmbeddingFailureMessageUsesBoundedMessages(t *testing.T) {
	for _, test := range []struct {
		code string
		want string
	}{
		{string(EmbeddingFailureProviderRateLimited), "embedding provider rate limited"},
		{string(EmbeddingFailureProviderTimeout), "embedding provider timed out"},
		{string(EmbeddingFailureProviderNetworkError), "embedding provider network failure"},
		{string(EmbeddingFailureProviderServerError), "embedding provider server failure"},
		{string(EmbeddingFailureProviderQuotaExhausted), "embedding provider quota exhausted"},
		{string(EmbeddingFailureProviderAuthentication), "embedding provider authentication failed"},
		{string(EmbeddingFailureProviderPermissionDenied), "embedding provider permission denied"},
		{string(EmbeddingFailureProviderContractRejected), "embedding provider contract rejected"},
		{string(EmbeddingFailureProviderResponseInvalid), "embedding provider response invalid"},
		{string(EmbeddingFailureInputRejected), "embedding input rejected"},
		{string(EmbeddingFailureContractMismatch), "embedding contract mismatch"},
		{"unknown", "embedding processing failed"},
	} {
		if got := EmbeddingFailureMessage(test.code); got != test.want {
			t.Fatalf("EmbeddingFailureMessage(%q) = %q, want %q", test.code, got, test.want)
		}
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
