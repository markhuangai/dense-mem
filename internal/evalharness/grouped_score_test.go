package evalharness

import (
	"encoding/json"
	"fmt"
	"math"
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

func TestScoringEvidenceAliasesAgreeAcrossMappingsAndDiagnostics(t *testing.T) {
	var canonical RetrievalScore
	for _, mappingType := range []string{"fragment", "evidence", "mixed"} {
		for _, judgmentType := range []string{"fragment", "evidence"} {
			for _, resultType := range []string{"fragment", "evidence"} {
				t.Run(mappingType+"/"+judgmentType+"/"+resultType, func(t *testing.T) {
					mapping := newKnowledgeMapping()
					for _, id := range []string{"good", "bad"} {
						kind := mappingType
						if kind == "mixed" {
							kind = "fragment"
							addSourceMapping(&mapping, Ref{Type: "evidence", ID: id, SourceDocID: id}, false)
						}
						addSourceMapping(&mapping, Ref{Type: kind, ID: id, SourceDocID: id}, true)
					}
					required := []Ref{
						{Type: judgmentType, SourceDocID: "good", Grade: 1},
						{Type: "fragment", ID: "good", Grade: 3},
						{Type: "evidence", ID: "good", Grade: 2},
						{Type: judgmentType, ID: "missing", Grade: 2},
						{Type: "evidence", ID: "missing", Grade: 1},
					}
					bad := []Ref{{Type: judgmentType, SourceDocID: "bad"}, {Type: "fragment", ID: "bad"}, {Type: "evidence", ID: "bad"}}
					qrel := QRel{RequiredRefs: required, BadRefs: bad, RequiredEvidenceRefs: required, BadEvidenceRefs: bad}
					var output map[string]any
					require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"ranked_refs":[{"type":%q,"id":"good"},{"type":%q,"id":"bad"}]}`, resultType, resultType)), &output))
					output["context_refs"] = output["ranked_refs"]
					output["context_evidence_refs"] = output["ranked_refs"]
					trace := traceFromToolOutput(Case{CaseID: "aliases"}, output)
					before, err := json.Marshal([]any{mapping, qrel, trace})
					require.NoError(t, err)
					require.NoError(t, validateRequiredQRelMappings(map[string]QRel{"aliases": qrel}, []SuiteCase{{CaseID: "aliases"}}, mapping))
					score := ScoreTrace("aliases", 2, qrel, trace, mapping)
					require.Equal(t, 2, score.RelevantTotal)
					require.Equal(t, 1, score.RelevantAtK)
					require.Equal(t, 0.5, score.RecallAtK)
					require.Equal(t, 1.0, score.MRR)
					require.InDelta(t, 7/(7+3/math.Log2(3)), score.NDCGAtK, 1e-12)
					require.Equal(t, 1, score.FirstRequiredRank)
					require.Equal(t, 1, score.BadAtK)
					require.Equal(t, 2, score.FirstBadRank)
					require.Equal(t, []Ref{{Type: "evidence", ID: "missing", Grade: 2}}, score.MissingRequired)
					require.Equal(t, []Ref{{Type: "evidence", SourceDocID: "bad"}}, score.BadRefsAtK)
					require.Empty(t, score.UnmappedSourceRefs)
					require.True(t, score.ContextScored)
					require.True(t, score.EvidenceScored)
					require.Equal(t, score.RecallAtK, score.ContextRecallAtK)
					require.Equal(t, score.NDCGAtK, score.EvidenceNDCGAtK)
					require.Equal(t, score.MissingRequired, score.ContextMissingRequired)
					require.Equal(t, score.BadRefsAtK, score.EvidenceBadRefsAtK)
					if canonical.CaseID == "" {
						canonical = score
					} else {
						require.Equal(t, canonical, score)
					}
					after, err := json.Marshal([]any{mapping, qrel, trace})
					require.NoError(t, err)
					require.Equal(t, before, after)
				})
			}
		}
	}
}

func TestScoringAliasMappingsKeepDistinctIDsAmbiguous(t *testing.T) {
	for _, kind := range []string{"fragment", "evidence"} {
		t.Run(kind, func(t *testing.T) {
			mapping := KnowledgeMapping{
				BySourceDocID: map[string]Ref{"source": {Type: "fragment", ID: "one"}},
				BySourceDocIDAndType: map[string]map[string][]Ref{"source": {
					"fragment": {{Type: "fragment", ID: "one"}, {Type: "fragment", ID: "one"}},
					"evidence": {{Type: "evidence", ID: "one"}},
				}},
			}
			qrel := QRel{RequiredRefs: []Ref{{Type: kind, SourceDocID: "source"}, {Type: "fragment", SourceDocID: "source"}}}
			trace := RecallTrace{RankedRefs: []Ref{{Type: "evidence", ID: "one"}}}
			suite := []SuiteCase{{CaseID: "aliases"}}
			require.NoError(t, validateRequiredQRelMappings(map[string]QRel{"aliases": qrel}, suite, mapping))
			require.Equal(t, 1.0, ScoreTrace("aliases", 1, qrel, trace, mapping).RecallAtK)
			mapping.BySourceDocIDAndType["source"]["evidence"] = append(mapping.BySourceDocIDAndType["source"]["evidence"], Ref{Type: "evidence", ID: "two"})
			require.ErrorContains(t, validateRequiredQRelMappings(map[string]QRel{"aliases": qrel}, suite, mapping), "unmapped")
			score := ScoreTrace("aliases", 1, qrel, trace, mapping)
			require.Zero(t, score.RecallAtK)
			require.Equal(t, 1, score.RelevantTotal)
			require.Equal(t, []Ref{{Type: "evidence", SourceDocID: "source"}}, score.MissingRequired)
			require.Equal(t, score.MissingRequired, score.UnmappedSourceRefs)
		})
	}
}

func TestScoringAliasesDeduplicateSourceDocumentDiagnostics(t *testing.T) {
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"first":  {Type: "evidence", ID: "shared"},
		"second": {Type: "fragment", ID: "shared"},
	}}
	required := []Ref{{Type: "source_doc", SourceDocID: "first", Grade: 1}, {Type: "source_doc", SourceDocID: "second", Grade: 3}}
	qrel := QRel{RequiredRefs: required, RequiredEvidenceRefs: required, BadRefs: required}
	score := ScoreTrace("duplicate sources", 1, qrel, RecallTrace{ContextEvidenceRefs: []Ref{}}, mapping)
	require.Equal(t, 1, score.RelevantTotal)
	require.Equal(t, []Ref{{Type: "source_doc", SourceDocID: "first", Grade: 3}}, score.MissingRequired)
	require.Equal(t, score.MissingRequired, score.EvidenceMissingRequired)
	returned := ScoreTrace("duplicate sources", 1, qrel, RecallTrace{RankedRefs: []Ref{{Type: "evidence", ID: "shared"}}}, mapping)
	require.Equal(t, 1.0, returned.RecallAtK)
	require.Equal(t, 1, returned.BadAtK)
	require.Equal(t, score.MissingRequired, returned.BadRefsAtK)
}

func TestGroupedEvidenceAliasesPreserveSlotsAndDisplayedSources(t *testing.T) {
	for _, kind := range []string{"fragment", "evidence"} {
		t.Run(kind, func(t *testing.T) {
			alternates := make([]string, 20)
			required := []Ref{{Type: "fragment", ID: "representative", Grade: 3}}
			for i := range alternates {
				alternates[i] = fmt.Sprintf("alternate-%02d", i)
				required = append(required, Ref{Type: "fragment", ID: alternates[i], Grade: 1}, Ref{Type: "evidence", ID: alternates[i], Grade: 2})
			}
			required = append(required, Ref{Type: "fragment", ID: "undisplayed", Grade: 1})
			body, err := json.Marshal(map[string]any{"ranked_refs": []map[string]any{
				{"type": kind, "id": "representative", "rank": 1, "equivalent_evidence_ids": alternates, "equivalents_truncated": true},
				{"type": kind, "id": "later", "rank": 2},
			}})
			require.NoError(t, err)
			var output map[string]any
			require.NoError(t, json.Unmarshal(body, &output))
			output["context_refs"] = output["ranked_refs"]
			output["context_evidence_refs"] = output["ranked_refs"]
			trace := traceFromToolOutput(Case{CaseID: "grouped"}, output)
			bad := []Ref{{Type: "fragment", ID: alternates[19]}, {Type: "evidence", ID: alternates[19]}, {Type: "fragment", ID: "undisplayed"}}
			qrel := QRel{RequiredRefs: required, BadRefs: bad, RequiredEvidenceRefs: required, BadEvidenceRefs: bad}
			score := ScoreTrace("grouped", 1, qrel, trace, newKnowledgeMapping())
			require.Equal(t, 1, score.K)
			require.Equal(t, 21, score.RelevantAtK)
			require.Equal(t, 22, score.RelevantTotal)
			require.Equal(t, 21.0/22, score.RecallAtK)
			require.Equal(t, 1.0, score.MRR)
			require.Equal(t, 1.0, score.NDCGAtK)
			require.Equal(t, []Ref{{Type: "evidence", ID: "undisplayed", Grade: 1}}, score.MissingRequired)
			require.Equal(t, 1, score.BadAtK)
			require.Equal(t, 1, score.FirstBadRank)
			require.Equal(t, []Ref{{Type: "evidence", ID: alternates[19]}}, score.BadRefsAtK)
			require.Equal(t, score.RecallAtK, score.ContextRecallAtK)
			require.Equal(t, score.BadRefsAtK, score.EvidenceBadRefsAtK)
			later := QRel{RequiredRefs: []Ref{{Type: "fragment", ID: "later"}}}
			require.Zero(t, ScoreTrace("later", 1, later, trace, newKnowledgeMapping()).RecallAtK)
			laterScore := ScoreTrace("later", 2, later, trace, newKnowledgeMapping())
			require.Equal(t, 2, laterScore.FirstRequiredRank)
			require.Equal(t, 0.5, laterScore.MRR)
			require.Len(t, trace.RankedRefs, 2)
			require.Equal(t, kind, trace.RankedRefs[0].Type)
			require.Equal(t, alternates, trace.RankedRefs[0].EquivalentEvidenceIDs)
		})
	}
}

func TestScoringEvidenceAliasesPreserveContextAvailability(t *testing.T) {
	for _, kind := range []string{"fragment", "evidence"} {
		for _, tc := range []struct {
			name      string
			context   []Ref
			evidence  []Ref
			available bool
			recall    float64
		}{
			{name: "absent"},
			{name: "empty context", context: []Ref{}, available: true},
			{name: "context fallback", context: []Ref{{Type: kind, ID: "good"}}, available: true, recall: 1},
			{name: "explicit empty evidence", context: []Ref{{Type: kind, ID: "good"}}, evidence: []Ref{}, available: true},
			{name: "explicit evidence", evidence: []Ref{{Type: kind, ID: "good"}}, available: true, recall: 1},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				qrel := QRel{RequiredEvidenceRefs: []Ref{{Type: "fragment", ID: "good"}, {Type: "evidence", ID: "good"}}}
				trace := RecallTrace{ContextRefs: tc.context, ContextEvidenceRefs: tc.evidence}
				score := ScoreTrace("context", 1, qrel, trace, newKnowledgeMapping())
				require.Equal(t, tc.available, score.EvidenceScored)
				require.Equal(t, tc.recall, score.EvidenceRecallAtK)
				require.Equal(t, tc.context == nil, trace.ContextRefs == nil)
				require.Equal(t, tc.evidence == nil, trace.ContextEvidenceRefs == nil)
				if len(tc.context) > 0 {
					require.Equal(t, kind, trace.ContextRefs[0].Type)
				}
				if len(tc.evidence) > 0 {
					require.Equal(t, kind, trace.ContextEvidenceRefs[0].Type)
				}
			})
		}
	}
}

func TestScoringEvidenceAliasesDoNotMergeOtherKinds(t *testing.T) {
	for _, kind := range []string{"entity", "value", "relationship", "fact", "dream"} {
		t.Run(kind, func(t *testing.T) {
			qrel := QRel{
				RequiredRefs: []Ref{{Type: "fragment", ID: "same"}, {Type: "evidence", ID: "same"}, {Type: kind, ID: "same"}},
				BadRefs:      []Ref{{Type: kind, ID: "same"}},
			}
			trace := RecallTrace{RankedRefs: []Ref{{Type: "evidence", ID: "same", EquivalentEvidenceIDs: []string{"alternate"}}}}
			score := ScoreTrace("kinds", 1, qrel, trace, newKnowledgeMapping())
			require.Equal(t, 2, score.RelevantTotal)
			require.Equal(t, 1, score.RelevantAtK)
			require.Equal(t, 0.5, score.RecallAtK)
			require.Zero(t, score.BadAtK)
			require.Equal(t, []Ref{{Type: kind, ID: "same"}}, score.MissingRequired)
			trace.RankedRefs[0].Type = kind
			qrel.RequiredRefs = []Ref{{Type: "fragment", ID: "same"}, {Type: "evidence", ID: "alternate"}}
			qrel.BadRefs = []Ref{{Type: "evidence", ID: "same"}}
			score = ScoreTrace("kinds", 1, qrel, trace, newKnowledgeMapping())
			require.Zero(t, score.RelevantAtK)
			require.Zero(t, score.BadAtK)
		})
	}
}
