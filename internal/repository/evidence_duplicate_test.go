package repository

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateRememberDuplicateCandidateInputUsesByteExactContentHash(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	content := "  exact bytes  "
	input := RememberDuplicateCandidateInput{
		TeamID: teamID, OwnerProfileID: ownerID,
		Evidence: []EvidenceInput{{FragmentID: uuid.NewString(), Content: content, ContentHash: sha256Hex(content)}},
	}
	require.NoError(t, validateRememberDuplicateCandidateInput(input))

	input.Evidence[0].ContentHash = sha256Hex("exact bytes")
	require.ErrorContains(t, validateRememberDuplicateCandidateInput(input), "does not match content")
}

func TestValidateRememberDuplicateCandidateInputRejectsRepeatedSubmittedIDs(t *testing.T) {
	teamID, ownerID, firstID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	input := RememberDuplicateCandidateInput{
		TeamID: teamID, OwnerProfileID: ownerID,
		Evidence: []EvidenceInput{
			{FragmentID: firstID, Content: "one", ContentHash: sha256Hex("one")},
			{FragmentID: firstID, Content: "two", ContentHash: sha256Hex("two")},
		},
	}
	require.ErrorContains(t, validateRememberDuplicateCandidateInput(input), "fragment_id is duplicated")
}

func TestRememberDuplicateBatchCanonicalKeyExcludesForcedAndLifecycleEvidence(t *testing.T) {
	item := EvidenceInput{Content: "same bytes", ContentHash: sha256Hex("same bytes")}
	require.NotEmpty(t, rememberDuplicateBatchCanonicalKey(item))

	item.ForceInsert = true
	require.Empty(t, rememberDuplicateBatchCanonicalKey(item))

	item.ForceInsert = false
	item.SupersedesEvidenceIDs = []string{uuid.NewString()}
	require.Empty(t, rememberDuplicateBatchCanonicalKey(item))
}
