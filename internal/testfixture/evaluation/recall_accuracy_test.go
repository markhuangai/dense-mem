package evaluation

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestRecallAccuracyRejectsIncompleteAndOverlappingJudgments(t *testing.T) {
	for name, mutate := range map[string]func([]RecallAccuracyCase) []RecallAccuracyCase{
		"missing case":     func(c []RecallAccuracyCase) []RecallAccuracyCase { return c[:23] },
		"family":           func(c []RecallAccuracyCase) []RecallAccuracyCase { c[0].Case.Slices[0] = "other"; return c },
		"missing negative": func(c []RecallAccuracyCase) []RecallAccuracyCase { c[0].QRel.BadRefs = nil; return c },
		"overlap": func(c []RecallAccuracyCase) []RecallAccuracyCase {
			c[0].QRel.BadRefs[0] = c[0].QRel.RequiredRefs[0]
			return c
		},
		"unmapped": func(c []RecallAccuracyCase) []RecallAccuracyCase {
			c[0].QRel.BadRefs[0].SourceDocID = "missing"
			return c
		},
		"wrong span":           func(c []RecallAccuracyCase) []RecallAccuracyCase { c[0].Judgments[0].SpanEnd--; return c },
		"wrong quote":          func(c []RecallAccuracyCase) []RecallAccuracyCase { c[0].Judgments[0].Quote = "unsupported"; return c },
		"duplicate provenance": func(c []RecallAccuracyCase) []RecallAccuracyCase { c[0].Judgments[1] = c[0].Judgments[0]; return c },
		"unjudged missing":     func(c []RecallAccuracyCase) []RecallAccuracyCase { c[0].Sources = c[0].Sources[:2]; return c },
		"duplicate case":       func(c []RecallAccuracyCase) []RecallAccuracyCase { c[1] = c[0]; return c },
		"ontology mapping":     func(c []RecallAccuracyCase) []RecallAccuracyCase { c[0].Concept.SourceIDs[0] = "missing"; return c },
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateRecallAccuracy(mutate(RecallAccuracyCohort())))
		})
	}
	require.NoError(t, validateRecallAccuracy(RecallAccuracyCohort()))
}

func TestRecallAccuracyOnlyExplicitNegativesCountAsBad(t *testing.T) {
	for _, c := range RecallAccuracyCohort() {
		t.Run(c.Case.CaseID, func(t *testing.T) {
			mapping := newKnowledgeMapping()
			for _, source := range c.Sources {
				addSourceMapping(&mapping, Ref{Type: "evidence", ID: source.ID, SourceDocID: source.ID}, true)
			}
			good, bad, unjudged := c.QRel.RequiredRefs[0].SourceDocID, c.QRel.BadRefs[0].SourceDocID, c.Sources[len(c.Sources)-1].ID
			trace := RecallTrace{CaseID: c.Case.CaseID, RankedRefs: []Ref{{Type: "evidence", ID: unjudged}, {Type: "fragment", ID: bad}, {Type: "evidence", ID: good}}}
			score := ScoreTrace(c.Case.CaseID, 10, c.QRel, trace, mapping)
			require.Empty(t, score.UnmappedSourceRefs)
			require.Equal(t, 1, score.BadAtK)
			require.Equal(t, 1, score.RelevantAtK)
			require.InDelta(t, 1.0/3, score.MRR, 1e-12)
			trace.RankedRefs = trace.RankedRefs[:1]
			require.Zero(t, ScoreTrace(c.Case.CaseID, 10, c.QRel, trace, mapping).BadAtK)
		})
	}
}

func TestRecallAccuracyGroupedAlternatesKeepOneRankedSlot(t *testing.T) {
	c := accuracySynonymCases()[0]
	mapping := newKnowledgeMapping()
	for _, source := range c.Sources {
		addSourceMapping(&mapping, Ref{Type: "evidence", ID: source.ID, SourceDocID: source.ID}, true)
	}
	trace := RecallTrace{CaseID: c.Case.CaseID, RankedRefs: []Ref{{Type: "fragment", ID: c.Sources[0].ID, EquivalentEvidenceIDs: []string{c.Sources[1].ID}}}}
	score := ScoreTrace(c.Case.CaseID, 10, c.QRel, trace, mapping)
	require.Empty(t, score.UnmappedSourceRefs)
	require.Equal(t, 1, score.K)
	require.Equal(t, 2, score.RelevantAtK)
	require.Equal(t, 1.0, score.RecallAtK)
	require.Zero(t, score.BadAtK)
	trace.RankedRefs[0].EquivalentEvidenceIDs = append(trace.RankedRefs[0].EquivalentEvidenceIDs, c.Sources[2].ID, c.Sources[3].ID)
	require.Equal(t, 1, ScoreTrace(c.Case.CaseID, 10, c.QRel, trace, mapping).BadAtK)
}

func TestRecallAccuracyExportRefusesExistingPaths(t *testing.T) {
	_, err := GenerateRecallAccuracy("")
	require.ErrorContains(t, err, "output directory is required")
	root := t.TempDir()
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, []byte("preserve"), 0o600))
	directory := filepath.Join(root, "directory")
	require.NoError(t, os.Mkdir(directory, 0o700))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(directory, link))
	for _, path := range []string{file, directory, link} {
		_, err := GenerateRecallAccuracy(path)
		require.ErrorContains(t, err, "already exist")
	}
	contents, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "preserve", string(contents))
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestRecallAccuracyGenerationMatchesFrozenLock(t *testing.T) {
	first, err := GenerateRecallAccuracy(filepath.Join(t.TempDir(), "first"))
	require.NoError(t, err)
	second, err := GenerateRecallAccuracy(filepath.Join(t.TempDir(), "second"))
	require.NoError(t, err)
	require.Equal(t, first, second)
	bytes, err := os.ReadFile("testdata/recall_accuracy_v1.json")
	require.NoError(t, err)
	var frozen RecallAccuracyLock
	require.NoError(t, json.Unmarshal(bytes, &frozen))
	frozen.GeneratorHashes = nil
	require.Equal(t, frozen, first)
}
