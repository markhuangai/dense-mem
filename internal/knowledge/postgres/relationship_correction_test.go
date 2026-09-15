package postgres

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCorrectRelationshipValidationRejectsInvalidConfirmation(t *testing.T) {
	input := normalizeCorrectRelationshipInput(CorrectRelationshipInput{
		TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), Action: "confirm",
		SubmissionID: uuid.NewString(), ConfirmationToken: uuid.NewString(), IdempotencyKey: "confirm",
	})
	err := validateCorrectRelationshipInput(input)
	require.ErrorContains(t, err, "selection")
}

func TestNormalizeCorrectRelationshipInputCanonicalizesWithoutMutatingCaller(t *testing.T) {
	teamID := uuid.NewString()
	profileID := uuid.NewString()
	relationshipID := uuid.NewString()
	entityID := uuid.NewString()
	firstEvidenceID := uuid.NewString()
	secondEvidenceID := uuid.NewString()
	callerSupports := []RelationshipCorrectionSupport{
		{EvidenceID: strings.ToUpper(secondEvidenceID), Start: 2, End: 3},
		{EvidenceID: strings.ToUpper(firstEvidenceID), Start: 0, End: 1},
	}
	callerPatch := &RelationshipCorrectionEntityPatch{EntityID: " " + strings.ToUpper(entityID) + " "}
	input := CorrectRelationshipInput{
		TeamID: strings.ToUpper(teamID), OwnerProfileID: strings.ToUpper(profileID), Action: " submit ",
		RelationshipID: strings.ToUpper(relationshipID), Patch: RelationshipCorrectionPatch{ObjectEntity: callerPatch},
		Supports: callerSupports,
	}

	normalized := normalizeCorrectRelationshipInput(input)

	require.Equal(t, teamID, normalized.TeamID)
	require.Equal(t, profileID, normalized.OwnerProfileID)
	require.Equal(t, relationshipID, normalized.RelationshipID)
	require.Equal(t, entityID, normalized.Patch.ObjectEntity.EntityID)
	require.ElementsMatch(t, []string{firstEvidenceID, secondEvidenceID}, []string{normalized.Supports[0].EvidenceID, normalized.Supports[1].EvidenceID})
	require.Equal(t, strings.ToUpper(secondEvidenceID), callerSupports[0].EvidenceID)
	require.Equal(t, " "+strings.ToUpper(entityID)+" ", callerPatch.EntityID)
}
