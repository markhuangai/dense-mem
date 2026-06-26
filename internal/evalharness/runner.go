package evalharness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RunOptions struct {
	Mode             string
	SeedManifestPath string
	SuitePath        string
	OutDir           string
	BaseURL          string
	APIKey           string
	ControlURL       string
	ControlToken     string
	ImportSeed       bool
	TracesPath       string
	MaxPageSize      int
	RunID            string
}

func Run(ctx context.Context, opts RunOptions) (Summary, error) {
	mode := strings.TrimSpace(opts.Mode)
	if mode == "" {
		mode = "validate"
	}
	if mode != "validate" && mode != "baseline" && mode != "candidate" {
		return Summary{}, fmt.Errorf("unsupported mode %q", mode)
	}
	if opts.SeedManifestPath == "" {
		return Summary{}, fmt.Errorf("seed manifest path is required")
	}
	if opts.SuitePath == "" {
		return Summary{}, fmt.Errorf("suite path is required")
	}
	runID := opts.RunID
	if runID == "" {
		runID = newRunID(mode)
	}
	manifest, corpus, cases, qrels, suite, seedHash, err := loadRunInputs(opts.SeedManifestPath, opts.SuitePath)
	if err != nil {
		return Summary{}, err
	}
	if err := validateSuite(cases, qrels, suite); err != nil {
		return Summary{}, err
	}
	runConfig := RunConfig{
		RunID:        runID,
		Mode:         mode,
		SeedManifest: opts.SeedManifestPath,
		SeedHash:     seedHash,
		SuitePath:    opts.SuitePath,
		BaseURL:      opts.BaseURL,
		ControlURL:   opts.ControlURL,
		ImportSeed:   opts.ImportSeed,
		TracesPath:   opts.TracesPath,
	}
	if mode == "validate" {
		summary := Summary{
			RunID:           runID,
			Mode:            mode,
			SeedID:          manifest.SeedID,
			SeedHash:        seedHash,
			SuitePath:       opts.SuitePath,
			CaseCount:       len(suite),
			ScoredCaseCount: 0,
			CreatedAt:       time.Now().UTC(),
		}
		if opts.OutDir != "" {
			if err := writeValidationArtifacts(opts.OutDir, manifest, suite, runConfig, summary); err != nil {
				return Summary{}, err
			}
		}
		return summary, nil
	}

	var traces []RecallTrace
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{}}
	if opts.TracesPath != "" {
		traces, err = LoadRecallTraces(opts.TracesPath)
		if err != nil {
			return Summary{}, err
		}
	} else {
		client := &HTTPClient{
			BaseURL:      opts.BaseURL,
			APIKey:       opts.APIKey,
			ControlURL:   opts.ControlURL,
			ControlToken: opts.ControlToken,
		}
		if err := client.EnableEvaluationMode(ctx, opts.MaxPageSize); err != nil {
			return Summary{}, err
		}
		if opts.ImportSeed {
			mapping, err = client.ImportCorpus(ctx, corpus)
			if err != nil {
				return Summary{}, err
			}
		}
		exported, err := client.ExportFragmentMapping(ctx, opts.MaxPageSize)
		if err != nil {
			return Summary{}, err
		}
		for sourceDocID, ref := range exported.BySourceDocID {
			mapping.BySourceDocID[sourceDocID] = ref
		}
		traces, err = runLiveSuite(ctx, client, suite, IndexCases(cases))
		if err != nil {
			return Summary{}, err
		}
	}

	scores, summary, err := ScoreTraces(runID, mode, manifest.SeedID, seedHash, opts.SuitePath, suite, IndexCases(cases), IndexQrels(qrels), traces, mapping)
	if err != nil {
		return Summary{}, err
	}
	if opts.OutDir != "" {
		if err := writeRunArtifacts(opts.OutDir, manifest, suite, runConfig, mapping, traces, scores, summary); err != nil {
			return Summary{}, err
		}
	}
	return summary, nil
}

