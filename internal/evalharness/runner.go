package evalharness

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RunOptions struct {
	Mode                   string
	SeedManifestPath       string
	SuitePath              string
	OutDir                 string
	BaseURL                string
	APIKey                 string
	ControlURL             string
	ControlToken           string
	ImportSeed             bool
	ImportConcurrency      int
	PlacementTimeout       time.Duration
	ResumeSourceDocIDsPath string
	TracesPath             string
	MappingPath            string
	MaxPageSize            int
	RunID                  string
	ReleaseGatePolicyPath  string
	Gates                  GateOptions
}

func Run(ctx context.Context, opts RunOptions) (Summary, error) {
	mode := strings.TrimSpace(opts.Mode)
	if mode == "" {
		mode = "validate"
	}
	if mode != "validate" && mode != "import" && mode != "baseline" && mode != "candidate" {
		return Summary{}, fmt.Errorf("unsupported mode %q", mode)
	}
	if opts.ReleaseGatePolicyPath != "" && mode != "validate" && mode != "baseline" && mode != "candidate" {
		return Summary{}, fmt.Errorf("release gate policy requires validate, baseline, or candidate mode")
	}
	if opts.SeedManifestPath == "" {
		return Summary{}, fmt.Errorf("seed manifest path is required")
	}
	if opts.SuitePath == "" {
		return Summary{}, fmt.Errorf("suite path is required")
	}
	if strings.TrimSpace(opts.ResumeSourceDocIDsPath) != "" && (mode != "import" || !opts.ImportSeed) {
		return Summary{}, fmt.Errorf("resume source document IDs require import mode with --import-seed")
	}
	if opts.PlacementTimeout < 0 {
		return Summary{}, fmt.Errorf("placement timeout must not be negative")
	}
	if opts.PlacementTimeout == 0 {
		opts.PlacementTimeout = 2 * time.Minute
	}
	runID := opts.RunID
	if runID == "" {
		runID = newRunID(mode)
	}
	manifest, corpus, cases, qrels, expectedDreams, suite, seedHash, err := loadRunInputs(opts.SeedManifestPath, opts.SuitePath)
	if err != nil {
		return Summary{}, err
	}
	if err := validateRunInputs(opts.SeedManifestPath, manifest, corpus, cases, qrels, expectedDreams, suite, seedHash); err != nil {
		return Summary{}, err
	}
	inputSummary := Summary{
		RunID:           runID,
		Mode:            mode,
		SeedID:          manifest.SeedID,
		SeedHash:        seedHash,
		SuitePath:       opts.SuitePath,
		CaseCount:       len(suite),
		ScoredCaseCount: 0,
		CreatedAt:       time.Now().UTC(),
	}
	var releaseGatePolicy *ReleaseGatePolicy
	var releaseGateInput *ReleaseGateInputResult
	var releaseGatePolicyHash string
	if opts.ReleaseGatePolicyPath != "" {
		policy, err := LoadReleaseGatePolicy(opts.ReleaseGatePolicyPath)
		if err != nil {
			return Summary{}, fmt.Errorf("release gate policy: %w", err)
		}
		result := EvaluateReleaseGateInput(inputSummary, policy)
		policyHash, err := canonicalJSONHash(policy)
		if err != nil {
			return Summary{}, fmt.Errorf("hash release gate policy: %w", err)
		}
		releaseGatePolicy = &policy
		releaseGateInput = &result
		releaseGatePolicyHash = policyHash
	}
	importRoute := ""
	if opts.ImportSeed {
		importRoute = "remember"
	}
	runConfig := RunConfig{
		RunID:                  runID,
		Mode:                   mode,
		SeedManifest:           opts.SeedManifestPath,
		SeedHash:               seedHash,
		SuitePath:              opts.SuitePath,
		ReleaseGatePolicyPath:  opts.ReleaseGatePolicyPath,
		ReleaseGatePolicyHash:  releaseGatePolicyHash,
		BaseURL:                opts.BaseURL,
		ControlURL:             opts.ControlURL,
		ToolTransport:          "mcp",
		ToolContract:           "mcp.tools/call.v1",
		ImportSeed:             opts.ImportSeed,
		ImportRoute:            importRoute,
		ImportConcurrency:      opts.ImportConcurrency,
		PlacementTimeout:       opts.PlacementTimeout.String(),
		ResumeSourceDocIDsPath: opts.ResumeSourceDocIDsPath,
		TracesPath:             opts.TracesPath,
		MappingPath:            opts.MappingPath,
	}
	if mode == "validate" || (releaseGateInput != nil && !releaseGateInput.Passed) {
		if opts.OutDir != "" {
			if err := writeValidationArtifacts(opts.OutDir, manifest, suite, runConfig, inputSummary); err != nil {
				return Summary{}, err
			}
			if releaseGateInput != nil {
				if err := writeJSONFile(filepath.Join(opts.OutDir, "release_gate_input_result.json"), releaseGateInput); err != nil {
					return Summary{}, err
				}
			}
		}
		if releaseGateInput != nil && !releaseGateInput.Passed {
			return inputSummary, fmt.Errorf("release gate input check failed: %s", strings.Join(releaseGateInput.Failures, "; "))
		}
		if mode == "validate" {
			return inputSummary, nil
		}
	}
	if mode == "import" && opts.TracesPath != "" {
		return Summary{}, fmt.Errorf("import mode cannot use --traces")
	}
	if mode == "import" && !opts.ImportSeed {
		return Summary{}, fmt.Errorf("import mode requires --import-seed")
	}

	var traces []RecallTrace
	mapping := newKnowledgeMapping()
	if opts.TracesPath != "" {
		traces, err = LoadRecallTraces(opts.TracesPath)
		if err != nil {
			return Summary{}, err
		}
		if opts.MappingPath != "" {
			if err := readJSONFile(opts.MappingPath, &mapping); err != nil {
				return Summary{}, err
			}
		}
	} else {
		client := &HTTPClient{
			BaseURL:          opts.BaseURL,
			APIKey:           opts.APIKey,
			ControlURL:       opts.ControlURL,
			ControlToken:     opts.ControlToken,
			PlacementTimeout: opts.PlacementTimeout,
		}
		if err := client.EnableEvaluationMode(ctx, opts.MaxPageSize); err != nil {
			return Summary{}, err
		}
		mappingLoadedFromPath := false
		if opts.MappingPath != "" {
			if err := readJSONFile(opts.MappingPath, &mapping); err != nil {
				return Summary{}, err
			}
			mappingLoadedFromPath = true
		}
		skipSourceDocIDs := map[string]struct{}{}
		if path := strings.TrimSpace(opts.ResumeSourceDocIDsPath); path != "" {
			checkpoint, err := loadSourceDocIDs(path)
			if err != nil {
				return Summary{}, fmt.Errorf("resume source document IDs: %w", err)
			}
			existing, err := client.ExportEvidenceMapping(ctx, opts.MaxPageSize)
			if err != nil {
				return Summary{}, fmt.Errorf("resume evidence mapping: %w", err)
			}
			mergeKnowledgeMapping(&mapping, existing)
			skipSourceDocIDs = completedMappedSourceDocIDs(checkpoint, existing)
		}
		if opts.ImportSeed {
			if mode == "import" {
				keepSourceDocIDs := sourceDocIDsForQRelMappings(qrels, expectedDreams)
				imported, _, err := client.ImportCorpusFileWithConcurrency(
					ctx,
					resolveSeedPath(opts.SeedManifestPath, manifest.CorpusFile),
					opts.ImportConcurrency,
					keepSourceDocIDs,
					skipSourceDocIDs,
				)
				if err != nil {
					return Summary{}, err
				}
				mergeKnowledgeMapping(&mapping, imported)
				exported, err := client.ExportKnowledgeMapping(ctx, opts.MaxPageSize)
				if err != nil {
					return Summary{}, err
				}
				mergeFilteredKnowledgeMapping(&mapping, exported, keepSourceDocIDs)
			} else {
				imported, err := client.ImportCorpusWithConcurrency(ctx, corpus, opts.ImportConcurrency)
				if err != nil {
					return Summary{}, err
				}
				mergeKnowledgeMapping(&mapping, imported)
			}
			if len(expectedDreams) > 0 {
				seeds := expectedDreamCycleSeeds(mapping, expectedDreams)
				if err := client.RunDreamCycle(ctx, len(expectedDreams), seeds...); err != nil {
					return Summary{}, err
				}
			}
		}
		if !mappingLoadedFromPath && mode != "import" {
			exported, err := client.ExportKnowledgeMapping(ctx, opts.MaxPageSize)
			if err != nil {
				return Summary{}, err
			}
			mergeKnowledgeMapping(&mapping, exported)
		}
		if len(expectedDreams) > 0 {
			dreams, err := client.ExportDreamMapping(ctx, opts.MaxPageSize)
			if err != nil {
				return Summary{}, err
			}
			mergeKnowledgeMapping(&mapping, dreams)
			mapExpectedDreams(&mapping, expectedDreams)
		}
		if err := validateRequiredQRelMappings(IndexQrels(qrels), suite, mapping); err != nil {
			return Summary{}, err
		}
		if mode == "import" {
			mappingHash, err := canonicalJSONHash(mapping)
			if err != nil {
				return Summary{}, fmt.Errorf("hash import mapping: %w", err)
			}
			runConfig.MappingHash = mappingHash
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
				if err := writeImportArtifacts(opts.OutDir, manifest, suite, runConfig, mapping, summary); err != nil {
					return Summary{}, err
				}
			}
			return summary, nil
		}
		traces, err = runLiveSuite(ctx, client, suite, IndexCases(cases))
		if err != nil {
			return Summary{}, err
		}
	}
	mappingHash, err := canonicalJSONHash(mapping)
	if err != nil {
		return Summary{}, fmt.Errorf("hash run mapping: %w", err)
	}
	runConfig.MappingHash = mappingHash

	scores, summary, err := ScoreTraces(runID, mode, manifest.SeedID, seedHash, opts.SuitePath, suite, IndexCases(cases), IndexQrels(qrels), traces, mapping)
	if err != nil {
		return Summary{}, err
	}
	if opts.OutDir != "" {
		if err := writeRunArtifacts(opts.OutDir, manifest, suite, runConfig, mapping, traces, scores, summary); err != nil {
			return Summary{}, err
		}
	}
	if opts.Gates.Any() {
		gate := EvaluateGates(summary, opts.Gates)
		if opts.OutDir != "" {
			if err := writeJSONFile(filepath.Join(opts.OutDir, "gate_result.json"), gate); err != nil {
				return Summary{}, err
			}
		}
		if !gate.Passed {
			return summary, fmt.Errorf("gate check failed: %s", strings.Join(gate.Failures, "; "))
		}
	}
	if releaseGatePolicy != nil {
		releaseGate := EvaluateReleaseGate(summary, *releaseGatePolicy)
		if opts.OutDir != "" {
			if err := writeJSONFile(filepath.Join(opts.OutDir, "release_gate_result.json"), releaseGate); err != nil {
				return Summary{}, err
			}
		}
		if !releaseGate.Passed {
			return summary, fmt.Errorf("release gate check failed: %s", strings.Join(releaseGate.Failures, "; "))
		}
	}
	return summary, nil
}

