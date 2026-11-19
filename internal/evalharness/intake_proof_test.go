package evalharness

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntakeProofFreezesCompletePacksAndRejectsDrift(t *testing.T) {
	directory := t.TempDir()
	registryPath := filepath.Join(directory, "datasets.json")
	require.ErrorIs(t, FreezeIntakeProofInputs(registryPath, directory), os.ErrNotExist)
	require.ErrorContains(t, FreezeIntakeProofInputs("../../tests/eval/fixtures/datasets.json", directory), "all five registered datasets")

	registry := writeIntakeProofFixture(t, directory, registryPath)
	require.NoError(t, FreezeIntakeProofInputs(registryPath, directory))
	frozenPath := filepath.Join(directory, "intake-proof-inputs.json")
	var frozen intakeProofInputs
	require.NoError(t, readJSON(frozenPath, &frozen))
	require.Len(t, frozen.Datasets, 5)
	require.Equal(t, registry.Revision, frozen.Revision)
	for _, dataset := range registry.Datasets {
		corpus, _, err := LoadDataset(dataset, directory)
		require.NoError(t, err)
		selected, err := ReadIntakeProofCorpus(Manifest{DataDir: directory, RegistryHash: frozen.Registry}, registry.Revision, dataset, corpus)
		require.NoError(t, err)
		require.Len(t, selected, 20)
		require.Len(t, frozen.Datasets[dataset.ID], 20)
	}

	dataset, err := registry.Dataset("hotpotqa")
	require.NoError(t, err)
	corpus, _, err := LoadDataset(dataset, directory)
	require.NoError(t, err)
	selected, _, err := selectIntakeProofCorpus(dataset, corpus)
	require.NoError(t, err)
	prior := []string{"hotpotqa:1338", "hotpotqa:3430", "hotpotqa:9465", "hotpotqa:9797", "hotpotqa:9798"}
	for i, id := range prior {
		require.Equal(t, id, selected[i].SourceDocID)
	}
	ranked := make([]string, 0, len(corpus)-len(prior))
	for _, item := range corpus {
		if !slices.Contains(prior, item.SourceDocID) {
			ranked = append(ranked, bytesHash([]byte(item.SourceDocID))+" "+item.SourceDocID)
		}
	}
	slices.Sort(ranked)
	for i, item := range selected[len(prior):] {
		_, id, _ := strings.Cut(ranked[i], " ")
		require.Equal(t, id, item.SourceDocID)
	}

	manifest := Manifest{DataDir: directory, RegistryHash: frozen.Registry}
	_, err = ReadIntakeProofCorpus(manifest, "unverified", dataset, corpus)
	require.ErrorContains(t, err, "source locks differ")
	_, err = ReadIntakeProofCorpus(manifest, registry.Revision, dataset, corpus[:19])
	require.ErrorContains(t, err, "complete registered corpus")
	index := slices.IndexFunc(corpus, func(row CorpusItem) bool { return row.SourceDocID == prior[0] })
	require.NotEqual(t, -1, index)
	_, err = ReadIntakeProofCorpus(manifest, registry.Revision, dataset, slices.Delete(slices.Clone(corpus), index, index+1))
	require.ErrorContains(t, err, "previously rejected row is missing")
	frozen.Datasets[dataset.ID][0].SHA256 = strings.Repeat("0", 64)
	require.NoError(t, writeJSON(frozenPath, frozen))
	_, err = ReadIntakeProofCorpus(manifest, registry.Revision, dataset, corpus)
	require.ErrorContains(t, err, "row differs from frozen input")

	manifest.DataDir = t.TempDir()
	_, err = ReadIntakeProofCorpus(manifest, registry.Revision, dataset, corpus)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorIs(t, FreezeIntakeProofInputs(registryPath, manifest.DataDir), os.ErrNotExist)
}

func writeIntakeProofFixture(t *testing.T, directory, registryPath string) Registry {
	t.Helper()
	registry, err := ReadRegistry("../../tests/eval/datasets.json")
	require.NoError(t, err)
	for i, dataset := range registry.Datasets {
		rowCount := 24
		var numericIDs []int
		for _, sourceID := range priorIntakeRows[dataset.ID] {
			value, positional := strings.CutPrefix(sourceID, "row:")
			index, err := strconv.Atoi(value)
			require.NoError(t, err)
			if positional {
				rowCount = max(rowCount, index+1)
			} else {
				numericIDs = append(numericIDs, index)
			}
		}
		type document struct {
			Index int    `json:"idx,omitempty"`
			Title string `json:"title"`
			Text  string `json:"text"`
		}
		rows := make([]document, rowCount)
		for index := range rows {
			rows[index] = document{Title: "Fixture " + strconv.Itoa(index), Text: "Exact synthetic evidence."}
		}
		for index, id := range numericIDs {
			rows[index].Index = id
		}
		question := map[string]any{"question": "What is the first fixture?", "answer": "Fixture 0"}
		passage := map[string]any{"title": rows[0].Title, "text": rows[0].Text, "paragraph_text": rows[0].Text, "is_supporting": true}
		switch dataset.ID {
		case "hotpotqa", "2wikimultihopqa":
			question["supporting_facts"] = [][]any{{rows[0].Title, 0}}
			question["context"] = [][]any{{rows[0].Title, []string{rows[0].Text}}}
		case "nq_rear":
			question["reference"] = []string{"Fixture 0"}
			question["contexts"] = []map[string]any{passage}
		default:
			question["possible_answers"] = []string{"Fixture 0"}
			question["paragraphs"] = []map[string]any{passage}
		}
		for _, source := range []struct {
			file  *SourceFile
			rows  int
			value any
		}{{&dataset.Corpus, len(rows), rows}, {&dataset.Questions, 1, []map[string]any{question}}} {
			filename := filepath.Join(directory, source.file.Path)
			require.NoError(t, writeJSON(filename, source.value))
			hash, err := FileHash(filename)
			require.NoError(t, err)
			source.file.SHA256, source.file.Rows = hash, source.rows
		}
		registry.Datasets[i] = dataset
	}
	require.NoError(t, writeJSON(registryPath, registry))
	return registry
}
