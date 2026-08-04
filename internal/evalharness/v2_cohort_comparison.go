package evalharness

import (
	"fmt"
	"path/filepath"
	"strings"
)

const v2CohortComparisonSchema = "dense-mem.eval.v2_cohort_comparison.v1"

// CompareV2CohortRunDirs compares a validated V1/V2 cohort pair while keeping
// generic same-seed comparisons fail closed.
func CompareV2CohortRunDirs(opts V2CohortComparisonOptions) (V2CohortComparison, error) {
	if strings.TrimSpace(opts.BaselineRunDir) == "" {
		return V2CohortComparison{}, fmt.Errorf("baseline run directory is required")
	}
	if strings.TrimSpace(opts.CandidateRunDir) == "" {
		return V2CohortComparison{}, fmt.Errorf("candidate run directory is required")
	}
	cohort, err := ValidateV2CohortDerivation(opts.Cohort)
	if err != nil {
		return V2CohortComparison{}, fmt.Errorf("validate V2 cohort: %w", err)
	}
	if cohort.RetainedCaseCount <= 0 {
		return V2CohortComparison{}, fmt.Errorf("validated V2 cohort must retain at least one case")
	}
	baseline, err := readComparisonSummary("baseline", opts.BaselineRunDir)
	if err != nil {
		return V2CohortComparison{}, err
	}
	candidate, err := readComparisonSummary("candidate", opts.CandidateRunDir)
	if err != nil {
		return V2CohortComparison{}, err
	}
	if err := validateV2CohortRunSummary("baseline", baseline, cohort.FilteredSeedHash, cohort.RetainedCaseCount); err != nil {
		return V2CohortComparison{}, err
	}
	if err := validateV2CohortRunSummary("candidate", candidate, cohort.DerivedSeedHash, cohort.RetainedCaseCount); err != nil {
		return V2CohortComparison{}, err
	}

	comparison := V2CohortComparison{
		SchemaVersion:     v2CohortComparisonSchema,
		Status:            "passed",
		Cohort:            cohort,
		BaselineRunID:     baseline.RunID,
		CandidateRunID:    candidate.RunID,
		BaselineSeedHash:  baseline.SeedHash,
		CandidateSeedHash: candidate.SeedHash,
		RetainedCaseCount: cohort.RetainedCaseCount,
		Comparison:        comparisonForSummaries(baseline, candidate, ""),
	}
	outDir := opts.OutDir
	if strings.TrimSpace(outDir) == "" {
		outDir = opts.CandidateRunDir
	}
	if err := writeJSONFile(filepath.Join(outDir, "v2_cohort_comparison.json"), comparison); err != nil {
		return V2CohortComparison{}, err
	}
	return comparison, nil
}

func readComparisonSummary(label, runDir string) (Summary, error) {
	var summary Summary
	if err := readJSONFile(filepath.Join(runDir, "summary.json"), &summary); err != nil {
		return Summary{}, fmt.Errorf("read %s run summary: %w", label, err)
	}
	return summary, nil
}

func validateV2CohortRunSummary(label string, summary Summary, expectedSeedHash string, retainedCaseCount int) error {
	if summary.SeedHash != expectedSeedHash {
		return fmt.Errorf("%s run seed hash %q does not match validated cohort seed hash %q", label, summary.SeedHash, expectedSeedHash)
	}
	if summary.CaseCount != retainedCaseCount {
		return fmt.Errorf("%s run case count %d does not match validated cohort case count %d", label, summary.CaseCount, retainedCaseCount)
	}
	if summary.ScoredCaseCount != retainedCaseCount {
		return fmt.Errorf("%s run scored case count %d does not match validated cohort case count %d", label, summary.ScoredCaseCount, retainedCaseCount)
	}
	return nil
}