func validateRequiredQRelMappings(qrels map[string]QRel, suite []SuiteCase, mapping KnowledgeMapping) error {
	for _, suiteCase := range suite {
		qrel, ok := qrels[suiteCase.CaseID]
		if !ok {
			return fmt.Errorf("suite case %q missing from seed qrels", suiteCase.CaseID)
		}
		for _, refs := range []struct {
			label string
			refs  []Ref
		}{
			{"required", qrel.RequiredRefs},
			{"required evidence", qrel.RequiredEvidenceRefs},
			{"bad", qrel.BadRefs},
			{"bad evidence", qrel.BadEvidenceRefs},
			{"required dream", qrel.RequiredDreamRefs},
			{"bad dream", qrel.BadDreamRefs},
		} {
			for _, ref := range refs.refs {
				if strings.TrimSpace(ref.SourceDocID) == "" {
					continue
				}
				if _, ok := resolveRef(ref, mapping); !ok {
					return fmt.Errorf("%s qrel ref for case %q is unmapped after import: type=%s source_doc_id=%s", refs.label, suiteCase.CaseID, ref.Type, ref.SourceDocID)
				}
			}
		}
	}
	return nil
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

func loadRunInputs(manifestPath, suitePath string) (*SeedManifest, []CorpusItem, []Case, []QRel, []ExpectedDream, []SuiteCase, string, error) {
	manifest, err := LoadSeedManifest(manifestPath)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, "", err
	}
	corpus, err := LoadCorpus(manifestPath, manifest)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, "", err
	}
	cases, err := LoadCases(manifestPath, manifest)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, "", err
	}
	qrels, err := LoadQrels(manifestPath, manifest)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, "", err
	}
	expectedDreams, err := LoadExpectedDreams(manifestPath, manifest)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, "", err
	}
	suite, err := LoadSuite(suitePath)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, "", err
	}
	seedHash, err := SeedHash(manifestPath, manifest)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, "", err
	}
	return manifest, corpus, cases, qrels, expectedDreams, suite, seedHash, nil
}

