package evalharness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

type PreparationReceipt struct {
	DatabaseWork PreparationWork `json:"database_work"`
	PreparedTopics
	Dataset       string `json:"dataset"`
	SeedImage     string `json:"prepared_seed_image"`
	CanonicalHash string `json:"canonical_sha256"`
	SearchHash    string `json:"search_sha256"`
	MappingHash   string `json:"mapping_sha256"`
	RegistryHash  string `json:"registry_sha256"`
	CorpusRows    int    `json:"corpus_rows"`
}

func PairedMeasurement(ctx context.Context, manifest Manifest, dataset Dataset, corpus []CorpusItem, questions []Question, client *HTTPClient, transport modelprovider.StructuredTransport, native *Native) error {
	work, stop := context.WithDeadline(ctx, manifest.ModelDeadline)
	defer stop()
	seed, mapping, canonical, initial, err := VerifySeed(work, manifest, corpus, client, native)
	if err != nil {
		return err
	}
	if seed.PreparationWork == nil {
		return errors.New("missing prepared database work measurement")
	}
	receipt := PreparationReceipt{DatabaseWork: *seed.PreparationWork, PreparedTopics: *seed.PreparedTopics, Dataset: manifest.Dataset, SeedImage: manifest.SeedImage, CanonicalHash: seed.CanonicalHash, SearchHash: seed.SearchHash, MappingHash: seed.MappingHash, RegistryHash: seed.RegistryHash, CorpusRows: len(corpus)}
	if err := os.MkdirAll(manifest.OutputDir, 0700); err != nil {
		return err
	}
	if manifest.PairPhase == "verify" {
		return writeJSON(filepath.Join(manifest.OutputDir, "preparation.json"), receipt)
	}
	var expected PreparationReceipt
	if err := readJSON(os.Getenv("EVAL_EXPECTED_PREPARATION"), &expected); err != nil {
		return err
	}
	if expected != receipt {
		return errors.New("restored database differs from the ten-arm readiness gate")
	}
	switch manifest.PairPhase {
	case "counterevidence":
		grouping, err := measureTopicGrouping(work, "tests/eval/source_locks/topic_grouping_hotpotqa.json", dataset.Corpus.SHA256, corpus, mapping, native)
		if err != nil {
			return err
		}
		before, err := readApplicationPool(work, native)
		if err != nil {
			return err
		}
		measurement, err := MeasureCounterevidence(work, "tests/eval/source_locks/climate_fever.json", client, native)
		after, poolErr := readApplicationPool(work, native)
		pool, intervalErr := compareApplicationPool(before, after)
		err = errors.Join(err, poolErr, intervalErr)
		if logsErr := capturePairLogs(ctx, native); logsErr != nil {
			err = errors.Join(err, logsErr)
		}
		return errors.Join(err, writeJSON(filepath.Join(manifest.OutputDir, "counterevidence.json"), map[string]any{"identity": receipt, "measurement": measurement, "topic_grouping": grouping, "application_pool_waits": pool, "complete": err == nil, "server_usage": native.ServerUsage, "measured_cost_usd": nil}))
	case "lexical":
		if manifest.PairArm != "candidate" {
			return errors.New("query-embedding ablation requires the candidate image")
		}
		type score struct {
			Question  string  `json:"question"`
			Recall    float64 `json:"recall_at_10"`
			ElapsedMS float64 `json:"elapsed_ms"`
		}
		scores := make([]score, 0, len(questions))
		for _, q := range questions {
			call := memoryCall(work, client, "recall_memory", map[string]any{"query": q.Query, "limit": 10, "topic_limit": 3})
			if err := validateInitial(call); err != nil {
				return err
			}
			gold := make(map[string]bool, len(q.GoldSources))
			for _, id := range q.GoldSources {
				gold[id] = true
			}
			if len(gold) == 0 {
				return errors.New("lexical arm missing gold references")
			}
			scores = append(scores, score{q.ID, initialSourceRecall(call, canonical, gold, 10), call.ElapsedMS})
		}
		if err := capturePairLogs(ctx, native); err != nil {
			return err
		}
		if native.ServerUsage["embedding"].Attempts != 0 {
			return errors.New("query-embedding ablation issued an embedding request")
		}
		return writeJSON(filepath.Join(manifest.OutputDir, "lexical.json"), map[string]any{"identity": receipt, "expected": len(questions), "scores": scores, "capture_complete": true, "query_embedding_attempts": native.ServerUsage["embedding"].Attempts, "server_usage": native.ServerUsage, "measured_cost_usd": nil})
	case "score":
		shard := make([]Question, 0)
		for i, q := range questions {
			if i%manifest.PairShards == manifest.PairShard {
				shard = append(shard, q)
			}
		}
		report, err := run(ctx, manifest, corpus, shard, client, transport, native, &verifiedSeed{seed, mapping, canonical, initial})
		if err != nil {
			return err
		}
		if !report.Complete {
			return errors.New("paired answer shard is incomplete")
		}
		return nil
	default:
		return errors.New("unknown paired evaluation phase")
	}
}

