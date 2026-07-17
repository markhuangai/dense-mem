package evalharness

import (
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
	if policy.SeedHash != "sha256:eb09124331228e59898a93740104ab978b9974e3ebf7f7fc2e09728ef95b3d78" {
		t.Fatalf("seed hash = %q", policy.SeedHash)
	}
	if policy.BaselineSummary.CaseCount != 206 || policy.BaselineSummary.ScoredCaseCount != 206 {
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
	policy.BaselineSummary.ScoredCaseCount = 1
	if err := validateReleaseGatePolicy(policy); err == nil || !strings.Contains(err.Error(), "baseline scored_case_count") {
		t.Fatalf("baseline count validation err = %v", err)
	}
}

func releaseGateTestPolicy() ReleaseGatePolicy {
	return ReleaseGatePolicy{
		SchemaVersion:           ReleaseGatePolicySchemaVersion,
		GateID:                  "test-gate",
		Release:                 "test",
		SeedID:                  "seed",
		SeedHash:                "sha256:seed",
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
