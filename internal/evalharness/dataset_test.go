package evalharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	require.Equal(t, "hotpotqa:row:0", corpus[0].SourceDocID)
	require.Equal(t, "hotpotqa:0", corpus[1].SourceDocID)
	require.Equal(t, "hotpotqa:row:2", corpus[2].SourceDocID)
	require.Equal(t, []string{corpus[0].SourceDocID}, questions[0].GoldSources)
	require.Equal(t, []string{corpus[1].SourceDocID}, questions[1].GoldSources)
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

func TestDatasetLocksAndAdmissionRejectIncompleteSources(t *testing.T) {
	original, err := ReadRegistry("../../tests/eval/fixtures/datasets.json")
	require.NoError(t, err)
	for name, change := range map[string]func(*Registry){
		"wrong revision":    func(r *Registry) { r.Revision = "unpinned" },
		"duplicate dataset": func(r *Registry) { r.Datasets = append(r.Datasets, r.Datasets[0]) },
		"empty identity":    func(r *Registry) { r.Datasets[0].ID = "" },
		"wrong adapter":     func(r *Registry) { r.Datasets[0].Adapter = "qasper" },
		"path traversal":    func(r *Registry) { r.Datasets[0].Corpus.Path = "../corpus.json" },
		"invalid checksum":  func(r *Registry) { r.Datasets[0].Corpus.SHA256 = strings.Repeat("z", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(original)
			require.NoError(t, err)
			var changed Registry
			require.NoError(t, json.Unmarshal(body, &changed))
			change(&changed)
			body, err = json.Marshal(changed)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "registry.json")
			require.NoError(t, os.WriteFile(path, body, 0o600))
			_, err = ReadRegistry(path)
			require.Error(t, err)
		})
	}
	const corpus = `[{"idx":0,"title":"Title","text":"first."}]`
	const questions = `[{"question":"Which storage?","answer":"PostgreSQL","supporting_facts":[["Title",0]],"context":[["Title",["first."]]]}]`
	for _, item := range []struct {
		name, corpus, questions  string
		corpusRows, questionRows int
	}{
		{"corpus syntax", "[", questions, 1, 1},
		{"question syntax", corpus, "[", 1, 1},
		{"corpus denominator", corpus, questions, 2, 1},
		{"question denominator", corpus, questions, 1, 2},
		{"fractional identity", strings.Replace(corpus, `"idx":0`, `"idx":0.5`, 1), questions, 1, 1},
		{"null identity", strings.Replace(corpus, `"idx":0`, `"idx":null`, 1), questions, 1, 1},
		{"string identity", strings.Replace(corpus, `"idx":0`, `"idx":"0"`, 1), questions, 1, 1},
		{"duplicate identity", `[{"idx":0,"title":"Title","text":"first."},{"idx":0,"title":"Title","text":"other."}]`, questions, 2, 1},
		{"empty source", strings.Replace(corpus, "first.", "", 1), questions, 1, 1},
		{"admission bound", strings.Replace(corpus, "first.", strings.Repeat("x", 16385), 1), questions, 1, 1},
		{"missing query", corpus, strings.Replace(questions, "Which storage?", "", 1), 1, 1},
		{"unsupported mapping", corpus, strings.Replace(questions, `"Title",0`, `"Missing",0`, 1), 1, 1},
	} {
		t.Run(item.name, func(t *testing.T) {
			directory := t.TempDir()
			dataset := Dataset{ID: "hotpotqa", Adapter: "hotpotqa", Corpus: SourceFile{Path: "corpus.json", Rows: item.corpusRows, SHA256: bytesHash([]byte(item.corpus))}, Questions: SourceFile{Path: "questions.json", Rows: item.questionRows, SHA256: bytesHash([]byte(item.questions))}}
			require.NoError(t, os.WriteFile(filepath.Join(directory, dataset.Corpus.Path), []byte(item.corpus), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(directory, dataset.Questions.Path), []byte(item.questions), 0o600))
			rows, questions, err := LoadDataset(dataset, directory)
			require.Error(t, err)
			require.Nil(t, rows)
			require.Nil(t, questions)
		})
	}
}

func TestPublicAdaptersRejectMalformedPublishedRecords(t *testing.T) {
	for _, item := range []struct {
		name, adapter, field string
		value                any
	}{
		{"query type", "hotpotqa", "question", 1},
		{"answer type", "hotpotqa", "answer", 1},
		{"support type", "hotpotqa", "supporting_facts", "wrong"},
		{"context type", "hotpotqa", "context", "wrong"},
		{"support shape", "hotpotqa", "supporting_facts", [][]any{{"Title"}}},
		{"support identity", "hotpotqa", "supporting_facts", [][]any{{1, 0}}},
		{"context shape", "hotpotqa", "context", [][]any{{"Title"}}},
		{"context identity", "hotpotqa", "context", [][]any{{1, []string{"first."}}}},
		{"context text", "hotpotqa", "context", [][]any{{"Title", 1}}},
		{"alias serialization", "popqa", "possible_answers", "invalid JSON"},
		{"alias type", "musique", "answer_aliases", 1},
		{"passage type", "musique", "paragraphs", 1},
		{"retired adapter", "qasper", "answer", "PostgreSQL"},
	} {
		t.Run(item.name, func(t *testing.T) {
			question := map[string]any{"question": "Which storage?", "answer": "PostgreSQL", "supporting_facts": [][]any{{"Title", 0}}, "context": [][]any{{"Title", []string{"first."}}}}
			question[item.field] = item.value
			_, err := adaptQuestion(item.adapter, rawQuestion(t, question), 0, map[string][]string{"Title\nfirst.": {"source"}})
			require.Error(t, err)
		})
	}
}