func validateRunInputs(manifestPath string, manifest *SeedManifest, corpus []CorpusItem, cases []Case, qrels []QRel, expectedDreams []ExpectedDream, suite []SuiteCase, seedHash string) error {
	if err := validateManifestCounts(manifestPath, manifest, corpus, cases, qrels); err != nil {
		return err
	}
	if err := validateSeedValidationReport(manifestPath, manifest, seedHash); err != nil {
		return err
	}
	caseIndex := IndexCases(cases)
	qrelIndex := IndexQrels(qrels)
	if len(qrelIndex) != len(qrels) {
		return fmt.Errorf("duplicate qrels case_id detected")
	}
	for _, suiteCase := range suite {
		if _, ok := caseIndex[suiteCase.CaseID]; !ok {
			return fmt.Errorf("suite case %q missing from seed cases", suiteCase.CaseID)
		}
		if _, ok := qrelIndex[suiteCase.CaseID]; !ok {
			return fmt.Errorf("suite case %q missing from seed qrels", suiteCase.CaseID)
		}
	}
	corpusIndex := sourceDocIDIndexForCorpus(corpus)
	dreamIndex := sourceDocIDIndexForExpectedDreams(expectedDreams)
	for _, dream := range expectedDreams {
		if _, ok := caseIndex[dream.CaseID]; dream.CaseID != "" && !ok {
			return fmt.Errorf("expected dream %q references missing case %q", dream.SourceDocID, dream.CaseID)
		}
		if err := validateQRelRefs(dream.CaseID, "expected_dream.source_refs", dream.SourceRefs, corpusIndex); err != nil {
			return err
		}
	}
	for _, qrel := range qrels {
		if _, ok := caseIndex[qrel.CaseID]; !ok {
			return fmt.Errorf("qrels case %q missing from seed cases", qrel.CaseID)
		}
		if err := validateQRelRefs(qrel.CaseID, "required_refs", qrel.RequiredRefs, corpusIndex); err != nil {
			return err
		}
		if err := validateQRelRefs(qrel.CaseID, "acceptable_refs", qrel.AcceptableRefs, corpusIndex); err != nil {
			return err
		}
		if err := validateQRelRefs(qrel.CaseID, "bad_refs", qrel.BadRefs, corpusIndex); err != nil {
			return err
		}
		if err := validateQRelRefs(qrel.CaseID, "required_evidence_refs", qrel.RequiredEvidenceRefs, corpusIndex); err != nil {
			return err
		}
		if err := validateQRelRefs(qrel.CaseID, "bad_evidence_refs", qrel.BadEvidenceRefs, corpusIndex); err != nil {
			return err
		}
		if err := validateQRelRefs(qrel.CaseID, "required_dream_refs", qrel.RequiredDreamRefs, dreamIndex); err != nil {
			return err
		}
		if err := validateQRelRefs(qrel.CaseID, "acceptable_dream_refs", qrel.AcceptableDreamRefs, dreamIndex); err != nil {
			return err
		}
		if err := validateQRelRefs(qrel.CaseID, "bad_dream_refs", qrel.BadDreamRefs, dreamIndex); err != nil {
			return err
		}
	}
	for _, c := range cases {
		if hasSlice(c.Slices, "adversarial") && len(qrelIndex[c.CaseID].BadRefs) == 0 {
			return fmt.Errorf("adversarial case %q has no bad_refs", c.CaseID)
		}
	}
	return nil
}

