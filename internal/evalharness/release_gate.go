package evalharness

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const ReleaseGatePolicySchemaVersion = "dense-mem.eval.release_gate.v1"

func LoadReleaseGatePolicy(path string) (ReleaseGatePolicy, error) {
	var policy ReleaseGatePolicy
	if err := readJSONFile(path, &policy); err != nil {
		return ReleaseGatePolicy{}, err
	}
	if err := validateReleaseGatePolicy(policy); err != nil {
		return ReleaseGatePolicy{}, err
	}
	return policy, nil
}

func EvaluateReleaseGate(summary Summary, policy ReleaseGatePolicy) ReleaseGateResult {
	result := ReleaseGateResult{
		Passed:   true,
		GateID:   policy.GateID,
		Release:  policy.Release,
		SeedID:   summary.SeedID,
		SeedHash: summary.SeedHash,
		Metrics: map[string]float64{
			"case_count":           float64(summary.CaseCount),
			"scored_case_count":    float64(summary.ScoredCaseCount),
			"average_recall_at_k":  summary.AverageRecallAtK,
			"average_mrr":          summary.AverageMRR,
			"average_ndcg_at_k":    summary.AverageNDCGAtK,
			"required_rank1_rate":  summary.RequiredRank1Rate,
			"average_bad_at_k":     summary.AverageBadAtK,
			"bad_rank1_rate":       summary.BadRank1Rate,
			"unmapped_source_refs": float64(summary.UnmappedSourceRefs),
		},
		Minimums: map[string]float64{
			"average_recall_at_k": policy.Minimums.AverageRecallAtK,
			"average_mrr":         policy.Minimums.AverageMRR,
			"average_ndcg_at_k":   policy.Minimums.AverageNDCGAtK,
			"required_rank1_rate": policy.Minimums.RequiredRank1Rate,
		},
		Maximums: map[string]float64{
			"average_bad_at_k":     policy.Maximums.AverageBadAtK,
			"bad_rank1_rate":       policy.Maximums.BadRank1Rate,
			"unmapped_source_refs": float64(policy.Maximums.UnmappedSourceRefs),
		},
		CreatedAt: time.Now().UTC(),
	}
	fail := func(format string, args ...any) {
		result.Passed = false
		result.Failures = append(result.Failures, fmt.Sprintf(format, args...))
	}
	if summary.SeedID != policy.SeedID {
		fail("seed_id %q does not match policy seed_id %q", summary.SeedID, policy.SeedID)
	}
	if summary.SeedHash != policy.SeedHash {
		fail("seed_hash %q does not match policy seed_hash %q", summary.SeedHash, policy.SeedHash)
	}
	if summary.CaseCount != policy.RequiredCaseCount {
		fail("case_count %d does not match required %d", summary.CaseCount, policy.RequiredCaseCount)
	}
	if summary.ScoredCaseCount != policy.RequiredScoredCaseCount {
		fail("scored_case_count %d does not match required %d", summary.ScoredCaseCount, policy.RequiredScoredCaseCount)
	}
	if summary.ScoredCaseCount != summary.CaseCount {
		fail("incomplete scoring: scored_case_count %d does not match case_count %d", summary.ScoredCaseCount, summary.CaseCount)
	}
	checkObservedRate := func(name string, value float64) bool {
		if !isReleaseGateRate(value) {
			fail("%s must be a finite value between 0 and 1", name)
			return false
		}
		return true
	}
	checkObservedNonNegative := func(name string, value float64) bool {
		if !isReleaseGateFiniteNonNegative(value) {
			fail("%s must be a finite non-negative value", name)
			return false
		}
		return true
	}
	checkMin := func(name string, value, threshold float64) {
		if value < threshold {
			fail("%s %.10f below minimum %.10f", name, value, threshold)
		}
	}
	checkMax := func(name string, value, threshold float64) {
		if value > threshold {
			fail("%s %.10f above maximum %.10f", name, value, threshold)
		}
	}
	if checkObservedRate("average_recall_at_k", summary.AverageRecallAtK) {
		checkMin("average_recall_at_k", summary.AverageRecallAtK, policy.Minimums.AverageRecallAtK)
	}
	if checkObservedRate("average_mrr", summary.AverageMRR) {
		checkMin("average_mrr", summary.AverageMRR, policy.Minimums.AverageMRR)
	}
	if checkObservedRate("average_ndcg_at_k", summary.AverageNDCGAtK) {
		checkMin("average_ndcg_at_k", summary.AverageNDCGAtK, policy.Minimums.AverageNDCGAtK)
	}
	if checkObservedRate("required_rank1_rate", summary.RequiredRank1Rate) {
		checkMin("required_rank1_rate", summary.RequiredRank1Rate, policy.Minimums.RequiredRank1Rate)
	}
	if checkObservedNonNegative("average_bad_at_k", summary.AverageBadAtK) {
		checkMax("average_bad_at_k", summary.AverageBadAtK, policy.Maximums.AverageBadAtK)
	}
	if checkObservedRate("bad_rank1_rate", summary.BadRank1Rate) {
		checkMax("bad_rank1_rate", summary.BadRank1Rate, policy.Maximums.BadRank1Rate)
	}
	if summary.UnmappedSourceRefs > policy.Maximums.UnmappedSourceRefs {
		fail("unmapped_source_refs %d above maximum %d", summary.UnmappedSourceRefs, policy.Maximums.UnmappedSourceRefs)
	}
	return result
}

