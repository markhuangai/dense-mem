package evalharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	remember "github.com/markhuangai/dense-mem/internal/remember/service"
)

type RecallAccuracyLock struct {
	SchemaVersion   string            `json:"schema_version"`
	SeedID          string            `json:"seed_id"`
	CaseCount       int               `json:"case_count"`
	SourceCount     int               `json:"source_count"`
	SeedHash        string            `json:"seed_hash"`
	CohortHash      string            `json:"cohort_sha256"`
	JudgmentHash    string            `json:"judgment_sha256"`
	ArtifactHashes  map[string]string `json:"artifact_sha256"`
	GeneratorHashes map[string]string `json:"generator_sha256,omitempty"`
}

func GenerateRecallAccuracy(outputDir string) (RecallAccuracyLock, error) {
	var lock RecallAccuracyLock
	if strings.TrimSpace(outputDir) == "" {
		return lock, fmt.Errorf("output directory is required")
	}
	cohort := RecallAccuracyCohort()
	if err := validateRecallAccuracy(cohort); err != nil {
		return lock, err
	}
	var corpus []CorpusItem
	var cases []Case
	var qrels []QRel
	var suite []SuiteCase
	var judgments []RecallAccuracyJudgment
	for _, item := range cohort {
		cases, qrels = append(cases, item.Case), append(qrels, item.QRel)
		suite = append(suite, SuiteCase{CaseID: item.Case.CaseID, Weight: 1, Slices: item.Case.Slices})
		judgments = append(judgments, item.Judgments...)
		for _, source := range item.Sources {
			corpus = append(corpus, source.CorpusItem())
		}
	}
	manifest := SeedManifest{SchemaVersion: SeedSchemaVersionV2, SeedID: RecallAccuracySeedID,
		Description: "Frozen synthetic Recall relevance judgments; PostgreSQL baselines use synthetic embeddings, not model-quality evidence.",
		CorpusFile:  "corpus.jsonl", CasesFile: "cases.jsonl", QrelsFile: "qrels.jsonl",
		Counts:  map[string]int{"corpus": len(corpus), "cases": len(cases), "qrels": len(qrels)},
		Sources: []SeedSource{{Name: "Manual synthetic recall accuracy cases", License: "Repository license", Notes: "Judgments and provenance never enter Remember inputs."}}}
	if err := validateV2CorpusRelationships(&manifest, corpus); err != nil {
		return lock, err
	}
	for _, item := range corpus {
		if _, err := remember.ScanSubmissionWithProviderProposal([]string{item.Content}, map[string]any{"relationships": item.Relationships}); err != nil {
			return lock, fmt.Errorf("production admission rejected %s: %w", item.SourceDocID, err)
		}
	}
	artifacts := map[string][]byte{}
	for name, value := range map[string]any{"seed_manifest.json": manifest, "cohort.json": cohort} {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return lock, err
		}
		artifacts[name] = append(data, '\n')
	}
	for name, rows := range map[string]any{"corpus.jsonl": corpus, "cases.jsonl": cases, "qrels.jsonl": qrels, "suite.jsonl": suite, "judgments.jsonl": judgments} {
		encoded, err := json.Marshal(rows)
		if err != nil {
			return lock, err
		}
		var entries []json.RawMessage
		if err = json.Unmarshal(encoded, &entries); err != nil {
			return lock, err
		}
		var content bytes.Buffer
		for _, entry := range entries {
			content.Write(entry)
			content.WriteByte('\n')
		}
		artifacts[name] = content.Bytes()
	}
	if err := os.MkdirAll(filepath.Dir(outputDir), 0o755); err != nil {
		return lock, err
	}
	if err := os.Mkdir(outputDir, 0o755); err != nil {
		return lock, fmt.Errorf("create cohort output (must not already exist): %w", err)
	}
	for name, data := range artifacts {
		file, err := os.OpenFile(filepath.Join(outputDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return lock, err
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			return lock, writeErr
		}
		if closeErr != nil {
			return lock, closeErr
		}
	}
	manifestPath := filepath.Join(outputDir, "seed_manifest.json")
	seedHash, err := SeedHash(manifestPath, &manifest)
	if err != nil {
		return lock, err
	}
	if err = validateRunInputs(manifestPath, &manifest, corpus, cases, qrels, nil, suite, seedHash); err != nil {
		return lock, err
	}
	lock = RecallAccuracyLock{SchemaVersion: "dense-mem.eval.recall_accuracy.v1", SeedID: RecallAccuracySeedID,
		CaseCount: len(cases), SourceCount: len(corpus), SeedHash: seedHash, ArtifactHashes: map[string]string{}}
	lock.CohortHash, err = canonicalJSONHash(cohort)
	if err != nil {
		return lock, err
	}
	for name := range artifacts {
		lock.ArtifactHashes[name], err = sha256File(filepath.Join(outputDir, name))
		if err != nil {
			return lock, err
		}
	}
	lock.JudgmentHash = lock.ArtifactHashes["judgments.jsonl"]
	return lock, writeJSONFile(filepath.Join(outputDir, "source_lock.json"), lock)
}

