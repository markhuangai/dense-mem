package evalharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecallAccuracyGenerationMatchesFrozenLock(t *testing.T) {
	root := t.TempDir()
	first, err := GenerateRecallAccuracy(filepath.Join(root, "first"))
	require.NoError(t, err)
	second, err := GenerateRecallAccuracy(filepath.Join(root, "second"))
	require.NoError(t, err)
	require.Equal(t, first, second)
	for name := range first.ArtifactHashes {
		a, err := os.ReadFile(filepath.Join(root, "first", name))
		require.NoError(t, err)
		b, err := os.ReadFile(filepath.Join(root, "second", name))
		require.NoError(t, err)
		require.Equal(t, a, b, name)
	}
	var frozen RecallAccuracyLock
	require.NoError(t, readJSONFile("../../tests/eval/source_locks/recall_accuracy_v1.json", &frozen))
	require.NotEmpty(t, frozen.GeneratorHashes)
	for path, hash := range frozen.GeneratorHashes {
		actual, err := sha256File(filepath.Join("../..", path))
		require.NoError(t, err)
		require.Equal(t, hash, actual, path)
	}
	frozen.GeneratorHashes = nil
	require.Equal(t, frozen, first)
	require.Equal(t, 24, first.CaseCount)
	manifestPath := filepath.Join(root, "first", "seed_manifest.json")
	manifest, err := LoadSeedManifest(manifestPath)
	require.NoError(t, err)
	corpus, err := LoadCorpus(manifestPath, manifest)
	require.NoError(t, err)
	require.NoError(t, validateV2CorpusRelationships(manifest, corpus))
	for _, item := range corpus {
		input, err := json.Marshal(corpusRememberInput(item))
		require.NoError(t, err)
		for _, forbidden := range []string{"required_refs", "bad_refs", "judgments", "Direct support for the query", "span_start", "span_end", "\"relevant\""} {
			require.NotContains(t, string(input), forbidden)
		}
	}
}

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

func TestRecallAccuracyLeavesFrozenV3LocksUnchanged(t *testing.T) {
	for path, expected := range map[string]string{
		"public_6axis_1k_v2_cohort.json":  "sha256:3dacb2de37f9c01f839c34820b826c0608bbf350007fa3add80cc72bfe9b3ade",
		"public_6axis_1k_v3_repairs.json": "sha256:05aab781ef70a6842b752081f4ca07f8cc9a3a19af157f60516ea3d2b0333f54",
		"public_6axis_v1.json":            "sha256:0f35237574961b442e53ce7988fc1076a8ff8bce4647c648f75a91463b3c049a",
	} {
		hash, err := sha256File(filepath.Join("../../tests/eval/source_locks", path))
		require.NoError(t, err)
		require.Equal(t, expected, hash)
	}
}
