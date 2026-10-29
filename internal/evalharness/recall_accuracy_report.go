package evalharness

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
)

const RecallAccuracyReportSchema = "dense-mem.eval.recall_accuracy_report.v1"

type RecallAccuracyExperiment struct {
	SchemaVersion string   `json:"schema_version"`
	PairConfigs   []string `json:"pair_configs"`
	OutDir        string   `json:"out_dir"`
}

type RecallAccuracyDistribution struct {
	Count   int     `json:"count"`
	Missing int     `json:"missing"`
	Mean    float64 `json:"mean"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	P50     float64 `json:"p50"`
	P95     float64 `json:"p95"`
	P99     float64 `json:"p99"`
	StdDev  float64 `json:"population_stddev"`
}

type RecallAccuracyCaseComparison struct {
	CaseID     string         `json:"case_id"`
	Axes       []string       `json:"axes"`
	Baseline   RetrievalScore `json:"baseline"`
	Candidate  RetrievalScore `json:"candidate"`
	Regression bool           `json:"regression"`
}

type RecallAccuracyRankOne struct {
	Cases        int     `json:"cases"`
	RequiredRate float64 `json:"required_rate"`
	BadRate      float64 `json:"bad_rate"`
}

type RecallAccuracyRepetition struct {
	Pair           PairReport                                  `json:"pair"`
	Cases          []RecallAccuracyCaseComparison              `json:"cases"`
	LatencyMS      map[string]RecallAccuracyDistribution       `json:"latency_ms"`
	AxisRankOne    map[string]map[string]RecallAccuracyRankOne `json:"axis_rank_one"`
	Passed         bool                                        `json:"passed"`
	FailureReasons []string                                    `json:"failure_reasons"`
}

type RecallAccuracyExperimentReport struct {
	SchemaVersion string                                `json:"schema_version"`
	SeedID        string                                `json:"seed_id"`
	SeedHash      string                                `json:"seed_hash"`
	SuiteSHA256   string                                `json:"suite_sha256"`
	Repetitions   []RecallAccuracyRepetition            `json:"repetitions"`
	Variability   map[string]RecallAccuracyDistribution `json:"variability"`
	Passed        bool                                  `json:"passed"`
	Limitations   []string                              `json:"limitations"`
}

func WriteRecallAccuracyReport(manifestPath string) (RecallAccuracyExperimentReport, error) {
	report := RecallAccuracyExperimentReport{SchemaVersion: RecallAccuracyReportSchema, Passed: true,
		Repetitions: []RecallAccuracyRepetition{}, Variability: map[string]RecallAccuracyDistribution{},
		Limitations: []string{"Independent model-generated ingestion and ontology state can differ; fixed-state regressions establish the ranking mechanism.", "Host load affects latency. Zero or omitted millisecond latency is counted as unavailable.", "This report does not evaluate AI_DECISION reranking or isolated ontology expansion."}}
	var manifest RecallAccuracyExperiment
	if err := readPairJSON(manifestPath, &manifest); err != nil {
		return report, err
	}
	if manifest.SchemaVersion != RecallAccuracyReportSchema || len(manifest.PairConfigs) == 0 || len(manifest.PairConfigs) > 3 || !filepath.IsAbs(manifest.OutDir) {
		return report, fmt.Errorf("recall accuracy report requires its schema, paired configs and an absolute output directory")
	}
	seen := map[string]bool{}
	seenCandidateScopes := map[PairScope]bool{}
	var first PairConfig
	values := map[string][]float64{}
	for index, path := range manifest.PairConfigs {
		if !filepath.IsAbs(path) {
			return report, fmt.Errorf("paired configuration paths must be absolute")
		}
		c, err := LoadPairConfig(path)
		if err != nil {
			return report, err
		}
		if seen[c.RunID] {
			return report, fmt.Errorf("duplicate paired run %s", c.RunID)
		}
		seen[c.RunID] = true
		if seenCandidateScopes[c.Candidate.Scope] {
			return report, fmt.Errorf("candidate repetitions must use distinct fresh runtime scopes")
		}
		seenCandidateScopes[c.Candidate.Scope] = true
		if c.Baseline.Models != c.Candidate.Models || c.Baseline.Capacity != c.Candidate.Capacity || c.Baseline.ImportConcurrency != c.Candidate.ImportConcurrency {
			return report, fmt.Errorf("paired measurements require identical model, capacity and import settings")
		}
		if err := accuracyReportOutput(c, manifest.OutDir); err != nil {
			return report, err
		}
		if index == 0 {
			first = c
			report.SeedHash, report.SuiteSHA256 = c.SeedHash, c.SuiteSHA256
		} else if c.SeedHash != first.SeedHash || c.SuiteSHA256 != first.SuiteSHA256 || c.Scorer != first.Scorer ||
			c.Baseline.SourceRevision != first.Baseline.SourceRevision || c.Candidate.SourceRevision != first.Candidate.SourceRevision ||
			c.Baseline.ImageID != first.Baseline.ImageID || c.Candidate.ImageID != first.Candidate.ImageID ||
			c.Baseline.Models != first.Baseline.Models || c.Candidate.Models != first.Candidate.Models {
			return report, fmt.Errorf("repetitions must share frozen inputs, source/image/model identities and one scorer")
		}
		run, seedID, err := readAccuracyRepetition(c)
		if err != nil {
			return report, fmt.Errorf("%s: %w", c.RunID, err)
		}
		report.SeedID = seedID
		if index > 0 {
			baseline, original := run.Pair.Sides["baseline"], report.Repetitions[0].Pair.Sides["baseline"]
			if baseline.Identity != original.Identity || baseline.Progress.SourceHash != original.Progress.SourceHash || baseline.Progress.CanonicalHash != original.Progress.CanonicalHash || baseline.Progress.WindowID != original.Progress.WindowID {
				return report, fmt.Errorf("repetitions must retain the original baseline capture identity and state")
			}
		}
		report.Repetitions = append(report.Repetitions, run)
		report.Passed = report.Passed && run.Passed
		for metric, value := range map[string]float64{"recall_delta": run.Pair.Comparison.RecallDelta, "mrr_delta": run.Pair.Comparison.MRRDelta, "ndcg_delta": run.Pair.Comparison.NDCGDelta, "bad_at_10_delta": run.Pair.Comparison.BadAtKDelta} {
			values[metric] = append(values[metric], value)
		}
	}
	if report.SeedID == RecallAccuracySeedID && len(report.Repetitions) != 3 {
		return report, fmt.Errorf("recall_accuracy_v1 requires all three predeclared candidate repetitions")
	}
	for metric, samples := range values {
		report.Variability[metric] = accuracyDistribution(samples, 0)
	}
	if err := os.Mkdir(manifest.OutDir, 0o700); err != nil {
		return report, fmt.Errorf("create a new recall accuracy report directory: %w", err)
	}
	if err := writePairJSON(filepath.Join(manifest.OutDir, "recall_accuracy_report.json"), report); err != nil {
		return report, err
	}
	return report, os.WriteFile(filepath.Join(manifest.OutDir, "recall_accuracy_report.md"), []byte(accuracyReportMarkdown(report)), 0o600)
}

func accuracyReportOutput(c PairConfig, output string) error {
	resolved, err := resolvedPairPath(output)
	if err != nil {
		return err
	}
	for _, input := range []string{c.OutDir, c.Baseline.CaptureDir, c.Baseline.Runtime.StateDir, c.Candidate.Runtime.StateDir, filepath.Dir(c.SeedManifest), filepath.Dir(c.Suite)} {
		if input == "" {
			continue
		}
		protected, err := resolvedPairPath(input)
		if err != nil {
			return err
		}
		if resolved == protected || pairWithin(protected, resolved) || pairWithin(resolved, protected) {
			return fmt.Errorf("recall accuracy output overlaps retained inputs or runtime state")
		}
	}
	return nil
}

func readAccuracyRepetition(c PairConfig) (RecallAccuracyRepetition, string, error) {
	run := RecallAccuracyRepetition{Passed: true, FailureReasons: []string{}, Cases: []RecallAccuracyCaseComparison{},
		LatencyMS: map[string]RecallAccuracyDistribution{}, AxisRankOne: map[string]map[string]RecallAccuracyRankOne{}}
	var err error
	run.Pair, err = readPairReport(c)
	if err != nil {
		return run, "", err
	}
	if run.Pair.Status != "complete" {
		return run, "", fmt.Errorf("incomplete paired coverage cannot establish recall accuracy")
	}
	seed, _, cases, _, _, suite, _, err := loadRunInputs(c.SeedManifest, c.Suite)
	if err != nil {
		return run, "", err
	}
	scores := map[string]map[string]RetrievalScore{}
	for _, side := range []string{"baseline", "candidate"} {
		scoreDir, err := latestPairStage(c, side, "score")
		if err != nil {
			return run, "", err
		}
		scores[side], err = readAccuracyScores(c, filepath.Join(scoreDir, "native"), run.Pair.Sides[side].Scores, seed, cases, suite)
		if err != nil {
			return run, "", err
		}
		latencies := []float64{}
		for _, score := range scores[side] {
			if score.LatencyMS > 0 {
				latencies = append(latencies, float64(score.LatencyMS))
			}
		}
		run.LatencyMS[side] = accuracyDistribution(latencies, len(suite)-len(latencies))
		run.AxisRankOne[side] = map[string]RecallAccuracyRankOne{}
	}
	indexed := IndexCases(cases)
	for _, item := range suite {
		before, after := scores["baseline"][item.CaseID], scores["candidate"][item.CaseID]
		axes := append(append([]string{}, item.Slices...), indexed[item.CaseID].Slices...)
		sort.Strings(axes)
		axes = slices.Compact(axes)
		run.Cases = append(run.Cases, RecallAccuracyCaseComparison{CaseID: item.CaseID, Axes: axes, Baseline: before, Candidate: after,
			Regression: after.RecallAtK < before.RecallAtK || after.MRR < before.MRR || after.NDCGAtK < before.NDCGAtK || after.BadAtK > before.BadAtK || (after.FirstBadRank == 1 && before.FirstBadRank != 1)})
		for _, side := range []string{"baseline", "candidate"} {
			for _, axis := range axes {
				value := run.AxisRankOne[side][axis]
				value.Cases++
				if scores[side][item.CaseID].FirstRequiredRank == 1 {
					value.RequiredRate++
				}
				if scores[side][item.CaseID].FirstBadRank == 1 {
					value.BadRate++
				}
				run.AxisRankOne[side][axis] = value
			}
		}
		if seed.SeedID == RecallAccuracySeedID && slices.Contains(axes, "generic_predicates") && after.FirstBadRank == 1 {
			run.FailureReasons = append(run.FailureReasons, item.CaseID+": bad rank-one result")
		}
	}
	for side, axes := range run.AxisRankOne {
		for axis, value := range axes {
			value.RequiredRate /= float64(value.Cases)
			value.BadRate /= float64(value.Cases)
			run.AxisRankOne[side][axis] = value
		}
	}
	if run.Pair.Comparison.RecallDelta < 0 {
		run.FailureReasons = append(run.FailureReasons, "aggregate Recall@10 declined")
	}
	if run.Pair.Comparison.BadAtKDelta > 0 {
		run.FailureReasons = append(run.FailureReasons, "aggregate Bad@10 increased")
	}
	if seed.SeedID == "public_6axis_1k_v3" {
		if run.Pair.Comparison.NDCGDelta <= 0 {
			run.FailureReasons = append(run.FailureReasons, "aggregate nDCG@10 did not improve")
		}
		if run.Pair.Comparison.MRRDelta < 0 {
			run.FailureReasons = append(run.FailureReasons, "aggregate MRR declined")
		}
	}
	run.Passed = len(run.FailureReasons) == 0
	return run, seed.SeedID, nil
}

func readAccuracyScores(c PairConfig, dir string, summary Summary, seed *SeedManifest, cases []Case, suite []SuiteCase) (map[string]RetrievalScore, error) {
	if err := ValidatePairCapture(c, dir); err != nil {
		return nil, err
	}
	var rows []RetrievalScore
	if err := readJSONL(filepath.Join(dir, "retrieval_scores.jsonl"), &rows); err != nil {
		return nil, err
	}
	actual := SummarizeScores(summary.RunID, summary.Mode, seed.SeedID, c.SeedHash, summary.SuitePath, suite, IndexCases(cases), rows)
	actual.CreatedAt = summary.CreatedAt
	if !reflect.DeepEqual(actual, summary) {
		return nil, fmt.Errorf("native aggregate summary differs from per-case scores")
	}
	indexed := map[string]RetrievalScore{}
	for _, row := range rows {
		indexed[row.CaseID] = row
	}
	return indexed, nil
}

func accuracyDistribution(values []float64, missing int) RecallAccuracyDistribution {
	result := RecallAccuracyDistribution{Count: len(values), Missing: missing}
	if len(values) == 0 {
		return result
	}
	values = append([]float64(nil), values...)
	sort.Float64s(values)
	result.Min, result.Max = values[0], values[len(values)-1]
	for _, value := range values {
		result.Mean += value / float64(len(values))
	}
	for _, value := range values {
		result.StdDev += math.Pow(value-result.Mean, 2) / float64(len(values))
	}
	result.StdDev = math.Sqrt(result.StdDev)
	percentile := func(p float64) float64 { return values[int(math.Ceil(p*float64(len(values))))-1] }
	result.P50, result.P95, result.P99 = percentile(.50), percentile(.95), percentile(.99)
	return result
}

func accuracyReportMarkdown(report RecallAccuracyExperimentReport) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Recall accuracy experiment\n\nSeed: %s. Repetitions: %d. Declared quality gates passed: %t.\n\n", report.SeedID, len(report.Repetitions), report.Passed)
	out.WriteString("| Run | Recall delta | MRR delta | nDCG delta | Bad@10 delta | Candidate p95 ms | Passed |\n|---|---:|---:|---:|---:|---:|---|\n")
	for _, run := range report.Repetitions {
		c := run.Pair.Comparison
		fmt.Fprintf(&out, "| %s | %.6f | %.6f | %.6f | %.6f | %.1f | %t |\n", run.Pair.RunID, c.RecallDelta, c.MRRDelta, c.NDCGDelta, c.BadAtKDelta, run.LatencyMS["candidate"].P95, run.Passed)
	}
	out.WriteString("\nThe JSON report retains every case, axis summary, rank-one result, latency distribution, usage/failure receipt and repetition variability.\n\n")
	for _, limitation := range report.Limitations {
		fmt.Fprintf(&out, "- %s\n", limitation)
	}
	return out.String()
}
