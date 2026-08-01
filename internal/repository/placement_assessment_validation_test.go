package repository

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateSemanticReviewDetailsRequiresSelectableOptions(t *testing.T) {
	if err := validateSemanticReviewDetails("", uuid.NewString(), "identity", "Choose an entity.", nil, "Select a supplied option."); err == nil {
		t.Fatal("validateSemanticReviewDetails() accepted an identity review without options")
	}
	if err := validateSemanticReviewDetails("", uuid.NewString(), "scope", "Provide evidence.", []map[string]any{{"action": "submit_new_evidence"}}, "Submit exact evidence."); err != nil {
		t.Fatalf("validateSemanticReviewDetails() error = %v", err)
	}
}

func TestValidateAssessmentReferencesRequiresOneProvenanceSource(t *testing.T) {
	placementAssessmentID := uuid.NewString()
	submissionAssessmentID := uuid.NewString()
	require.Error(t, validateAssessmentReferences(placementAssessmentID, submissionAssessmentID))
	require.NoError(t, validateAssessmentReferences(placementAssessmentID, ""))
	require.NoError(t, validateAssessmentReferences("", submissionAssessmentID))
}

func TestSemanticAssessmentKnownEntityInputValidation(t *testing.T) {
	teamID := uuid.NewString()
	profileID := uuid.NewString()
	firstID := uuid.NewString()
	secondID := uuid.NewString()
	input := normalizeSemanticAssessmentKnownEntityInput(SemanticAssessmentKnownEntityInput{
		TeamID:         " " + teamID + " ",
		OwnerProfileID: " " + profileID + " ",
		EntityIDs:      []string{" " + firstID + " ", firstID, "", secondID},
	})
	if input.TeamID != teamID || input.OwnerProfileID != profileID {
		t.Fatalf("normalized scope = %q/%q", input.TeamID, input.OwnerProfileID)
	}
	if len(input.EntityIDs) != 2 || input.EntityIDs[0] != firstID || input.EntityIDs[1] != secondID {
		t.Fatalf("normalized entity IDs = %#v", input.EntityIDs)
	}
	if err := validateSemanticAssessmentKnownEntityInput(input); err != nil {
		t.Fatalf("validateSemanticAssessmentKnownEntityInput() error = %v", err)
	}

	input.EntityIDs = []string{"not-a-uuid"}
	if err := validateSemanticAssessmentKnownEntityInput(input); err == nil || !strings.Contains(err.Error(), "entity_id is invalid") {
		t.Fatalf("invalid entity ID error = %v", err)
	}

	input.EntityIDs = make([]string, semanticAssessmentMaxKnownEntityIDs+1)
	for index := range input.EntityIDs {
		input.EntityIDs[index] = uuid.NewString()
	}
	if err := validateSemanticAssessmentKnownEntityInput(input); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("oversized entity ID error = %v", err)
	}
}
