package verifier

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareSemanticAssessmentEvidenceUsesUnicodeCodePointBoundaries(t *testing.T) {
	content := "A😀界e\u0301"
	prepared := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "evidence:0", Content: content})
	runes := []rune(content)

	require.Len(t, prepared.BoundaryRefs, len(runes)+1)
	seen := map[string]struct{}{}
	for offset := 0; offset <= len(runes); offset++ {
		ref, ok := SemanticAssessmentBoundaryRef(prepared, offset)
		require.True(t, ok, "missing boundary %d", offset)
		_, duplicate := seen[ref]
		assert.False(t, duplicate, "duplicate boundary ref %q", ref)
		seen[ref] = struct{}{}
		resolved, ok := semanticAssessmentBoundaryOffset(prepared, ref)
		require.True(t, ok)
		assert.Equal(t, offset, resolved)
	}

	startRef, _ := SemanticAssessmentBoundaryRef(prepared, 1)
	endRef, _ := SemanticAssessmentBoundaryRef(prepared, 3)
	rangeValue := SemanticAssessmentGroundedRange{EvidenceID: prepared.EvidenceID, StartRef: startRef, EndRef: endRef}
	require.NoError(t, resolveSemanticAssessmentRange(map[string]SemanticReviewEvidence{prepared.EvidenceID: prepared}, &rangeValue))
	quote, err := SemanticEvidenceSpan(prepared.Content, rangeValue.Start, rangeValue.End)
	require.NoError(t, err)
	assert.Equal(t, "😀界", quote)
}

func TestPrepareSemanticAssessmentEvidenceDoesNotTrustMarkerLikeContent(t *testing.T) {
	const untrustedRef = "bdeadbeef_0"
	content := "literal ⟦" + untrustedRef + "⟧ marker"
	prepared := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "evidence:0", Content: content})

	restored := prepared.BoundaryText
	for ref := range prepared.BoundaryRefs {
		restored = strings.ReplaceAll(restored, "⟦"+ref+"⟧", "")
	}
	assert.Equal(t, content, restored)
	_, accepted := semanticAssessmentBoundaryOffset(prepared, untrustedRef)
	assert.False(t, accepted)
	for ref := range prepared.BoundaryRefs {
		assert.False(t, strings.Contains(content, "⟦"+ref), "generated prefix collided with evidence content")
	}
}
