package domain

import "testing"

func TestSemanticEntityKindContract(t *testing.T) {
	valid := []SemanticEntityKind{
		SemanticEntityUnknown,
		SemanticEntityPerson,
		SemanticEntityOrganization,
		SemanticEntityProject,
		SemanticEntityProduct,
		SemanticEntityPlace,
		SemanticEntityDocument,
		SemanticEntityConcept,
	}
	for _, kind := range valid {
		if !kind.IsValid() {
			t.Fatalf("entity kind %q should be valid", kind)
		}
	}
	if SemanticEntityKind("event").IsValid() {
		t.Fatal("unrecognized entity kind should be invalid")
	}
}

func TestSemanticRelationshipTierContract(t *testing.T) {
	valid := []SemanticRelationshipTier{
		SemanticTierCandidate,
		SemanticTierValidatedClaim,
		SemanticTierFact,
	}
	for _, tier := range valid {
		if !tier.IsValid() {
			t.Fatalf("tier %q should be valid", tier)
		}
	}

	for _, tier := range []SemanticRelationshipTier{"", "dream", "assertion", "validated"} {
		if tier.IsValid() {
			t.Fatalf("tier %q should be invalid", tier)
		}
	}
}

func TestSemanticStatusAndHypothesisAreSeparate(t *testing.T) {
	if !SemanticStatusActive.IsValid() {
		t.Fatal("active relationship status should be valid")
	}
	if SemanticRelationshipStatus("proposed").IsValid() {
		t.Fatal("relationship status must not accept hypothesis lifecycle values")
	}
	if !SemanticHypothesisProposed.IsValid() {
		t.Fatal("proposed hypothesis status should be valid")
	}
	if SemanticHypothesisStatus("fact").IsValid() {
		t.Fatal("hypothesis status must not accept relationship tiers")
	}
}
