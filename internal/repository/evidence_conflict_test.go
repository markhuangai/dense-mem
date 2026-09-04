package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvidenceConflictIdentityUsesOccurrenceHashAndScope(t *testing.T) {
	base := resolvedEvidenceConflictCitation{CanonicalEvidenceID: "fragment-1", ContentHash: "occurrence-hash-1"}
	position := evidenceConflictPositionKey(base, 2, 7)
	base.ContentHash = "occurrence-hash-2"
	require.NotEqual(t, position, evidenceConflictPositionKey(base, 2, 7))

	keys := []string{"position-b", "position-a"}
	caseA := evidenceConflictCaseKey("team-a", "space-a", 1, keys)
	caseAReordered := evidenceConflictCaseKey("team-a", "space-a", 1, []string{"position-a", "position-b"})
	require.Equal(t, caseA, caseAReordered)
	require.NotEqual(t, caseA, evidenceConflictCaseKey("team-b", "space-a", 1, keys))
	require.NotEqual(t, caseA, evidenceConflictCaseKey("team-a", "space-b", 1, keys))
	require.NotEqual(t, caseA, evidenceConflictCaseKey("team-a", "space-a", 2, keys))
}