func validateReleaseGatePolicy(policy ReleaseGatePolicy) error {
	if policy.SchemaVersion != ReleaseGatePolicySchemaVersion {
		return fmt.Errorf("release gate schema_version %q is unsupported", policy.SchemaVersion)
	}
	if strings.TrimSpace(policy.GateID) == "" {
		return fmt.Errorf("release gate policy missing gate_id")
	}
	if strings.TrimSpace(policy.Release) == "" {
		return fmt.Errorf("release gate policy missing release")
	}
	if strings.TrimSpace(policy.SeedID) == "" {
		return fmt.Errorf("release gate policy missing seed_id")
	}
	if strings.TrimSpace(policy.SeedHash) == "" {
		return fmt.Errorf("release gate policy missing seed_hash")
	}
	if strings.TrimSpace(policy.SuiteHash) == "" {
		return fmt.Errorf("release gate policy missing suite_hash")
	}
	if policy.RequiredCaseCount <= 0 || policy.RequiredScoredCaseCount <= 0 {
		return fmt.Errorf("release gate policy requires positive case and scored counts")
	}
	if policy.RequiredScoredCaseCount != policy.RequiredCaseCount {
		return fmt.Errorf(
			"release gate policy scored count %d must equal case count %d",
			policy.RequiredScoredCaseCount,
			policy.RequiredCaseCount,
		)
	}
	if policy.BaselineSummary.CaseCount != policy.RequiredCaseCount {
		return fmt.Errorf("baseline case_count %d does not match required %d", policy.BaselineSummary.CaseCount, policy.RequiredCaseCount)
	}
	if policy.BaselineSummary.ScoredCaseCount != policy.RequiredScoredCaseCount {
		return fmt.Errorf("baseline scored_case_count %d does not match required %d", policy.BaselineSummary.ScoredCaseCount, policy.RequiredScoredCaseCount)
	}
	if err := validateReleaseGateMinimum("average_recall_at_k", policy.Minimums.AverageRecallAtK, policy.BaselineSummary.AverageRecallAtK); err != nil {
		return err
	}
	if err := validateReleaseGateMinimum("average_mrr", policy.Minimums.AverageMRR, policy.BaselineSummary.AverageMRR); err != nil {
		return err
	}
	if err := validateReleaseGateMinimum("average_ndcg_at_k", policy.Minimums.AverageNDCGAtK, policy.BaselineSummary.AverageNDCGAtK); err != nil {
		return err
	}
	if err := validateReleaseGateMinimum("required_rank1_rate", policy.Minimums.RequiredRank1Rate, policy.BaselineSummary.RequiredRank1Rate); err != nil {
		return err
	}
	if err := validateReleaseGateNonNegativeMaximum("average_bad_at_k", policy.Maximums.AverageBadAtK, policy.BaselineSummary.AverageBadAtK); err != nil {
		return err
	}
	if err := validateReleaseGateMaximum("bad_rank1_rate", policy.Maximums.BadRank1Rate, policy.BaselineSummary.BadRank1Rate); err != nil {
		return err
	}
	if policy.Maximums.UnmappedSourceRefs < 0 {
		return fmt.Errorf("unmapped_source_refs maximum must be non-negative")
	}
	if policy.Maximums.UnmappedSourceRefs > policy.BaselineSummary.UnmappedSourceRefs {
		return fmt.Errorf(
			"unmapped_source_refs maximum %d is weaker than baseline %d",
			policy.Maximums.UnmappedSourceRefs,
			policy.BaselineSummary.UnmappedSourceRefs,
		)
	}
	return nil
}