type questionCheckpoint struct {
	Fingerprint string      `json:"fingerprint"`
	Question    string      `json:"question"`
	RunID       string      `json:"run_id"`
	Scores      []CaseScore `json:"scores"`
}

func checkpointFile(dir, question string) string {
	id := sha256.Sum256([]byte(question))
	return filepath.Join(dir, hex.EncodeToString(id[:])+".json")
}

func checkpointFingerprint(m Manifest) string {
	value, _ := json.Marshal([]any{"paired-public-rag-v2", m.Dataset, m.RegistryHash, m.PairArm, m.Image, m.SourceRevision, m.SeedImage, m.ParentSeedImage, m.Model, m.ContextWindowTokens, m.EmbeddingModel, m.EmbeddingDimensions, m.PairShard, m.PairShards})
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func restoredQuestion(manifest Manifest, question string) ([]CaseScore, error) {
	dir := os.Getenv("EVAL_RESUME_QUESTIONS")
	if manifest.PairArm == "" || dir == "" {
		return nil, nil
	}
	var sealed questionCheckpoint
	err := readJSON(checkpointFile(dir, question), &sealed)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sealed.Fingerprint != checkpointFingerprint(manifest) || sealed.Question != question || sealed.RunID == "" || sealed.RunID != os.Getenv("EVAL_RESUME_RUN") || len(sealed.Scores) != 2 {
		return nil, errors.New("question checkpoint differs from frozen evaluation identity")
	}
	policies := map[string]bool{}
	for _, s := range sealed.Scores {
		if s.QuestionID != question || s.Condition != "on" || !s.Completed || (s.Policy != "single" && s.Policy != "multi_turn") || policies[s.Policy] {
			return nil, errors.New("question checkpoint has incomplete or inconsistent answers")
		}
		policies[s.Policy] = true
	}
	return sealed.Scores, nil
}

func checkpointQuestion(manifest Manifest, question string, scores []CaseScore) error {
	if manifest.PairArm == "" || len(scores) != 2 || !scores[0].Completed || !scores[1].Completed {
		return nil
	}
	dir := filepath.Join(manifest.OutputDir, "questions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file := checkpointFile(dir, question)
	temporary, err := os.CreateTemp(dir, ".checkpoint-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	err = json.NewEncoder(temporary).Encode(questionCheckpoint{checkpointFingerprint(manifest), question, manifest.WorkflowRunID, scores})
	err = errors.Join(err, temporary.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(temporary.Name(), file); err != nil {
		return fmt.Errorf("seal question checkpoint: %w", err)
	}
	return nil
}

func capturePairLogs(ctx context.Context, n *Native) error {
	if n.RuntimeLogs == nil {
		return errors.New("paired evaluation requires complete runtime capture")
	}
	flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	return n.RuntimeLogs(flush)
}

type applicationPoolCounters struct {
	ProcessStartedAt string `json:"process_started_at"`
	WaitCount        *int64 `json:"wait_count"`
	WaitDurationNS   *int64 `json:"wait_duration_ns"`
}

type applicationPoolInterval struct {
	Available      bool   `json:"available"`
	Reason         string `json:"reason,omitempty"`
	Source         string `json:"source"`
	Scope          string `json:"scope"`
	WaitCount      *int64 `json:"wait_count"`
	WaitDurationNS *int64 `json:"wait_duration_ns"`
}

func readApplicationPool(ctx context.Context, n *Native) (*applicationPoolCounters, error) {
	var metrics struct {
		Window json.RawMessage          `json:"window"`
		System json.RawMessage          `json:"system"`
		Pool   *applicationPoolCounters `json:"application_postgres_pool"`
	}
	err := n.Control(ctx, http.MethodGet, "/metrics?window_minutes=1", nil, &metrics)
	if err == nil && (len(metrics.Window) == 0 || len(metrics.System) == 0) {
		err = errors.New("control service metrics response is incomplete")
	}
	return metrics.Pool, err
}

func compareApplicationPool(before, after *applicationPoolCounters) (applicationPoolInterval, error) {
	result := applicationPoolInterval{Source: "/control/api/metrics", Scope: "fresh Remember and both-sided Recall on one isolated application process"}
	if before == nil && after == nil {
		result.Reason = "application_postgres_pool_not_exposed_by_runtime"
		return result, nil
	}
	if before == nil || after == nil || before.ProcessStartedAt == "" || before.ProcessStartedAt != after.ProcessStartedAt || before.WaitCount == nil || after.WaitCount == nil || before.WaitDurationNS == nil || after.WaitDurationNS == nil || *before.WaitCount < 0 || *before.WaitDurationNS < 0 || *after.WaitCount < *before.WaitCount || *after.WaitDurationNS < *before.WaitDurationNS {
		return result, errors.New("partial or reset application pool measurements")
	}
	result.Available = true
	result.WaitCount = new(*after.WaitCount - *before.WaitCount)
	result.WaitDurationNS = new(*after.WaitDurationNS - *before.WaitDurationNS)
	return result, nil
}
