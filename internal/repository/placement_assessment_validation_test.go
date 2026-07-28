package repository

import "testing"

func TestValidateSemanticReviewDetailsRequiresSelectableOptions(t *testing.T) {
	if err := validateSemanticReviewDetails("assessment-1", "identity", "Choose an entity.", nil, "Select a supplied option."); err == nil {
		t.Fatal("validateSemanticReviewDetails() accepted an identity review without options")
	}
	if err := validateSemanticReviewDetails("assessment-1", "scope", "Provide evidence.", []map[string]any{{"action": "submit_new_evidence"}}, "Submit exact evidence."); err != nil {
		t.Fatalf("validateSemanticReviewDetails() error = %v", err)
	}
}