type seedValidationReport struct {
	SchemaVersion string `json:"schema_version"`
	SeedID        string `json:"seed_id"`
	Status        string `json:"status"`
	SeedHash      string `json:"seed_hash"`
}

func validateSeedValidationReport(manifestPath string, manifest *SeedManifest, seedHash string) error {
	if manifest == nil {
		return nil
	}
	reportFile := strings.TrimSpace(manifest.ValidationReportFile)
	if reportFile == "" {
		if strings.HasPrefix(manifest.SeedID, "public_6axis_") {
			return fmt.Errorf("seed %q requires validation_report_file", manifest.SeedID)
		}
		return nil
	}
	var report seedValidationReport
	if err := readJSONFile(resolveSeedPath(manifestPath, reportFile), &report); err != nil {
		return fmt.Errorf("read validation report: %w", err)
	}
	if report.SchemaVersion != "dense-mem.eval.validation.v1" {
		return fmt.Errorf("validation report schema_version %q is unsupported", report.SchemaVersion)
	}
	if report.SeedID != manifest.SeedID {
		return fmt.Errorf("validation report seed_id %q does not match manifest seed_id %q", report.SeedID, manifest.SeedID)
	}
	if report.Status != "passed" {
		return fmt.Errorf("validation report status = %q, want passed", report.Status)
	}
	if report.SeedHash != seedHash {
		return fmt.Errorf("validation report seed_hash %q does not match current seed hash %q", report.SeedHash, seedHash)
	}
	return nil
}

