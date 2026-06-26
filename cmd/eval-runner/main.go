package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

func main() {
	var opts evalharness.RunOptions
	var baselineRun string
	var candidateRun string
	flag.StringVar(&opts.Mode, "mode", "validate", "validate, baseline, candidate, or compare")
	flag.StringVar(&opts.SeedManifestPath, "seed", "tests/eval/seeds/local_pr_v1/seed_manifest.json", "seed manifest path")
	flag.StringVar(&opts.SuitePath, "suite", "tests/eval/suites/pr.jsonl", "suite JSONL path")
	flag.StringVar(&opts.OutDir, "out", "", "output run directory")
	flag.StringVar(&opts.BaseURL, "base-url", env("DENSE_MEM_BASE_URL", "http://127.0.0.1:8080"), "Dense-Mem HTTP base URL")
	flag.StringVar(&opts.APIKey, "api-key", env("DENSE_MEM_API_KEY", ""), "read/write API key")
	flag.StringVar(&opts.ControlURL, "control-url", env("DENSE_MEM_CONTROL_URL", "http://127.0.0.1:8090"), "control portal base URL")
	flag.StringVar(&opts.ControlToken, "control-token", env("DENSE_MEM_CONTROL_TOKEN", ""), "control portal token")
	flag.BoolVar(&opts.ImportSeed, "import-seed", false, "import corpus through remember before running cases")
	flag.StringVar(&opts.TracesPath, "traces", "", "offline recall_traces.jsonl path to score instead of running live")
	flag.IntVar(&opts.MaxPageSize, "max-page-size", 100, "evaluation export max page size")
	flag.StringVar(&opts.RunID, "run-id", "", "optional stable run id")
	flag.StringVar(&baselineRun, "baseline-run", "", "baseline run directory for compare mode")
	flag.StringVar(&candidateRun, "candidate-run", "", "candidate run directory for compare mode")
	flag.Parse()

	ctx := context.Background()
	if opts.Mode == "compare" {
		comparison, err := evalharness.CompareRunDirs(baselineRun, candidateRun, opts.OutDir)
		if err != nil {
			exitf("compare: %v", err)
		}
		fmt.Printf("comparison written: recall_delta=%.4f mrr_delta=%.4f ndcg_delta=%.4f bad_at_k_delta=%.4f\n",
			comparison.RecallDelta, comparison.MRRDelta, comparison.NDCGDelta, comparison.BadAtKDelta)
		return
	}
	if opts.OutDir == "" {
		opts.OutDir = "tests/eval/runs/" + time.Now().UTC().Format("20060102T150405Z") + "_" + opts.Mode
	}
	summary, err := evalharness.Run(ctx, opts)
	if err != nil {
		exitf("eval run: %v", err)
	}
	fmt.Printf("run_id=%s mode=%s seed_hash=%s cases=%d scored=%d recall_at_k=%.4f mrr=%.4f ndcg_at_k=%.4f bad_at_k=%.4f out=%s\n",
		summary.RunID,
		summary.Mode,
		summary.SeedHash,
		summary.CaseCount,
		summary.ScoredCaseCount,
		summary.AverageRecallAtK,
		summary.AverageMRR,
		summary.AverageNDCGAtK,
		summary.AverageBadAtK,
		opts.OutDir,
	)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
