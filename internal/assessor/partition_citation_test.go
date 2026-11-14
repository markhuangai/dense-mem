package assessor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRememberPartitionUnspacedCitationAnchors(t *testing.T) {
	for _, length := range []int{5000, 16384} {
		content := strings.Repeat("界", length)
		submitted := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "submitted", Content: content})
		known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "known", Content: "contradiction"})
		start := -1
		for _, offset := range submitted.BoundaryRefs {
			if offset > start && offset < length && length-offset <= SemanticAssessmentMaxEvidenceConflictQuoteRunes {
				start = offset
			}
		}
		require.GreaterOrEqual(t, start, 0, "long unspaced text needs a bounded tail citation")
		startRef, ok := SemanticAssessmentBoundaryRef(submitted, start)
		require.True(t, ok)
		endRef, ok := SemanticAssessmentBoundaryRef(submitted, length)
		require.True(t, ok)
		knownStart, _ := SemanticAssessmentBoundaryRef(known, 0)
		knownEnd, _ := SemanticAssessmentBoundaryRef(known, len([]rune(known.Content)))
		response := SemanticAssessmentResponse{EvidenceConflictResults: []SemanticAssessmentEvidenceConflictResult{{Positions: []SemanticAssessmentEvidenceConflictPosition{
			{EvidenceID: "submitted", StartRef: startRef, EndRef: endRef}, {EvidenceID: "known", StartRef: knownStart, EndRef: knownEnd},
		}}}}
		require.Empty(t, resolveSemanticAssessmentEvidenceConflictResults(SemanticAssessmentRequest{Evidence: []SemanticReviewEvidence{submitted}, KnownEvidence: []SemanticReviewEvidence{known}}, &response))
		quote, err := SemanticEvidenceSpan(content, response.EvidenceConflictResults[0].Positions[0].Start, response.EvidenceConflictResults[0].Positions[0].End)
		require.NoError(t, err)
		require.Equal(t, string([]rune(content)[start:]), quote)
		require.Len(t, PreparePartitionRequest("unspaced", []string{content}).Items[0].Refs, 2, "citation anchors cannot become semantic cut points")
	}
}

func TestRememberPartitionTailCitationResolvesExactUnicodeSourceOffsets(t *testing.T) {
	const first = "Independent prelude.\n\n"
	const quote = "tail quote."
	content := first + strings.Repeat("界", 16384-len([]rune(first))-len([]rune(quote))-1) + " " + quote
	req := PreparePartitionRequest("tail-citation", []string{content})
	var cut, final string
	for ref, offset := range req.Items[0].Refs {
		if offset == len([]rune(first)) {
			cut = ref
		}
		if offset == 16384 {
			final = ref
		}
	}
	require.NotEmpty(t, cut)
	require.NotEmpty(t, final)
	body, err := json.Marshal(PartitionResponse{RequestID: req.RequestID, Items: []PartitionResult{{ItemID: req.Items[0].ItemID, EndRefs: []string{cut, final}}}})
	require.NoError(t, err)
	spans, err := ValidatePartitionResponse(req, body)
	require.NoError(t, err)
	children := []SemanticReviewEvidence{}
	for i, span := range spans[0] {
		id := []string{"prelude", "tail"}[i]
		children = append(children, PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: id, Content: string([]rune(content)[span[0]:span[1]])}))
	}
	tail := children[1]
	tailEnd := len([]rune(tail.Content))
	tailStart := tailEnd - len([]rune(quote))
	startRef, ok := SemanticAssessmentBoundaryRef(tail, tailStart)
	require.True(t, ok)
	endRef, ok := SemanticAssessmentBoundaryRef(tail, tailEnd)
	require.True(t, ok)
	preludeStart, _ := SemanticAssessmentBoundaryRef(children[0], 0)
	preludeEnd, _ := SemanticAssessmentBoundaryRef(children[0], len([]rune(first)))
	request, errs := PrepareSemanticAssessmentRequest(SemanticAssessmentRequest{RequestID: "tail-assessment", TeamID: "team", Evidence: children}, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	response, errs := PrepareSemanticAssessmentResponse(request, SemanticAssessmentResponse{RequestID: request.RequestID,
		EvidenceSecurityResults: []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "prelude", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}, {EvidenceID: "tail", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}}, EvidenceEquivalenceResults: []SemanticAssessmentEvidenceEquivalenceResult{},
		EvidenceConflictResults: []SemanticAssessmentEvidenceConflictResult{{Positions: []SemanticAssessmentEvidenceConflictPosition{{EvidenceID: "prelude", StartRef: preludeStart, EndRef: preludeEnd}, {EvidenceID: "tail", StartRef: startRef, EndRef: endRef}}}},
	}, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	citation := response.EvidenceConflictResults[0].Positions[1]
	exact, err := SemanticEvidenceSpan(tail.Content, citation.Start, citation.End)
	require.NoError(t, err)
	require.Equal(t, quote, exact)
	require.Equal(t, 16384, spans[0][1][0]+citation.End)
	require.Equal(t, 16384-len([]rune(quote)), spans[0][1][0]+citation.Start)
	for _, offset := range req.Items[0].Refs {
		require.NotEqual(t, 16384-len([]rune(quote)), offset, "word citation anchors cannot be partition boundaries")
	}
	_, ok = SemanticAssessmentBoundaryRef(tail, tailEnd+1)
	require.False(t, ok)
}
