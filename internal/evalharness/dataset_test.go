package evalharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func rawQuestion(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	return raw
}

func TestPublicAdaptersPreservePublishedGoldJoinsAndAliases(t *testing.T) {
	for _, adapter := range []string{"hotpotqa", "2wikimultihopqa", "musique", "nq_rear", "popqa"} {
		t.Run(adapter, func(t *testing.T) {
			question := map[string]any{"question": "Which storage?", "answer": "PostgreSQL"}
			text := "first.second."
			switch adapter {
			case "hotpotqa", "2wikimultihopqa":
				question["supporting_facts"] = [][]any{{"Title", 0}, {"Title", 1}}
				question["context"] = [][]any{{"Title", []string{"first.", "second."}}}
				if adapter == "2wikimultihopqa" {
					text = "first. second."
				}
			case "musique":
				question["paragraphs"] = []map[string]any{{"title": "Title", "paragraph_text": text, "is_supporting": true}}
				question["answer_aliases"] = []string{"Postgres"}
			case "nq_rear":
				question["reference"] = []string{"PostgreSQL", "Postgres"}
				question["contexts"] = []map[string]any{{"title": "Title", "text": text, "is_supporting": true}}
			case "popqa":
				question["possible_answers"] = `["PostgreSQL","Postgres"]`
				question["paragraphs"] = []map[string]any{{"title": "Title", "text": text, "is_supporting": true}}
			}
			raw := rawQuestion(t, question)
			mapping := map[string][]string{"Title\n" + text: {"source"}}
			actual, err := adaptQuestion(adapter, raw, 0, mapping)
			require.NoError(t, err)
			require.Equal(t, []string{"source"}, actual.GoldSources)
			require.Contains(t, actual.Answers, "PostgreSQL")
			if adapter == "musique" || adapter == "nq_rear" || adapter == "popqa" {
				require.Contains(t, actual.Answers, "Postgres")
			}
			mapping["Title\n"+text] = []string{"source", "ambiguous"}
			_, err = adaptQuestion(adapter, raw, 0, mapping)
			require.ErrorContains(t, err, "ambiguous")
			_, err = adaptQuestion(adapter, raw, 0, map[string][]string{})
			require.ErrorContains(t, err, "missing")
		})
	}
}

func TestFixturePackValidatesWholeSourceAndRejectsTampering(t *testing.T) {
	registry, err := ReadRegistry("../../tests/eval/fixtures/datasets.json")
	require.NoError(t, err)
	dataset, err := registry.Dataset("hotpotqa")
	require.NoError(t, err)
	corpus, questions, err := LoadDataset(dataset, "../../tests/eval/fixtures")
	require.NoError(t, err)
	require.Len(t, corpus, 3)
	require.Len(t, questions, 2)
	directory := t.TempDir()
	for _, source := range []SourceFile{dataset.Corpus, dataset.Questions} {
		data, err := os.ReadFile(filepath.Join("../../tests/eval/fixtures", source.Path))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, source.Path), data, 0600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(directory, dataset.Corpus.Path), []byte("[]"), 0600))
	_, _, err = LoadDataset(dataset, directory)
	require.ErrorContains(t, err, "digest")
	_, err = registry.Dataset("qasper")
	require.Error(t, err)
}
