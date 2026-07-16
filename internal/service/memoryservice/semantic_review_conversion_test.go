package memoryservice

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSemanticReviewConversionAcceptsStrictRelationship(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem stores durable memory in Postgres."}}
	units := splitSemanticReviewUnits(0, evidence[0].Content)

	relationships, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
		Relationships: []semanticReviewRelationship{semanticReviewRelationshipFixture("r1", "e0_u1")},
	})

	require.NoError(t, err)
	require.Len(t, relationships, 1)
	rel := relationships[0]
	require.Equal(t, "Dense-Mem", rel.SubjectName)
	require.Equal(t, domain.SemanticEntityProject, rel.SubjectKind)
	require.Equal(t, "stores_in", rel.Predicate)
	require.Equal(t, domain.PolarityPlus, rel.Polarity)
	require.Equal(t, domain.SemanticEntityProduct, rel.ObjectKind)
	require.Equal(t, "Postgres", rel.ObjectName)
	require.Equal(t, 0.8, rel.Confidence)
	require.Equal(t, evidence[0].Content, rel.Quote)
	require.Equal(t, 0, rel.SpanStart)
	require.Equal(t, len(evidence[0].Content), rel.SpanEnd)
}

func TestSemanticReviewConversionRejectsFormerFallbacks(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem stores durable memory in Postgres."}}
	units := splitSemanticReviewUnits(0, evidence[0].Content)
	tests := []struct {
		name    string
		mutate  func(*semanticReviewRelationship)
		wantErr string
	}{
		{
			name: "unknown entity kind",
			mutate: func(rel *semanticReviewRelationship) {
				rel.SubjectKind = "unexpected-kind"
			},
			wantErr: "subject_kind is invalid",
		},
		{
			name: "normalized predicate",
			mutate: func(rel *semanticReviewRelationship) {
				rel.Predicate = "Stores In"
			},
			wantErr: "predicate must be lower_snake_case ASCII",
		},
		{
			name: "missing exact quote",
			mutate: func(rel *semanticReviewRelationship) {
				rel.Quote = "missing exact quote"
			},
			wantErr: `quote "missing exact quote" is not an exact substring`,
		},
		{
			name: "clamped confidence",
			mutate: func(rel *semanticReviewRelationship) {
				rel.Confidence = -0.5
			},
			wantErr: "confidence is out of range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel := semanticReviewRelationshipFixture("r1", "e0_u1")
			tt.mutate(&rel)
			_, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
				Relationships: []semanticReviewRelationship{rel},
			})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSemanticReviewConversionRequiresExactUnitCoverage(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem stores durable memory in Postgres. It supports semantic recall."}}
	units := splitSemanticReviewUnits(0, evidence[0].Content)

	_, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
		Relationships: []semanticReviewRelationship{},
	})
	require.ErrorContains(t, err, "unit coverage mismatch: missing [e0_u1]")

	relationships, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
		Relationships: []semanticReviewRelationship{semanticReviewRelationshipFixture("r1", "e0_u1")},
	})
	require.NoError(t, err)
	require.Len(t, relationships, 1)
}

func TestSemanticReviewConversionRejectsRelationshipAndSkipForSameUnit(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem stores durable memory in Postgres."}}
	units := splitSemanticReviewUnits(0, evidence[0].Content)

	_, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
		Relationships: []semanticReviewRelationship{semanticReviewRelationshipFixture("r1", "e0_u1")},
		Skips:         []semanticReviewSkip{{UnitID: "e0_u1", Reason: "duplicate"}},
	})

	require.ErrorContains(t, err, "cannot contain relationships and a skip")
}

func TestSemanticReviewConversionRejectsDuplicateReviewerRefs(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem stores durable memory in Postgres."}}
	units := splitSemanticReviewUnits(0, evidence[0].Content)

	_, err := convertSemanticReview(evidence, units, semanticReviewAPIResult{
		Relationships: []semanticReviewRelationship{
			semanticReviewRelationshipFixture("r1", "e0_u1"),
			semanticReviewRelationshipFixture("r1", "e0_u1"),
		},
	})

	require.ErrorContains(t, err, "duplicate relationship ref")
}

