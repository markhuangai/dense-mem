package evalharness

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupedDecoderAndScorerPreserveSourceCoverageAndSlots(t *testing.T) {
	var output map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"ranked_refs":[{"type":"evidence","id":"representative","rank":1,"equivalent_evidence_ids":["alternate","bad-a","bad-b"]},{"type":"evidence","id":"later","rank":2}],"context_evidence_refs":[{"type":"evidence","id":"representative","rank":1,"equivalent_evidence_ids":["alternate","bad-a","bad-b"]},{"type":"evidence","id":"later","rank":2}]}`), &output))
	trace := traceFromToolOutput(Case{CaseID: "grouped"}, output)
	require.Len(t, trace.RankedRefs, 2)
	require.Len(t, trace.ContextEvidenceRefs, 2)
	required := []Ref{{Type: "evidence", ID: "representative", Grade: 3}, {Type: "evidence", ID: "alternate", Grade: 2}}
	bad := []Ref{{Type: "evidence", ID: "bad-a"}, {Type: "evidence", ID: "bad-b"}}
	qrel := QRel{RequiredRefs: required, BadRefs: bad, RequiredEvidenceRefs: required, BadEvidenceRefs: bad}
	score := ScoreTrace("grouped", 1, qrel, trace, newKnowledgeMapping())
	require.Equal(t, 1, score.K)
	require.Equal(t, 2, score.RelevantAtK)
	require.Equal(t, 1.0, score.RecallAtK)
	require.Equal(t, 1.0, score.MRR)
	require.Equal(t, 1.0, score.NDCGAtK)
	require.Equal(t, 1, score.FirstRequiredRank)
	require.Empty(t, score.MissingRequired)
	require.Equal(t, 1, score.BadAtK)
	require.Equal(t, 1, score.FirstBadRank)
	require.Equal(t, bad, score.BadRefsAtK)
	require.Equal(t, score.RecallAtK, score.EvidenceRecallAtK)
	require.Equal(t, score.MRR, score.EvidenceMRR)
	require.Equal(t, score.NDCGAtK, score.EvidenceNDCGAtK)
	require.Equal(t, score.BadRefsAtK, score.EvidenceBadRefsAtK)
	later := QRel{RequiredRefs: []Ref{{Type: "evidence", ID: "later", Grade: 1}}}
	outside := ScoreTrace("later", 1, later, trace, newKnowledgeMapping())
	require.Zero(t, outside.RecallAtK)
	require.Equal(t, 2, outside.FirstRequiredRank)
	inside := ScoreTrace("later", 2, later, trace, newKnowledgeMapping())
	require.Equal(t, 1.0, inside.RecallAtK)
	require.Equal(t, 0.5, inside.MRR)
}

func TestGroupedScorerCreditsOnlyDisplayedTwentyAlternates(t *testing.T) {
	alternates := []string{}
	for index := 0; index < 20; index++ {
		alternates = append(alternates, fmt.Sprintf("alternate-%02d", index))
	}
	body, err := json.Marshal(map[string]any{"ranked_refs": []map[string]any{{"type": "evidence", "id": "representative", "rank": 1, "equivalent_evidence_ids": alternates, "equivalents_truncated": true}}})
	require.NoError(t, err)
	var output map[string]any
	require.NoError(t, json.Unmarshal(body, &output))
	trace := traceFromToolOutput(Case{CaseID: "truncated"}, output)
	require.True(t, trace.RankedRefs[0].EquivalentsTruncated)
	required := []Ref{{Type: "evidence", ID: "representative", Grade: 1}}
	for _, id := range alternates {
		required = append(required, Ref{Type: "evidence", ID: id, Grade: 1})
	}
	missing := Ref{Type: "evidence", ID: "undisplayed", Grade: 1}
	required = append(required, missing)
	qrel := QRel{RequiredRefs: required, BadRefs: []Ref{{Type: "evidence", ID: missing.ID}}}
	score := ScoreTrace("truncated", 1, qrel, trace, newKnowledgeMapping())
	require.Equal(t, 21, score.RelevantAtK)
	require.Equal(t, 22, score.RelevantTotal)
	require.Equal(t, []Ref{missing}, score.MissingRequired)
	require.Zero(t, score.BadAtK)
	require.Empty(t, score.BadRefsAtK)
	encoded, err := json.Marshal(trace)
	require.NoError(t, err)
	var replay RecallTrace
	require.NoError(t, json.Unmarshal(encoded, &replay))
	require.Equal(t, score, ScoreTrace("truncated", 1, qrel, replay, newKnowledgeMapping()))
}

func TestGroupedScorerResolvesAlternateSourceJudgmentsAndKeepsUngroupedSemantics(t *testing.T) {
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "evidence", ID: "alternate", SourceDocID: "source"}, true)
	trace := traceFromToolOutput(Case{CaseID: "source"}, map[string]any{"ranked_refs": []any{map[string]any{"type": "evidence", "id": "representative", "rank": 1, "equivalent_evidence_ids": []any{"alternate"}}}})
	score := ScoreTrace("source", 1, QRel{RequiredRefs: []Ref{{Type: "evidence", SourceDocID: "source", Grade: 1}}}, trace, mapping)
	require.Equal(t, 1.0, score.RecallAtK)
	require.Empty(t, score.UnmappedSourceRefs)
	trace.RankedRefs = []Ref{{Type: "evidence", ID: "representative"}, {Type: "evidence", ID: "alternate"}}
	score = ScoreTrace("source", 2, QRel{RequiredRefs: []Ref{{Type: "evidence", SourceDocID: "source", Grade: 1}}}, trace, mapping)
	require.Equal(t, 1.0, score.RecallAtK)
	require.Equal(t, 0.5, score.MRR)
	require.InDelta(t, 0.6309297536, score.NDCGAtK, 1e-9)
	require.Equal(t, 2, score.FirstRequiredRank)
}
