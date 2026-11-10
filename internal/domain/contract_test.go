package domain

import (
	"slices"
	"testing"
)

func TestContractEnums(t *testing.T) {
	if ContractVersion != "dense-mem.v3.0.0" {
		t.Fatalf("ContractVersion = %q", ContractVersion)
	}
	if !ContractVersionCompatible(ContractVersion) || ContractVersionCompatible(PreviousContractVersion) || ContractVersionCompatible("dense-mem.v2.7.0") {
		t.Fatal("only the Evidence-only v3 contract is compatible")
	}
	for _, status := range []string{"accepted", "quarantined", "rejected", "retryable", "terminal_failure", "superseded"} {
		if !slices.Contains(SemanticReviewStatuses(), status) {
			t.Fatalf("SemanticReviewStatuses missing %s", status)
		}
	}
	for _, state := range []string{"not_required", "pending", "current", "failed"} {
		if !slices.Contains(SearchProjectionStates(), state) {
			t.Fatalf("SearchProjectionStates missing %s", state)
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

func TestContractUsesTypedPublicIDs(t *testing.T) {
	ids := []any{
		IngestID("ing-1"),
		EvidenceID("ev-1"),
		MemoryPackID("pack-1"),
	}
	if len(ids) != 3 {
		t.Fatalf("typed ID count = %d", len(ids))
	}
}