func sourceDocIDIndexForExpectedDreams(dreams []ExpectedDream) map[string]struct{} {
	index := map[string]struct{}{}
	for _, dream := range dreams {
		if strings.TrimSpace(dream.SourceDocID) != "" {
			index[dream.SourceDocID] = struct{}{}
		}
	}
	return index
}

func sourceDocIDIndexForCorpus(corpus []CorpusItem) map[string]struct{} {
	corpusIndex := map[string]struct{}{}
	for _, item := range corpus {
		if strings.TrimSpace(item.SourceDocID) == "" {
			continue
		}
		corpusIndex[item.SourceDocID] = struct{}{}
	}
	return corpusIndex
}

func validateManifestCounts(manifestPath string, manifest *SeedManifest, corpus []CorpusItem, cases []Case, qrels []QRel) error {
	if manifest == nil || len(manifest.Counts) == 0 {
		return nil
	}
	if err := validateCount(manifest.Counts, "cases", len(cases)); err != nil {
		return err
	}
	if err := validateCount(manifest.Counts, "corpus", len(corpus)); err != nil {
		return err
	}
	if err := validateCount(manifest.Counts, "qrels", len(qrels)); err != nil {
		return err
	}
	if expected, ok := manifest.Counts["docs_per_case"]; ok {
		if len(cases) == 0 {
			return fmt.Errorf("manifest count docs_per_case=%d but seed has no cases", expected)
		}
		if len(corpus)%len(cases) != 0 {
			return fmt.Errorf("manifest count docs_per_case=%d mismatch: corpus=%d cases=%d", expected, len(corpus), len(cases))
		}
		actual := len(corpus) / len(cases)
		if actual != expected {
			return fmt.Errorf("manifest count docs_per_case=%d mismatch: got %d", expected, actual)
		}
	}
	for _, optional := range []struct {
		countName string
		fileName  string
	}{
		{countName: "answers", fileName: manifest.AnswersFile},
		{countName: "hard_negatives", fileName: manifest.HardNegativesFile},
		{countName: "transforms", fileName: manifest.TransformsFile},
		{countName: "dreams", fileName: manifest.DreamsFile},
	} {
		expected, ok := manifest.Counts[optional.countName]
		if !ok {
			continue
		}
		if strings.TrimSpace(optional.fileName) == "" {
			return fmt.Errorf("manifest count %s=%d but file is not set", optional.countName, expected)
		}
		actual, err := countSeedJSONLRows(manifestPath, optional.fileName)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("manifest count %s=%d mismatch: got %d", optional.countName, expected, actual)
		}
	}
	return nil
}