func validateRecallAccuracy(cohort []RecallAccuracyCase) error {
	if len(cohort) != 24 {
		return fmt.Errorf("recall accuracy requires exactly 24 cases")
	}
	families := map[string]int{"generic_predicates": 0, "ambiguous_names": 0, "graph_hubs": 0, "synonyms": 0, "polarity_time": 0, "multihop": 0}
	sourceIDs, caseIDs := map[string]bool{}, map[string]bool{}
	for _, item := range cohort {
		if caseIDs[item.Case.CaseID] || item.QRel.CaseID != item.Case.CaseID || len(item.Case.Slices) == 0 {
			return fmt.Errorf("invalid or duplicate recall accuracy case %s", item.Case.CaseID)
		}
		caseIDs[item.Case.CaseID] = true
		families[item.Case.Slices[0]]++
		sources := map[string]RecallAccuracySource{}
		for _, source := range item.Sources {
			if source.ID == "" || sourceIDs[source.ID] || source.Text == "" || utf8.RuneCountInString(source.Text) > MaxCorpusContentCodepoints {
				return fmt.Errorf("invalid or duplicate recall accuracy source %s", source.ID)
			}
			sources[source.ID], sourceIDs[source.ID] = source, true
		}
		labels := map[string]bool{}
		if len(item.QRel.RequiredRefs) == 0 || len(item.QRel.BadRefs) == 0 {
			return fmt.Errorf("case %s needs positive and negative judgments", item.Case.CaseID)
		}
		for index, refs := range [][]Ref{item.QRel.RequiredRefs, item.QRel.BadRefs} {
			for _, ref := range refs {
				if _, exists := labels[ref.SourceDocID]; exists || ref.Type != "evidence" || sources[ref.SourceDocID].ID == "" || ref.Reason == "" {
					return fmt.Errorf("duplicate, overlapping, or unmapped judgment in %s", item.Case.CaseID)
				}
				labels[ref.SourceDocID] = index == 0
			}
		}
		if len(labels) >= len(sources) || len(labels) != len(item.Judgments) {
			return fmt.Errorf("case %s needs provenance and unjudged distractors", item.Case.CaseID)
		}
		seen := map[string]bool{}
		for _, judgment := range item.Judgments {
			source := sources[judgment.SourceDocID]
			label, exists := labels[judgment.SourceDocID]
			if !exists || seen[source.ID] || label != judgment.Relevant || judgment.CaseID != item.Case.CaseID || judgment.Reason == "" || judgment.SpanStart != 0 || judgment.SpanEnd != utf8.RuneCountInString(source.Text) || judgment.Quote != source.Text {
				return fmt.Errorf("invalid judgment provenance in %s", item.Case.CaseID)
			}
			seen[source.ID] = true
		}
		for _, id := range item.Concept.SourceIDs {
			if sources[id].ID == "" {
				return fmt.Errorf("unmapped ontology source in %s", item.Case.CaseID)
			}
		}
	}
	if len(families) != 6 {
		return fmt.Errorf("recall accuracy has an unknown family")
	}
	for family, count := range families {
		if count != 4 {
			return fmt.Errorf("recall accuracy family %s has %d cases, want 4", family, count)
		}
	}
	return nil
}
