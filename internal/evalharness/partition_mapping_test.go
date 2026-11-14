package evalharness

import (
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRememberPartitionFallbackContextEvidenceScoring(t *testing.T) {
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "first", SourceDocID: "source"}, true)
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "second", SourceDocID: "source"}, false)
	mapping.Partitions = map[string][]ImportedPartition{"source": {{EvidenceID: "first"}, {EvidenceID: "second"}}}
	score := ScoreTrace("fallback", 2, QRel{RequiredEvidenceRefs: []Ref{{Type: "evidence", SourceDocID: "source", Grade: 3}}}, RecallTrace{ContextRefs: []Ref{{Type: "evidence", ID: "second"}}}, mapping)
	require.True(t, score.EvidenceScored)
	require.Equal(t, 1.0, score.EvidenceRecallAtK)
	require.Empty(t, score.EvidenceMissingRequired)
}

func TestRememberPartitionImportRetainsEverySliceAndSourceGroup(t *testing.T) {
	item := CorpusItem{SourceDocID: "source", Content: "First.\n第二。"}
	first := "First.\n"
	boundary := len([]rune(first))
	entries := []any{
		map[string]any{"evidence_id": "first", "disposition": "stored", "evidence_index": 0, "partition_index": 0, "source_item_id": "original", "occurrence_id": "occurrence-one", "span_start": 0, "span_end": boundary, "content_hash": knowledge.EvidenceContentHash(first)},
		map[string]any{"evidence_id": "second", "disposition": "stored", "evidence_index": 0, "partition_index": 1, "source_item_id": "original", "occurrence_id": "occurrence-two", "span_start": boundary, "span_end": len([]rune(item.Content)), "content_hash": knowledge.EvidenceContentHash("第二。")},
	}
	output := map[string]any{"processing_state": "completed", "evidence": entries}
	mapping, err := (&HTTPClient{}).importTerminalRememberResult(item, output, newKnowledgeMapping())
	require.NoError(t, err)
	require.Len(t, mapping.BySourceDocIDAndType["source"]["fragment"], 2)
	require.Equal(t, "first", mapping.BySourceDocID["source"].ID)
	require.Len(t, mapping.Partitions["source"], 2)
	require.Equal(t, "occurrence-two", mapping.Partitions["source"][1].OccurrenceID)
	for _, kind := range []string{"source_doc", "fragment", "evidence"} {
		qrel := QRel{RequiredRefs: []Ref{{Type: kind, SourceDocID: "source", Grade: 3}}}
		score := ScoreTrace("tail", 2, qrel, RecallTrace{RankedRefs: []Ref{{Type: "evidence", ID: "second"}}}, mapping)
		require.Equal(t, 1.0, score.RecallAtK, kind)
		require.Equal(t, 1.0, score.NDCGAtK, kind)
		require.Empty(t, score.UnmappedSourceRefs)
		score = ScoreTrace("duplicate slices", 2, qrel, RecallTrace{RankedRefs: []Ref{{Type: "evidence", ID: "second"}, {Type: "evidence", ID: "first"}}}, mapping)
		require.Equal(t, 1, score.RelevantAtK)
		require.Equal(t, 1.0, score.NDCGAtK)
		score = ScoreTrace("semantic representative", 1, qrel, RecallTrace{RankedRefs: []Ref{{Type: "evidence", ID: "representative", EquivalentEvidenceIDs: []string{"second"}}}}, mapping)
		require.Equal(t, 1.0, score.RecallAtK)
	}
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "other", SourceDocID: "other-source"}, true)
	qrel := QRel{RequiredRefs: []Ref{{Type: "source_doc", SourceDocID: "source", Grade: 3}, {Type: "source_doc", SourceDocID: "other-source", Grade: 1}}}
	score := ScoreTrace("rank slots", 2, qrel, RecallTrace{RankedRefs: []Ref{{Type: "evidence", ID: "second"}, {Type: "evidence", ID: "first"}, {Type: "evidence", ID: "other"}}}, mapping)
	require.Equal(t, 0.5, score.RecallAtK)
	require.Len(t, score.MissingRequired, 1)
	require.Equal(t, "other-source", score.MissingRequired[0].SourceDocID)
	filtered := newKnowledgeMapping()
	mergeFilteredKnowledgeMapping(&filtered, mapping, map[string]struct{}{"other-source": {}})
	require.Empty(t, filtered.BySourceDocIDAndType["source"])
	require.Equal(t, mapping.Partitions["source"], filtered.Partitions["source"], "partition captures must include unjudged corpus sources")
	entries[1].(map[string]any)["span_end"] = boundary
	unchanged := newKnowledgeMapping()
	_, err = (&HTTPClient{}).importTerminalRememberResult(item, output, unchanged)
	require.Error(t, err)
	require.Empty(t, unchanged.BySourceDocID)
}
