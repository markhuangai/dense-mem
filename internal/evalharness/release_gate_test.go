package evalharness

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedReleaseGatePolicyMatchesApprovedBaseline(t *testing.T) {
	policyPath := filepath.Join("..", "..", "tests", "eval", "baselines", "v2.1.1_public_6axis_1k_baseline.json")
	policy, err := LoadReleaseGatePolicy(policyPath)
	if err != nil {
		t.Fatalf("LoadReleaseGatePolicy: %v", err)
	}
	if policy.GateID != "v2.1.1-public-6axis-1k" {
		t.Fatalf("gate id = %q", policy.GateID)
	}
	if policy.SeedID != "public_6axis_1k_v1" {
		t.Fatalf("seed id = %q", policy.SeedID)
	}
	if policy.SeedHash != "sha256:ca1da9dccad1a84a281ca6e2e18582fba0882feb6b99f359c96f4355bd2cdf76" {
		t.Fatalf("seed hash = %q", policy.SeedHash)
	}
	if policy.SuiteHash != "sha256:14c1229aad134e732392e08f7113ec12b710489d96a80750e9060a161730ea75" {
		t.Fatalf("suite hash = %q", policy.SuiteHash)
	}
	if policy.BaselineSummary.CaseCount != 237 || policy.BaselineSummary.ScoredCaseCount != 237 {
		t.Fatalf("baseline counts = %+v", policy.BaselineSummary)
	}
	if policy.Minimums.AverageRecallAtK != policy.BaselineSummary.AverageRecallAtK ||
		policy.Minimums.AverageMRR != policy.BaselineSummary.AverageMRR ||
		policy.Minimums.AverageNDCGAtK != policy.BaselineSummary.AverageNDCGAtK ||
		policy.Minimums.RequiredRank1Rate != policy.BaselineSummary.RequiredRank1Rate {
		t.Fatalf("minimums do not match baseline summary: %+v vs %+v", policy.Minimums, policy.BaselineSummary)
	}
	if policy.Maximums.AverageBadAtK != 0 || policy.Maximums.BadRank1Rate != 0 || policy.Maximums.UnmappedSourceRefs != 0 {
		t.Fatalf("maximums = %+v; want zero bad and unmapped", policy.Maximums)
	}
}

func TestEvaluateReleaseGateRejectsDriftAndRegression(t *testing.T) {
	policy := releaseGateTestPolicy()
	passing := Summary{
		Mode:               "candidate",
		SeedID:             policy.SeedID,
		SeedHash:           policy.SeedHash,
		CaseCount:          policy.RequiredCaseCount,
		ScoredCaseCount:    policy.RequiredScoredCaseCount,
		AverageRecallAtK:   0.91,
		AverageMRR:         0.82,
		AverageNDCGAtK:     0.81,
		RequiredRank1Rate:  0.77,
		AverageBadAtK:      0,
		BadRank1Rate:       0,
		UnmappedSourceRefs: 0,
	}
	if result := EvaluateReleaseGate(passing, policy); !result.Passed {
		t.Fatalf("passing release gate = %+v", result)
	}

	tests := []struct {
		name string
		edit func(*Summary)
		want string
	}{
		{
			name: "metric regression",
			edit: func(summary *Summary) {
				summary.AverageRecallAtK = 0.89
			},
			want: "average_recall_at_k",
		},
		{
			name: "bad result",
			edit: func(summary *Summary) {
				summary.AverageBadAtK = 0.01
			},
			want: "average_bad_at_k",
		},
		{
			name: "unmapped reference",
			edit: func(summary *Summary) {
				summary.UnmappedSourceRefs = 1
			},
			want: "unmapped_source_refs",
		},
		{
			name: "seed drift",
			edit: func(summary *Summary) {
				summary.SeedHash = "sha256:drift"
			},
			want: "seed_hash",
		},
		{
			name: "incomplete scoring",
			edit: func(summary *Summary) {
				summary.ScoredCaseCount = 1
			},
			want: "scored_case_count",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			summary := passing
			tc.edit(&summary)
			result := EvaluateReleaseGate(summary, policy)
			if result.Passed {
				t.Fatalf("release gate passed for %s", tc.name)
			}
			if !strings.Contains(strings.Join(result.Failures, "\n"), tc.want) {
				t.Fatalf("failures = %v; want %q", result.Failures, tc.want)
			}
		})
	}
}