func validateReleaseGatePolicyForRun(policy ReleaseGatePolicy, manifest SeedManifest, seedHash string, suiteHash string, caseCount int) error {
	if policy.SeedID != manifest.SeedID {
		return fmt.Errorf("release gate policy seed_id %q does not match manifest seed_id %q", policy.SeedID, manifest.SeedID)
	}
	if policy.SeedHash != seedHash {
		return fmt.Errorf("release gate policy seed_hash %q does not match seed hash %q", policy.SeedHash, seedHash)
	}
	if policy.SuiteHash != suiteHash {
		return fmt.Errorf("release gate policy suite_hash %q does not match suite hash %q", policy.SuiteHash, suiteHash)
	}
	if policy.RequiredCaseCount != caseCount {
		return fmt.Errorf("release gate policy case count %d does not match suite count %d", policy.RequiredCaseCount, caseCount)
	}
	if policy.RequiredScoredCaseCount != caseCount {
		return fmt.Errorf("release gate policy scored count %d does not match suite count %d", policy.RequiredScoredCaseCount, caseCount)
	}
	return nil
}

func validateReleaseGateMinimum(name string, threshold float64, baseline float64) error {
	if !isReleaseGateRate(threshold) {
		return fmt.Errorf("%s minimum must be a finite value between 0 and 1", name)
	}
	if !isReleaseGateRate(baseline) {
		return fmt.Errorf("baseline %s must be a finite value between 0 and 1", name)
	}
	if threshold < baseline {
		return fmt.Errorf("%s minimum %.10f is weaker than baseline %.10f", name, threshold, baseline)
	}
	return nil
}

func validateReleaseGateMaximum(name string, threshold float64, baseline float64) error {
	if !isReleaseGateRate(threshold) {
		return fmt.Errorf("%s maximum must be a finite value between 0 and 1", name)
	}
	if !isReleaseGateRate(baseline) {
		return fmt.Errorf("baseline %s must be a finite value between 0 and 1", name)
	}
	if threshold > baseline {
		return fmt.Errorf("%s maximum %.10f is weaker than baseline %.10f", name, threshold, baseline)
	}
	return nil
}

func validateReleaseGateNonNegativeMaximum(name string, threshold float64, baseline float64) error {
	if !isReleaseGateFiniteNonNegative(threshold) {
		return fmt.Errorf("%s maximum must be a finite non-negative value", name)
	}
	if !isReleaseGateFiniteNonNegative(baseline) {
		return fmt.Errorf("baseline %s must be a finite non-negative value", name)
	}
	if threshold > baseline {
		return fmt.Errorf("%s maximum %.10f is weaker than baseline %.10f", name, threshold, baseline)
	}
	return nil
}

func isReleaseGateRate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func isReleaseGateFiniteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