func TestSplitSemanticReviewUnitsPreservesEvidenceItemBoundary(t *testing.T) {
	content := "On July 13, 2026, Dense-Mem used https://example.com/v2. It stored 42 facts."

	units := splitSemanticReviewUnits(7, content)

	require.Equal(t, []semanticReviewUnit{
		{UnitID: "e7_u1", EvidenceIndex: 7, Text: content, Start: 0, End: len(content)},
	}, units)
}

func TestSplitSemanticReviewUnitsSkipsBlankEvidence(t *testing.T) {
	require.Nil(t, splitSemanticReviewUnits(7, " \n\t "))
}

func TestSemanticReviewQuoteMatchNormalizesWhitespace(t *testing.T) {
	offset, exact, ok := semanticReviewQuoteMatch("Dense-Mem stores\n durable\tmemory.", "stores durable memory")

	require.True(t, ok)
	require.Equal(t, len("Dense-Mem "), offset)
	require.Equal(t, "stores\n durable\tmemory", exact)

	_, _, ok = semanticReviewQuoteMatch("Dense-Mem stores memory.", "\n\t")
	require.False(t, ok)

	_, _, ok = semanticReviewQuoteMatch("Dense-Mem stores memory.", "missing")
	require.False(t, ok)
}

func TestSemanticReviewRepairSnippet(t *testing.T) {
	require.Equal(t, "", semanticReviewRepairSnippet(" \n\t "))
	require.Equal(t, "short value", semanticReviewRepairSnippet(" short value "))

	long := strings.Repeat("x", semanticReviewRepairSnippetMaxRunes+1)
	require.Equal(t, strings.Repeat("x", semanticReviewRepairSnippetMaxRunes)+"…", semanticReviewRepairSnippet(long))
}

func TestSemanticReviewHelperValidators(t *testing.T) {
	for _, reason := range []string{"non_factual", " context_only ", "duplicate", "unsupported"} {
		require.True(t, semanticReviewSkipReasonValid(reason))
	}
	require.False(t, semanticReviewSkipReasonValid("unclear"))

	kind, err := semanticReviewEntityKind(" project ")
	require.NoError(t, err)
	require.Equal(t, domain.SemanticEntityProject, kind)
	_, err = semanticReviewEntityKind("event")
	require.ErrorContains(t, err, "invalid semantic entity kind")

	require.True(t, semanticReviewPredicateValid("uses_postgres2"))
	for _, predicate := range []string{"", "Uses", "uses__postgres", "uses_", "uses-postgres"} {
		require.False(t, semanticReviewPredicateValid(predicate))
	}
}

func TestDecodeClosedJSONRejectsTrailingData(t *testing.T) {
	var decoded semanticReviewAPIResult

	err := decodeClosedJSON(`{"relationships": []} {}`, &decoded)

	require.Error(t, err)
	var malformed *verifier.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Contains(t, err.Error(), "trailing data")
}

func TestDecodeClosedJSONRejectsUnknownFields(t *testing.T) {
	var decoded semanticReviewAPIResult

	err := decodeClosedJSON(`{"relationships": [], "extra": true}`, &decoded)

	require.Error(t, err)
	var malformed *verifier.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Contains(t, err.Error(), "failed to decode")
}

func semanticReviewRelationshipFixture(ref, unitID string) semanticReviewRelationship {
	return semanticReviewRelationship{
		Ref:         ref,
		UnitID:      unitID,
		SubjectName: "Dense-Mem",
		SubjectKind: "project",
		Predicate:   "stores_in",
		Polarity:    "+",
		ObjectName:  "Postgres",
		ObjectKind:  "product",
		Quote:       "Dense-Mem stores durable memory in Postgres.",
		Confidence:  0.8,
	}
}