func TestEvaluateReleaseGateRejectsInvalidObservedMetrics(t *testing.T) {
	policy := releaseGateTestPolicy()
	passing := Summary{
		Mode:               "candidate",
		SeedID:             policy.SeedID,
		SeedHash:           policy.SeedHash,
		CaseCount:          policy.RequiredCaseCount,
		ScoredCaseCount:    policy.RequiredScoredCaseCount,
		AverageRecallAtK:   0.91,
		AverageMRR:         0.82,
		AverageNDCGAtK:     0.81,
		RequiredRank1Rate:  0.77,
		AverageBadAtK:      0,
		BadRank1Rate:       0,
		UnmappedSourceRefs: 0,
	}
	tests := []struct {
		name string
		edit func(*Summary)
		want string
	}{
		{
			name: "nan recall",
			edit: func(summary *Summary) { summary.AverageRecallAtK = math.NaN() },
			want: "average_recall_at_k must be a finite value between 0 and 1",
		},
		{
			name: "negative bad at k",
			edit: func(summary *Summary) { summary.AverageBadAtK = -1 },
			want: "average_bad_at_k must be a finite non-negative value",
		},
		{
			name: "infinite bad rank1",
			edit: func(summary *Summary) { summary.BadRank1Rate = math.Inf(1) },
			want: "bad_rank1_rate must be a finite value between 0 and 1",
		},
		{
			name: "negative unmapped source refs",
			edit: func(summary *Summary) { summary.UnmappedSourceRefs = -1 },
			want: "unmapped_source_refs must be non-negative",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			summary := passing
			tc.edit(&summary)

			result := EvaluateReleaseGate(summary, policy)

			if result.Passed {
				t.Fatal("release gate passed with invalid observed metric")
			}
			if !strings.Contains(strings.Join(result.Failures, "\n"), tc.want) {
				t.Fatalf("failures = %v; want %q", result.Failures, tc.want)
			}
		})
	}
}

func TestReleaseGatePolicyValidation(t *testing.T) {
	if _, err := LoadReleaseGatePolicy(filepath.Join("testdata", "missing.json")); err == nil {
		t.Fatal("LoadReleaseGatePolicy missing file error = nil")
	}
	policy := releaseGateTestPolicy()
	policy.SchemaVersion = "wrong"
	if err := validateReleaseGatePolicy(policy); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("wrong schema validation err = %v", err)
	}
	policy = releaseGateTestPolicy()
	policy.SuiteHash = ""
	if err := validateReleaseGatePolicy(policy); err == nil || !strings.Contains(err.Error(), "suite_hash") {
		t.Fatalf("missing suite hash validation err = %v", err)
	}
	policy = releaseGateTestPolicy()
	policy.BaselineSummary.ScoredCaseCount = 1
	if err := validateReleaseGatePolicy(policy); err == nil || !strings.Contains(err.Error(), "baseline scored_case_count") {
		t.Fatalf("baseline count validation err = %v", err)
	}
	policy = releaseGateTestPolicy()
	policy.RequiredScoredCaseCount = policy.RequiredCaseCount - 1
	if err := validateReleaseGatePolicy(policy); err == nil || !strings.Contains(err.Error(), "must equal case count") {
		t.Fatalf("equal count validation err = %v", err)
	}
}