func CompareRunDirs(baselineRunDir, candidateRunDir, outDir string) (Comparison, error) {
	var baseline Summary
	if err := readJSONFile(filepath.Join(baselineRunDir, "summary.json"), &baseline); err != nil {
		return Comparison{}, err
	}
	var candidate Summary
	if err := readJSONFile(filepath.Join(candidateRunDir, "summary.json"), &candidate); err != nil {
		return Comparison{}, err
	}
	comparison, err := CompareSummaries(baseline, candidate)
	if err != nil {
		return Comparison{}, err
	}
	if outDir == "" {
		outDir = candidateRunDir
	}
	if err := writeJSONFile(filepath.Join(outDir, "comparison.json"), comparison); err != nil {
		return Comparison{}, err
	}
	return comparison, nil
}

func loadRunInputs(manifestPath, suitePath string) (*SeedManifest, []CorpusItem, []Case, []QRel, []SuiteCase, string, error) {
	manifest, err := LoadSeedManifest(manifestPath)
	if err != nil {
		return nil, nil, nil, nil, nil, "", err
	}
	corpus, err := LoadCorpus(manifestPath, manifest)
	if err != nil {
		return nil, nil, nil, nil, nil, "", err
	}
	cases, err := LoadCases(manifestPath, manifest)
	if err != nil {
		return nil, nil, nil, nil, nil, "", err
	}
	qrels, err := LoadQrels(manifestPath, manifest)
	if err != nil {
		return nil, nil, nil, nil, nil, "", err
	}
	suite, err := LoadSuite(suitePath)
	if err != nil {
		return nil, nil, nil, nil, nil, "", err
	}
	seedHash, err := SeedHash(manifestPath, manifest)
	if err != nil {
		return nil, nil, nil, nil, nil, "", err
	}
	return manifest, corpus, cases, qrels, suite, seedHash, nil
}

func validateSuite(cases []Case, qrels []QRel, suite []SuiteCase) error {
	caseIndex := IndexCases(cases)
	qrelIndex := IndexQrels(qrels)
	for _, suiteCase := range suite {
		if _, ok := caseIndex[suiteCase.CaseID]; !ok {
			return fmt.Errorf("suite case %q missing from seed cases", suiteCase.CaseID)
		}
		if _, ok := qrelIndex[suiteCase.CaseID]; !ok {
			return fmt.Errorf("suite case %q missing from seed qrels", suiteCase.CaseID)
		}
	}
	return nil
}

func runLiveSuite(ctx context.Context, client *HTTPClient, suite []SuiteCase, cases map[string]Case) ([]RecallTrace, error) {
	traces := make([]RecallTrace, 0, len(suite))
	for _, suiteCase := range suite {
		tc := cases[suiteCase.CaseID]
		trace, err := client.RunRecallCase(ctx, tc)
		if err != nil {
			return traces, fmt.Errorf("run case %s: %w", suiteCase.CaseID, err)
		}
		traces = append(traces, trace)
	}
	return traces, nil
}

func writeValidationArtifacts(outDir string, manifest *SeedManifest, suite []SuiteCase, runConfig RunConfig, summary Summary) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(outDir, "run_config.json"), runConfig); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(outDir, "seed_manifest.json"), manifest); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(outDir, "suite.jsonl"), suite); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(outDir, "summary.json"), summary)
}

func writeRunArtifacts(outDir string, manifest *SeedManifest, suite []SuiteCase, runConfig RunConfig, mapping KnowledgeMapping, traces []RecallTrace, scores []RetrievalScore, summary Summary) error {
	if err := writeValidationArtifacts(outDir, manifest, suite, runConfig, summary); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(outDir, "knowledge_mapping.json"), mapping); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(outDir, "recall_traces.jsonl"), traces); err != nil {
		return err
	}
	return writeJSONL(filepath.Join(outDir, "retrieval_scores.jsonl"), scores)
}

func newRunID(mode string) string {
	return time.Now().UTC().Format("20060102T150405Z") + "_" + mode
}
