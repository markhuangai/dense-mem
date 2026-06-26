package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

type sliceSpec struct {
	name  string
	count int
}

var prSlices = []sliceSpec{
	{name: "single_active_fact", count: 30},
	{name: "fragment_evidence", count: 20},
	{name: "validated_claim", count: 15},
	{name: "multi_hop_graph", count: 25},
	{name: "community_global", count: 20},
	{name: "temporal_stale", count: 20},
	{name: "conflict_clarification", count: 15},
	{name: "hard_negatives", count: 20},
	{name: "no_answer_abstain", count: 15},
	{name: "security_scope", count: 15},
	{name: "long_context_noisy_team", count: 5},
}

func main() {
	outDir := flag.String("out", "tests/eval/seeds/local_pr_v1", "seed output directory")
	suitePath := flag.String("suite", "tests/eval/suites/pr.jsonl", "suite JSONL output path")
	docsPerCase := flag.Int("docs-per-case", 10, "corpus rows per case")
	flag.Parse()

	if *docsPerCase < 1 {
		exitf("docs-per-case must be at least 1")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		exitf("create seed dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*suitePath), 0o755); err != nil {
		exitf("create suite dir: %v", err)
	}

	corpusFile, err := os.Create(filepath.Join(*outDir, "corpus.jsonl"))
	if err != nil {
		exitf("create corpus: %v", err)
	}
	defer corpusFile.Close()
	casesFile, err := os.Create(filepath.Join(*outDir, "cases.jsonl"))
	if err != nil {
		exitf("create cases: %v", err)
	}
	defer casesFile.Close()
	qrelsFile, err := os.Create(filepath.Join(*outDir, "qrels.jsonl"))
	if err != nil {
		exitf("create qrels: %v", err)
	}
	defer qrelsFile.Close()
	answersFile, err := os.Create(filepath.Join(*outDir, "answers.jsonl"))
	if err != nil {
		exitf("create answers: %v", err)
	}
	defer answersFile.Close()
	negativesFile, err := os.Create(filepath.Join(*outDir, "hard_negatives.jsonl"))
	if err != nil {
		exitf("create hard negatives: %v", err)
	}
	defer negativesFile.Close()
	transformsFile, err := os.Create(filepath.Join(*outDir, "transforms.jsonl"))
	if err != nil {
		exitf("create transforms: %v", err)
	}
	defer transformsFile.Close()
	suiteFile, err := os.Create(*suitePath)
	if err != nil {
		exitf("create suite: %v", err)
	}
	defer suiteFile.Close()

	corpusEnc := json.NewEncoder(corpusFile)
	casesEnc := json.NewEncoder(casesFile)
	qrelsEnc := json.NewEncoder(qrelsFile)
	answersEnc := json.NewEncoder(answersFile)
	negativesEnc := json.NewEncoder(negativesFile)
	transformsEnc := json.NewEncoder(transformsFile)
	suiteEnc := json.NewEncoder(suiteFile)

	caseIndex := 0
	corpusCount := 0
	for _, spec := range prSlices {
		for i := 0; i < spec.count; i++ {
			caseIndex++
			caseID := fmt.Sprintf("%s_%03d", spec.name, i+1)
			answerCode := fmt.Sprintf("DM-EVAL-%03d", caseIndex)
			entity := fmt.Sprintf("dense mem eval entity %03d", caseIndex)
			requiredDocID := fmt.Sprintf("%s_required", caseID)
			required := evalharness.CorpusItem{
				SourceDocID:   requiredDocID,
				Title:         strings.ReplaceAll(caseID, "_", " "),
				Content:       fmt.Sprintf("%s has canonical answer code %s. The evidence slice is %s. This row is the authoritative local evaluation fact for case %s.", entity, answerCode, spec.name, caseID),
				SourceDataset: "densemem_synthetic_pr_v1",
				SourceType:    "document",
				Authority:     "authoritative",
				SourceQuality: 0.95,
				Labels:        []string{"eval", spec.name, "required"},
				Metadata: map[string]any{
					"case_id":           caseID,
					"expected_behavior": behaviorForSlice(spec.name),
				},
			}
			mustEncode(corpusEnc, required)
			corpusCount++

			badRefs := []evalharness.Ref{}
			for doc := 1; doc < *docsPerCase; doc++ {
				sourceDocID := fmt.Sprintf("%s_negative_%02d", caseID, doc)
				negativeCode := fmt.Sprintf("DM-DISTRACTOR-%03d-%02d", caseIndex, doc)
				negative := evalharness.CorpusItem{
					SourceDocID:   sourceDocID,
					Title:         fmt.Sprintf("%s distractor %02d", strings.ReplaceAll(caseID, "_", " "), doc),
					Content:       fmt.Sprintf("%s distractor %02d claims canonical answer code %s, which is intentionally wrong for case %s but lexically similar to the required evidence.", entity, doc, negativeCode, caseID),
					SourceDataset: "densemem_synthetic_pr_v1",
					SourceType:    "document",
					Authority:     "secondary",
					SourceQuality: 0.2,
					Labels:        []string{"eval", spec.name, "hard_negative"},
					Metadata: map[string]any{
						"case_id":      caseID,
						"negative_for": requiredDocID,
					},
				}
				mustEncode(corpusEnc, negative)
				corpusCount++
				if doc <= 3 {
					badRefs = append(badRefs, evalharness.Ref{Type: "fragment", SourceDocID: sourceDocID, Reason: "synthetic hard negative"})
				}
				mustEncode(negativesEnc, map[string]any{
					"case_id":       caseID,
					"source_doc_id": sourceDocID,
					"reason":        "synthetic hard negative with similar wording and wrong answer code",
				})
			}

			tc := evalharness.Case{
				CaseID:           caseID,
				Query:            fmt.Sprintf("For %s, what canonical answer code should Dense-Mem recall?", entity),
				TaskType:         "retrieval",
				Difficulty:       difficultyForSlice(spec.name),
				Slices:           []string{spec.name},
				ExpectedBehavior: behaviorForSlice(spec.name),
				Limit:            10,
			}
			mustEncode(casesEnc, tc)
			mustEncode(qrelsEnc, evalharness.QRel{
				CaseID:       caseID,
				RequiredRefs: []evalharness.Ref{{Type: "fragment", SourceDocID: requiredDocID, Grade: 3}},
				BadRefs:      badRefs,
			})
			mustEncode(answersEnc, evalharness.AnswerLabel{
				CaseID:           caseID,
				ReferenceAnswer:  answerCode,
				MustInclude:      []string{answerCode},
				MustNotInclude:   []string{"DM-DISTRACTOR"},
				ExpectedBehavior: behaviorForSlice(spec.name),
			})
			mustEncode(transformsEnc, map[string]any{
				"case_id":         caseID,
				"transform":       "deterministic_synthetic_pr_v1",
				"required_doc_id": requiredDocID,
				"docs_per_case":   *docsPerCase,
			})
			mustEncode(suiteEnc, evalharness.SuiteCase{CaseID: caseID, Slices: []string{spec.name}, Weight: 1})
		}
	}

	manifest := evalharness.SeedManifest{
		SchemaVersion:     evalharness.SeedSchemaVersion,
		SeedID:            "local_pr_v1",
		Description:       "Deterministic local-only PR-scale synthetic retrieval seed for Dense-Mem RAG accuracy baselines.",
		GeneratedAt:       time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		CorpusFile:        "corpus.jsonl",
		CasesFile:         "cases.jsonl",
		QrelsFile:         "qrels.jsonl",
		AnswersFile:       "answers.jsonl",
		HardNegativesFile: "hard_negatives.jsonl",
		TransformsFile:    "transforms.jsonl",
		LicensesFile:      "licenses.md",
		Counts: map[string]int{
			"cases":          caseIndex,
			"corpus":         corpusCount,
			"docs_per_case":  *docsPerCase,
			"hard_negatives": caseIndex * (*docsPerCase - 1),
		},
		Sources: []evalharness.SeedSource{{
			Name:    "densemem_synthetic_pr_v1",
			License: "Project-local synthetic fixture",
			Notes:   "Generated by cmd/eval-seedgen; no production data or external corpus content.",
		}},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		exitf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "seed_manifest.json"), append(manifestBytes, '\n'), 0o644); err != nil {
		exitf("write manifest: %v", err)
	}
	license := "# local_pr_v1 Licenses\n\nThis seed is deterministic synthetic project-local fixture data generated by `cmd/eval-seedgen`.\nIt contains no production data and no copied public-dataset content.\n"
	if err := os.WriteFile(filepath.Join(*outDir, "licenses.md"), []byte(license), 0o644); err != nil {
		exitf("write licenses: %v", err)
	}
	fmt.Printf("wrote %d cases and %d corpus rows to %s; suite %s\n", caseIndex, corpusCount, *outDir, *suitePath)
}

func mustEncode(enc *json.Encoder, value any) {
	if err := enc.Encode(value); err != nil {
		exitf("encode: %v", err)
	}
}

func behaviorForSlice(slice string) string {
	switch slice {
	case "no_answer_abstain":
		return "abstain_when_required_ref_missing"
	case "security_scope":
		return "same_team_only"
	default:
		return "answer"
	}
}

func difficultyForSlice(slice string) string {
	switch slice {
	case "hard_negatives", "multi_hop_graph", "community_global", "long_context_noisy_team":
		return "hard"
	case "temporal_stale", "conflict_clarification", "security_scope":
		return "medium"
	default:
		return "easy"
	}
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