func sourceDocIDsForQRelMappings(qrels []QRel, dreams []ExpectedDream) map[string]struct{} {
	out := map[string]struct{}{}
	addRefs := func(refs []Ref) {
		for _, ref := range refs {
			if sourceDocID := strings.TrimSpace(ref.SourceDocID); sourceDocID != "" {
				out[sourceDocID] = struct{}{}
			}
		}
	}
	for _, qrel := range qrels {
		addRefs(qrel.RequiredRefs)
		addRefs(qrel.AcceptableRefs)
		addRefs(qrel.BadRefs)
		addRefs(qrel.RequiredEvidenceRefs)
		addRefs(qrel.BadEvidenceRefs)
	}
	for _, dream := range dreams {
		addRefs(dream.SourceRefs)
	}
	return out
}

func loadSourceDocIDs(path string) (map[string]struct{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]struct{}{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if sourceDocID := strings.TrimSpace(scanner.Text()); sourceDocID != "" {
			out[sourceDocID] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func completedMappedSourceDocIDs(checkpoint map[string]struct{}, mapping KnowledgeMapping) map[string]struct{} {
	out := map[string]struct{}{}
	for sourceDocID := range checkpoint {
		if len(mapping.BySourceDocIDAndType[sourceDocID]["evidence"]) > 0 {
			out[sourceDocID] = struct{}{}
		}
	}
	return out
}

func validateCount(counts map[string]int, name string, actual int) error {
	expected, ok := counts[name]
	if !ok {
		return nil
	}
	if actual != expected {
		return fmt.Errorf("manifest count %s=%d mismatch: got %d", name, expected, actual)
	}
	return nil
}

func validateQRelRefs(caseID, field string, refs []Ref, corpusIndex map[string]struct{}) error {
	for _, ref := range refs {
		if strings.TrimSpace(ref.SourceDocID) == "" {
			continue
		}
		if _, ok := corpusIndex[ref.SourceDocID]; !ok {
			return fmt.Errorf("qrels case %q %s source_doc_id %q missing from corpus", caseID, field, ref.SourceDocID)
		}
	}
	return nil
}

func hasSlice(slices []string, want string) bool {
	for _, slice := range slices {
		if strings.EqualFold(strings.TrimSpace(slice), want) {
			return true
		}
	}
	return false
}

func countSeedJSONLRows(manifestPath, rel string) (int, error) {
	path := resolveSeedPath(manifestPath, rel)
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	count := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return count, nil
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

func writeImportArtifacts(outDir string, manifest *SeedManifest, suite []SuiteCase, runConfig RunConfig, mapping KnowledgeMapping, summary Summary) error {
	if err := writeValidationArtifacts(outDir, manifest, suite, runConfig, summary); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(outDir, "knowledge_mapping.json"), mapping)
}

func newRunID(mode string) string {
	return time.Now().UTC().Format("20060102T150405Z") + "_" + mode
}