func TestReleaseGatePolicyRequiresBaselineStrengthThresholds(t *testing.T) {
	policy := releaseGateTestPolicy()
	if err := validateReleaseGatePolicy(policy); err != nil {
		t.Fatalf("baseline policy validation: %v", err)
	}

	stronger := releaseGateTestPolicy()
	stronger.Minimums.AverageRecallAtK = 0.91
	if err := validateReleaseGatePolicy(stronger); err != nil {
		t.Fatalf("stronger minimum rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*ReleaseGatePolicy)
		want string
	}{
		{
			name: "omitted minimum decodes weaker than baseline",
			edit: func(policy *ReleaseGatePolicy) {
				policy.Minimums.AverageMRR = 0
			},
			want: "average_mrr minimum",
		},
		{
			name: "weaker minimum",
			edit: func(policy *ReleaseGatePolicy) {
				policy.Minimums.AverageRecallAtK = 0.89
			},
			want: "average_recall_at_k minimum",
		},
		{
			name: "non finite minimum",
			edit: func(policy *ReleaseGatePolicy) {
				policy.Minimums.AverageNDCGAtK = math.NaN()
			},
			want: "average_ndcg_at_k minimum",
		},
		{
			name: "out of range minimum",
			edit: func(policy *ReleaseGatePolicy) {
				policy.Minimums.RequiredRank1Rate = 1.01
			},
			want: "required_rank1_rate minimum",
		},
		{
			name: "weaker maximum",
			edit: func(policy *ReleaseGatePolicy) {
				policy.BaselineSummary.AverageBadAtK = 0.01
				policy.Maximums.AverageBadAtK = 0.02
			},
			want: "average_bad_at_k maximum",
		},
		{
			name: "weaker unmapped maximum",
			edit: func(policy *ReleaseGatePolicy) {
				policy.BaselineSummary.UnmappedSourceRefs = 1
				policy.Maximums.UnmappedSourceRefs = 2
			},
			want: "unmapped_source_refs maximum",
		},
		{
			name: "non finite average bad maximum",
			edit: func(policy *ReleaseGatePolicy) {
				policy.BaselineSummary.AverageBadAtK = 2
				policy.Maximums.AverageBadAtK = math.Inf(1)
			},
			want: "average_bad_at_k maximum",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := releaseGateTestPolicy()
			tc.edit(&policy)
			err := validateReleaseGatePolicy(policy)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateReleaseGatePolicy err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReleaseGateAverageBadAtKAllowsCountsAboveOne(t *testing.T) {
	policy := releaseGateTestPolicy()
	policy.BaselineSummary.AverageBadAtK = 2
	policy.Maximums.AverageBadAtK = 2
	if err := validateReleaseGatePolicy(policy); err != nil {
		t.Fatalf("validateReleaseGatePolicy rejected count-like bad@k: %v", err)
	}

	summary := Summary{
		Mode:               "candidate",
		SeedID:             policy.SeedID,
		SeedHash:           policy.SeedHash,
		CaseCount:          policy.RequiredCaseCount,
		ScoredCaseCount:    policy.RequiredScoredCaseCount,
		AverageRecallAtK:   policy.Minimums.AverageRecallAtK,
		AverageMRR:         policy.Minimums.AverageMRR,
		AverageNDCGAtK:     policy.Minimums.AverageNDCGAtK,
		RequiredRank1Rate:  policy.Minimums.RequiredRank1Rate,
		AverageBadAtK:      1.5,
		BadRank1Rate:       0,
		UnmappedSourceRefs: 0,
	}
	if result := EvaluateReleaseGate(summary, policy); !result.Passed {
		t.Fatalf("release gate rejected valid average_bad_at_k count: %+v", result)
	}
}

func TestValidateReleaseGatePolicyForRun(t *testing.T) {
	policy := releaseGateTestPolicy()
	manifest := SeedManifest{SeedID: policy.SeedID}
	if err := validateReleaseGatePolicyForRun(policy, manifest, policy.SeedHash, policy.SuiteHash, policy.RequiredCaseCount); err != nil {
		t.Fatalf("validateReleaseGatePolicyForRun: %v", err)
	}

	cases := []struct {
		name      string
		edit      func(*ReleaseGatePolicy)
		seedHash  string
		suiteHash string
		n         int
		want      string
	}{
		{
			name:      "seed id",
			edit:      func(policy *ReleaseGatePolicy) { policy.SeedID = "other" },
			seedHash:  policy.SeedHash,
			suiteHash: policy.SuiteHash,
			n:         policy.RequiredCaseCount,
			want:      "seed_id",
		},
		{
			name:      "seed hash",
			edit:      func(policy *ReleaseGatePolicy) {},
			seedHash:  "sha256:other",
			suiteHash: policy.SuiteHash,
			n:         policy.RequiredCaseCount,
			want:      "seed_hash",
		},
		{
			name:      "suite hash",
			edit:      func(policy *ReleaseGatePolicy) {},
			seedHash:  policy.SeedHash,
			suiteHash: "sha256:other",
			n:         policy.RequiredCaseCount,
			want:      "suite_hash",
		},
		{
			name:      "case count",
			edit:      func(policy *ReleaseGatePolicy) {},
			seedHash:  policy.SeedHash,
			suiteHash: policy.SuiteHash,
			n:         policy.RequiredCaseCount + 1,
			want:      "case count",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := releaseGateTestPolicy()
			tc.edit(&policy)
			err := validateReleaseGatePolicyForRun(policy, manifest, tc.seedHash, tc.suiteHash, tc.n)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateReleaseGatePolicyForRun err = %v; want %q", err, tc.want)
			}
		})
	}
}

func releaseGateTestPolicy() ReleaseGatePolicy {
	return ReleaseGatePolicy{
		SchemaVersion:           ReleaseGatePolicySchemaVersion,
		GateID:                  "test-gate",
		Release:                 "test",
		SeedID:                  "seed",
		SeedHash:                "sha256:seed",
		SuiteHash:               "sha256:suite",
		RequiredCaseCount:       2,
		RequiredScoredCaseCount: 2,
		BaselineSummary: ReleaseGateBaseline{
			CaseCount:          2,
			ScoredCaseCount:    2,
			AverageRecallAtK:   0.9,
			AverageMRR:         0.8,
			AverageNDCGAtK:     0.8,
			RequiredRank1Rate:  0.75,
			AverageBadAtK:      0,
			BadRank1Rate:       0,
			UnmappedSourceRefs: 0,
		},
		Minimums: ReleaseGateMetricMinimums{
			AverageRecallAtK:  0.9,
			AverageMRR:        0.8,
			AverageNDCGAtK:    0.8,
			RequiredRank1Rate: 0.75,
		},
		Maximums: ReleaseGateMetricMaximums{
			AverageBadAtK:      0,
			BadRank1Rate:       0,
			UnmappedSourceRefs: 0,
		},
	}
}
