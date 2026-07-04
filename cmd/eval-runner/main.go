package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

func main() {
	var opts evalharness.RunOptions
	var baselineRun string
	var candidateRun string
	flag.StringVar(&opts.Mode, "mode", "validate", "validate, import, baseline, candidate, or compare")
	flag.StringVar(&opts.SeedManifestPath, "seed", "tests/eval/seeds/public_rag_3axis_5k_v1/seed_manifest.json", "seed manifest path")
	flag.StringVar(&opts.SuitePath, "suite", "tests/eval/suites/public_rag_3axis_5k_v1.jsonl", "suite JSONL path")
	flag.StringVar(&opts.OutDir, "out", "", "output run directory")
	flag.StringVar(&opts.BaseURL, "base-url", env("DENSE_MEM_BASE_URL", "http://127.0.0.1:8080"), "Dense-Mem HTTP base URL")
	flag.StringVar(&opts.APIKey, "api-key", env("DENSE_MEM_API_KEY", ""), "read/write API key")
	flag.StringVar(&opts.ControlURL, "control-url", env("DENSE_MEM_CONTROL_URL", "http://127.0.0.1:8090"), "control portal base URL")
	flag.StringVar(&opts.ControlToken, "control-token", env("DENSE_MEM_CONTROL_TOKEN", ""), "control portal token")
	flag.BoolVar(&opts.ImportSeed, "import-seed", false, "import corpus through remember before running cases")
	flag.IntVar(&opts.ImportConcurrency, "import-concurrency", envInt("DENSE_MEM_EVAL_IMPORT_CONCURRENCY", 1), "maximum concurrent seed import requests")
	flag.BoolVar(&opts.DirectImport, "direct-import", envBool("DENSE_MEM_EVAL_DIRECT_IMPORT", false), "import fragment-only corpus directly into Neo4j with batched embeddings")
	flag.IntVar(&opts.DirectImportBatch, "direct-import-batch-size", envInt("DENSE_MEM_EVAL_DIRECT_IMPORT_BATCH_SIZE", 32), "maximum rows per direct-import embedding/write batch")
	flag.StringVar(&opts.DirectImportTeam, "direct-import-team-id", env("DENSE_MEM_EVAL_DIRECT_IMPORT_TEAM_ID", ""), "team id for direct Neo4j eval import")
	flag.StringVar(&opts.Neo4jURI, "neo4j-uri", env("DENSE_MEM_EVAL_NEO4J_URI", ""), "Neo4j URI override for direct eval import")
	flag.StringVar(&opts.Neo4jUser, "neo4j-user", env("DENSE_MEM_EVAL_NEO4J_USER", ""), "Neo4j user override for direct eval import")
	flag.StringVar(&opts.Neo4jPassword, "neo4j-password", env("DENSE_MEM_EVAL_NEO4J_PASSWORD", ""), "Neo4j password override for direct eval import")
	flag.StringVar(&opts.Neo4jDatabase, "neo4j-database", env("DENSE_MEM_EVAL_NEO4J_DATABASE", ""), "Neo4j database override for direct eval import")
	flag.StringVar(&opts.TracesPath, "traces", "", "offline recall_traces.jsonl path to score instead of running live")
	flag.StringVar(&opts.MappingPath, "mapping", "", "offline knowledge_mapping.json path to use with --traces")
	flag.IntVar(&opts.MaxPageSize, "max-page-size", 100, "evaluation export max page size")
	flag.StringVar(&opts.RunID, "run-id", "", "optional stable run id")
	flag.StringVar(&baselineRun, "baseline-run", "", "baseline run directory for compare mode")
	flag.StringVar(&candidateRun, "candidate-run", "", "candidate run directory for compare mode")
	registerFloatGate("min-recall-at-k", "minimum average recall@k required for scoring modes", &opts.Gates.MinRecallAtK, validateRate)
	registerFloatGate("min-required-rank1-rate", "minimum share of cases with a required ref ranked first", &opts.Gates.MinRequiredRank1Rate, validateRate)
	registerFloatGate("max-average-bad-at-k", "maximum average bad refs@k allowed for scoring modes", &opts.Gates.MaxAverageBadAtK, validateNonNegative)
	registerFloatGate("max-bad-rank1-rate", "maximum share of cases with a bad ref ranked first", &opts.Gates.MaxBadRank1Rate, validateRate)
	registerFloatGate("min-context-recall-at-k", "minimum average context recall@k required when context refs are present", &opts.Gates.MinContextRecallAtK, validateRate)
	registerFloatGate("min-context-required-rank1-rate", "minimum share of context-scored cases with a required ref first in context", &opts.Gates.MinContextRequiredRank1Rate, validateRate)
	registerFloatGate("max-average-context-bad-at-k", "maximum average bad context refs@k allowed when context refs are present", &opts.Gates.MaxAverageContextBadAtK, validateNonNegative)
	registerFloatGate("max-context-bad-rank1-rate", "maximum share of context-scored cases with a bad ref first in context", &opts.Gates.MaxContextBadRank1Rate, validateRate)
	registerFloatGate("min-evidence-recall-at-k", "minimum average evidence recall@k required when evidence qrels are present", &opts.Gates.MinEvidenceRecallAtK, validateRate)
	registerFloatGate("min-evidence-required-rank1-rate", "minimum share of evidence-scored cases with a required evidence ref first", &opts.Gates.MinEvidenceRequiredRank1Rate, validateRate)
	registerFloatGate("max-average-evidence-bad-at-k", "maximum average bad evidence refs@k allowed when evidence qrels are present", &opts.Gates.MaxAverageEvidenceBadAtK, validateNonNegative)
	registerFloatGate("max-evidence-bad-rank1-rate", "maximum share of evidence-scored cases with a bad evidence ref first", &opts.Gates.MaxEvidenceBadRank1Rate, validateRate)
	registerFloatGate("min-dream-recall-at-k", "minimum average dream recall@k required when dream qrels are present", &opts.Gates.MinDreamRecallAtK, validateRate)
	registerFloatGate("min-dream-required-rank1-rate", "minimum share of dream-scored cases with a required dream ref first", &opts.Gates.MinDreamRequiredRank1Rate, validateRate)
	registerFloatGate("max-average-dream-bad-at-k", "maximum average bad dream refs@k allowed when dream qrels are present", &opts.Gates.MaxAverageDreamBadAtK, validateNonNegative)
	registerFloatGate("max-dream-bad-rank1-rate", "maximum share of dream-scored cases with a bad dream ref first", &opts.Gates.MaxDreamBadRank1Rate, validateRate)
	flag.Parse()

	ctx := context.Background()
	if opts.Mode == "compare" {
		comparison, err := evalharness.CompareRunDirs(baselineRun, candidateRun, opts.OutDir)
		if err != nil {
			exitf("compare: %v", err)
		}
		msg := fmt.Sprintf("comparison written: recall_delta=%.4f mrr_delta=%.4f ndcg_delta=%.4f bad_at_k_delta=%.4f",
			comparison.RecallDelta, comparison.MRRDelta, comparison.NDCGDelta, comparison.BadAtKDelta)
		if comparison.ContextRecallDelta != 0 || comparison.ContextMRRDelta != 0 || comparison.ContextNDCGDelta != 0 || comparison.ContextBadAtKDelta != 0 {
			msg += fmt.Sprintf(" context_recall_delta=%.4f context_mrr_delta=%.4f context_ndcg_delta=%.4f context_bad_at_k_delta=%.4f",
				comparison.ContextRecallDelta,
				comparison.ContextMRRDelta,
				comparison.ContextNDCGDelta,
				comparison.ContextBadAtKDelta,
			)
		}
		if comparison.EvidenceRecallDelta != 0 || comparison.EvidenceMRRDelta != 0 || comparison.EvidenceNDCGDelta != 0 || comparison.EvidenceBadAtKDelta != 0 {
			msg += fmt.Sprintf(" evidence_recall_delta=%.4f evidence_mrr_delta=%.4f evidence_ndcg_delta=%.4f evidence_bad_at_k_delta=%.4f",
				comparison.EvidenceRecallDelta,
				comparison.EvidenceMRRDelta,
				comparison.EvidenceNDCGDelta,
				comparison.EvidenceBadAtKDelta,
			)
		}
		if comparison.DreamRecallDelta != 0 || comparison.DreamMRRDelta != 0 || comparison.DreamNDCGDelta != 0 || comparison.DreamBadAtKDelta != 0 {
			msg += fmt.Sprintf(" dream_recall_delta=%.4f dream_mrr_delta=%.4f dream_ndcg_delta=%.4f dream_bad_at_k_delta=%.4f",
				comparison.DreamRecallDelta,
				comparison.DreamMRRDelta,
				comparison.DreamNDCGDelta,
				comparison.DreamBadAtKDelta,
			)
		}
		fmt.Println(msg)
		return
	}
	if opts.OutDir == "" {
		opts.OutDir = "tests/eval/runs/" + time.Now().UTC().Format("20060102T150405Z") + "_" + opts.Mode
	}
	summary, err := evalharness.Run(ctx, opts)
	if err != nil {
		exitf("eval run: %v", err)
	}
	msg := fmt.Sprintf("run_id=%s mode=%s seed_hash=%s cases=%d scored=%d recall_at_k=%.4f mrr=%.4f ndcg_at_k=%.4f bad_at_k=%.4f required_rank1=%.4f bad_rank1=%.4f",
		summary.RunID,
		summary.Mode,
		summary.SeedHash,
		summary.CaseCount,
		summary.ScoredCaseCount,
		summary.AverageRecallAtK,
		summary.AverageMRR,
		summary.AverageNDCGAtK,
		summary.AverageBadAtK,
		summary.RequiredRank1Rate,
		summary.BadRank1Rate,
	)
	if summary.ContextScoredCaseCount > 0 {
		msg += fmt.Sprintf(" context_scored=%d context_recall_at_k=%.4f context_mrr=%.4f context_ndcg_at_k=%.4f context_bad_at_k=%.4f context_required_rank1=%.4f context_bad_rank1=%.4f",
			summary.ContextScoredCaseCount,
			summary.AverageContextRecallAtK,
			summary.AverageContextMRR,
			summary.AverageContextNDCGAtK,
			summary.AverageContextBadAtK,
			summary.ContextRequiredRank1Rate,
			summary.ContextBadRank1Rate,
		)
	}
	if summary.EvidenceScoredCaseCount > 0 {
		msg += fmt.Sprintf(" evidence_scored=%d evidence_recall_at_k=%.4f evidence_mrr=%.4f evidence_ndcg_at_k=%.4f evidence_bad_at_k=%.4f evidence_required_rank1=%.4f evidence_bad_rank1=%.4f",
			summary.EvidenceScoredCaseCount,
			summary.AverageEvidenceRecallAtK,
			summary.AverageEvidenceMRR,
			summary.AverageEvidenceNDCGAtK,
			summary.AverageEvidenceBadAtK,
			summary.EvidenceRequiredRank1Rate,
			summary.EvidenceBadRank1Rate,
		)
	}
	if summary.DreamScoredCaseCount > 0 {
		msg += fmt.Sprintf(" dream_scored=%d dream_recall_at_k=%.4f dream_mrr=%.4f dream_ndcg_at_k=%.4f dream_bad_at_k=%.4f dream_required_rank1=%.4f dream_bad_rank1=%.4f",
			summary.DreamScoredCaseCount,
			summary.AverageDreamRecallAtK,
			summary.AverageDreamMRR,
			summary.AverageDreamNDCGAtK,
			summary.AverageDreamBadAtK,
			summary.DreamRequiredRank1Rate,
			summary.DreamBadRank1Rate,
		)
	}
	fmt.Printf("%s out=%s\n", msg, opts.OutDir)
}

func registerFloatGate(name, usage string, target **float64, validate func(float64) error) {
	flag.Func(name, usage, func(raw string) error {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return err
		}
		if err := validate(value); err != nil {
			return err
		}
		*target = &value
		return nil
	})
}

func validateRate(value float64) error {
	if value < 0 || value > 1 {
		return fmt.Errorf("value must be between 0 and 1")
	}
	return nil
}

func validateNonNegative(value float64) error {
	if value < 0 {
		return fmt.Errorf("value must be non-negative")
	}
	return nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
