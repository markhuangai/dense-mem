package assessor

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
	require.NoError(t, ResolveSemanticAssessmentRange(map[string]SemanticReviewEvidence{prepared.EvidenceID: prepared}, &rangeValue))
	quote, err := SemanticEvidenceSpan(prepared.Content, rangeValue.Start, rangeValue.End)
	require.NoError(t, err)
	assert.Equal(t, "😀界", quote)
}

func TestSemanticAssessmentRangeFailuresHaveAuthoritativeReasons(t *testing.T) {
	evidence := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "evidence:0", Content: "A😀界e\u0301 literal ⟦bdeadbeef_0⟧"})
	other := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "evidence:1", Content: "Another document"})
	start, _ := SemanticAssessmentBoundaryRef(evidence, 1)
	end, _ := SemanticAssessmentBoundaryRef(evidence, 3)
	foreign, _ := SemanticAssessmentBoundaryRef(other, 1)
	for _, test := range []struct {
		name, evidenceID, start, end, code, message string
	}{
		{"unknown evidence", "unknown", start, end, "range_evidence_unknown", "supplied evidence_id"},
		{"literal marker", evidence.EvidenceID, "bdeadbeef_0", end, "range_boundary_unknown", "boundary_text"},
		{"foreign marker", evidence.EvidenceID, foreign, end, "range_boundary_unknown", "boundary_text"},
		{"unknown end", evidence.EvidenceID, start, "provider-canary", "range_boundary_unknown", "boundary_text"},
		{"empty", evidence.EvidenceID, start, start, "range_order_invalid", "start inclusive"},
		{"reversed", evidence.EvidenceID, end, start, "range_order_invalid", "end exclusive"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := SemanticAssessmentGroundedRange{EvidenceID: test.evidenceID, StartRef: test.start, EndRef: test.end}
			err := ResolveSemanticAssessmentRange(map[string]SemanticReviewEvidence{evidence.EvidenceID: evidence, other.EvidenceID: other}, &value)
			var issue SemanticValidationError
			require.ErrorAs(t, err, &issue)
			require.Equal(t, test.code, issue.Code)
			require.Contains(t, issue.Message, test.message)
			require.NotContains(t, issue.Message, "provider-canary")
			require.NotContains(t, issue.Message, evidence.Content)
			require.Zero(t, value.Start)
			require.Zero(t, value.End)
		})
	}
	err := ResolveSemanticAssessmentRange(nil, nil)
	var issue SemanticValidationError
	require.ErrorAs(t, err, &issue)
	require.Equal(t, "range_required", issue.Code)
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
