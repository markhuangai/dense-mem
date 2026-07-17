package evalharness

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunValidateChecksReleaseGateInputBeforeLiveWork(t *testing.T) {
	dir := writeEvalFixture(t)
	manifestPath := filepath.Join(dir, "seed_manifest.json")
	suitePath := filepath.Join(dir, "suite.jsonl")
	_, _, _, _, _, _, seedHash, err := loadRunInputs(manifestPath, suitePath)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	policy := releaseGateTestPolicy()
	policy.SeedID = "fixture"
	policy.SeedHash = seedHash
	policyPath := filepath.Join(dir, "release_gate.json")
	if err := writeJSONFile(policyPath, policy); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	out := filepath.Join(dir, "validated")
	if _, err := Run(context.Background(), RunOptions{
		Mode:                  "validate",
		SeedManifestPath:      manifestPath,
		SuitePath:             suitePath,
		ReleaseGatePolicyPath: policyPath,
	}); err == nil || !strings.Contains(err.Error(), "requires MCP tool transport") {
		t.Fatalf("Run release gate over REST err = %v", err)
	}

	if _, err := Run(context.Background(), RunOptions{
		Mode:                  "validate",
		SeedManifestPath:      manifestPath,
		SuitePath:             suitePath,
		OutDir:                out,
		ReleaseGatePolicyPath: policyPath,
		ToolTransport:         "mcp",
	}); err != nil {
		t.Fatalf("Run validate with approved input: %v", err)
	}
	var inputResult ReleaseGateInputResult
	if err := readJSONFile(filepath.Join(out, "release_gate_input_result.json"), &inputResult); err != nil {
		t.Fatalf("read input result: %v", err)
	}
	if !inputResult.Passed {
		t.Fatalf("release gate input result = %+v", inputResult)
	}
	var runConfig RunConfig
	if err := readJSONFile(filepath.Join(out, "run_config.json"), &runConfig); err != nil {
		t.Fatalf("read run config: %v", err)
	}
	if runConfig.ReleaseGatePolicyHash == "" || runConfig.ToolTransport != "mcp" || runConfig.ToolContract != "mcp.tools/call.v1" {
		t.Fatalf("release gate run config = %+v", runConfig)
	}

	policy.SeedHash = "sha256:unapproved"
	if err := writeJSONFile(policyPath, policy); err != nil {
		t.Fatalf("rewrite policy: %v", err)
	}
	_, err = Run(context.Background(), RunOptions{
		Mode:                  "validate",
		SeedManifestPath:      manifestPath,
		SuitePath:             suitePath,
		OutDir:                filepath.Join(dir, "rejected"),
		ReleaseGatePolicyPath: policyPath,
		ToolTransport:         "mcp",
	})
	if err == nil || !strings.Contains(err.Error(), "release gate input check failed") || !strings.Contains(err.Error(), "seed_hash") {
		t.Fatalf("Run validate unapproved seed err = %v", err)
	}
}

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

func TestEvaluateReleaseGateInputRejectsUnapprovedSeed(t *testing.T) {
	policy := releaseGateTestPolicy()
	passing := Summary{
		SeedID:    policy.SeedID,
		SeedHash:  policy.SeedHash,
		CaseCount: policy.RequiredCaseCount,
	}
	if result := EvaluateReleaseGateInput(passing, policy); !result.Passed {
		t.Fatalf("passing release gate input = %+v", result)
	}

	tests := []struct {
		name string
		edit func(*Summary)
		want string
	}{
		{name: "seed id", edit: func(summary *Summary) { summary.SeedID = "other" }, want: "seed_id"},
		{name: "seed hash", edit: func(summary *Summary) { summary.SeedHash = "sha256:other" }, want: "seed_hash"},
		{name: "case count", edit: func(summary *Summary) { summary.CaseCount++ }, want: "case_count"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			summary := passing
			tc.edit(&summary)
			result := EvaluateReleaseGateInput(summary, policy)
			if result.Passed || !strings.Contains(strings.Join(result.Failures, "\n"), tc.want) {
				t.Fatalf("release gate input = %+v; want %q failure", result, tc.want)
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
