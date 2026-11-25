package evalharness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

type completion struct {
	ImportedRows       int                    `json:"restored_rows"`
	CanonicalHash      string                 `json:"canonical_hash"`
	ElapsedMS          float64                `json:"elapsed_ms"`
	Failure            string                 `json:"failure"`
	NativeMeasurements []NativeObservation    `json:"native_measurements"`
	ServerUsage        map[string]UsageTotals `json:"server_usage"`
	Timings            map[string]float64     `json:"timings_ms"`
	ModelVerified      bool                   `json:"model_verified_before_cutoff"`
}

func Run(ctx context.Context, manifest Manifest, corpus []CorpusItem, questions []Question, client *HTTPClient, transport modelprovider.StructuredTransport, native *Native) (report Report, runErr error) {
	started := time.Now()
	report = NewReport(manifest, len(corpus), len(questions))
	if client == nil || client.Capture == nil || native == nil {
		return report, errors.New("runner dependencies are required")
	}
	if manifest.Condition != scoringCondition {
		return report, errors.New("scoring requires topic maintenance to be disabled")
	}
	manifest.AnswerWorkers = manifest.effectiveAnswerWorkers()
	if manifest.AnswerWorkers < defaultAnswerWorkers || manifest.AnswerWorkers > maxAnswerWorkers {
		return report, errors.New("answer_workers must be 4..10")
	}
	if err := os.MkdirAll(manifest.OutputDir, 0700); err != nil {
		return report, err
	}
	report.Timings = maps.Clone(manifest.Timings)
	if report.Timings == nil {
		report.Timings = map[string]float64{}
	}
	stage, timing, phaseStarted := "seed verification", "seed_verification", time.Now()
	work, stop := context.WithDeadline(ctx, manifest.ModelDeadline)
	defer stop()
	var scoringStarted time.Time
	client.Capture.startQueue(stop)
	client.Capture.setPhase(stage)
	advance := func(next, key string) {
		report.Timings[timing] = elapsedMS(phaseStarted)
		stage, timing, phaseStarted = next, key, time.Now()
		client.Capture.setPhase(next)
		_ = client.Capture.EmitContext(work, "phase_started", map[string]any{"stage": stage, "condition": scoringCondition, "answer_workers": manifest.AnswerWorkers})
	}
	var pausedAndDrained bool
	defer func() {
		report.Timings[timing] = elapsedMS(phaseStarted)
		logsComplete := true
		shutdownStarted := time.Now()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		cutoff := !time.Now().Before(manifest.ModelDeadline) || (native.StopRuntime != nil && ctx.Err() != nil)
		if !cutoff && !pausedAndDrained {
			if err := native.Pause(shutdown); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
		if native.StopRuntime != nil {
			cutoff = cutoff || !time.Now().Before(manifest.ModelDeadline) || ctx.Err() != nil
			if err := native.StopRuntime(shutdown, cutoff); err != nil {
				logsComplete = false
				if runErr == nil {
					stage = "runtime shutdown"
				}
				runErr = errors.Join(runErr, err)
			}
		}
		cancel()
		report.Timings["runtime_shutdown"] = elapsedMS(shutdownStarted)
		queue := client.Capture.queue
		preserve := queue.preservation(ctx)
		defer queue.cancelPreserve()
		captureStage := "log capture"
		client.Capture.setPhase(captureStage)
		if logsComplete && native.RuntimeLogs != nil {
			captureStarted := time.Now()
			if err := native.RuntimeLogs(preserve); err != nil {
				client.Capture.recordFailure(CaptureFailure{Phase: captureStage, Kind: "runtime_logs", Reason: "runtime_log_collection_failed"})
				logsComplete = false
				if runErr == nil {
					stage = "log capture"
				}
				runErr = errors.Join(runErr, fmt.Errorf("protected runtime capture: %w", err))
			}
			report.Timings["runtime_log_capture"] = elapsedMS(captureStarted)
		}
		report.ServerUsage = native.ServerUsage
		report.ElapsedMS = elapsedMS(started)
		if runErr != nil {
			report.Failure = stage + " incomplete"
			_ = client.Capture.EmitContext(preserve, "campaign_failed", map[string]any{"stage": stage, "error": client.Capture.protectError(preserve, runErr)})
			runErr = fmt.Errorf("%s: %w", report.Failure, runErr)
		}
		terminal := completion{ImportedRows: report.ImportedRows, CanonicalHash: report.Identity.CanonicalHash, ElapsedMS: report.ElapsedMS, Failure: report.Failure, NativeMeasurements: report.NativeMeasurements, ServerUsage: report.ServerUsage, Timings: report.Timings, ModelVerified: report.ModelVerified}
		if err := client.Capture.EmitContext(preserve, "finished", terminal); err != nil {
			runErr = errors.Join(runErr, err)
			report.Failure = "log capture incomplete"
		} else {
			report.CaptureComplete = logsComplete
		}
		flushStarted := time.Now()
		if err := client.Capture.finishQueue(preserve); err != nil {
			runErr = errors.Join(runErr, err)
			report.CaptureComplete = false
			report.Failure = "log capture incomplete"
		}
		report.Timings["scoring_capture_flush"] = elapsedMS(flushStarted)
		report.Timings["capture_queue_worker_ms"] = float64(queue.waitNS.Load()) / float64(time.Millisecond)
		report.Timings["capture_sink_ms"] = float64(queue.sinkNS.Load()) / float64(time.Millisecond)
		report.Timings["answer_http_worker_ms"] = float64(queue.httpNS.Load()) / float64(time.Millisecond)
		if finished := queue.scoringFinishedNS.Load(); finished != 0 && !scoringStarted.IsZero() {
			report.Timings["scoring_through_flush"] = float64(max(time.Unix(0, finished).Sub(scoringStarted), 0)) / float64(time.Millisecond)
		}
		report.CaptureFailure = client.Capture.FirstFailure()
		report.ElapsedMS = elapsedMS(started)
		if err := FinalizeReport(&report); err != nil {
			runErr = errors.Join(runErr, err)
		}
		if err := writeScoreFile(filepath.Join(manifest.OutputDir, "scores.json"), report); err != nil {
			runErr = errors.Join(runErr, err)
			report.Complete = false
			if report.Failure == "" {
				report.Failure = "score report publication incomplete"
			}
		}
		if logger := client.Capture.Logger; logger != nil {
			level := slog.LevelInfo
			if runErr != nil || !report.Complete {
				level = slog.LevelError
			}
			fields := []slog.Attr{
				slog.String("run_id", manifest.RunID), slog.String("dataset", manifest.Dataset),
				slog.Int("completed_answers", report.CompletedAnswers), slog.Int("expected_answers", report.ExpectedAnswers),
				slog.Bool("model_verified_before_cutoff", report.ModelVerified), slog.Bool("capture_complete", report.CaptureComplete),
				slog.Bool("complete", report.Complete), slog.Any("timings_ms", report.Timings),
			}
			if report.Failure != "" {
				fields = append(fields, slog.String("failure", report.Failure))
			}
			if failure := report.CaptureFailure; failure != nil {
				fields = append(fields, slog.Group("capture_failure", "phase", failure.Phase, "kind", failure.Kind,
					"sequence", failure.Sequence, "reason", failure.Reason, "bytes", failure.Bytes,
					"protection", failure.Protection, "source", failure.Source, "exchange_id", failure.ExchangeID))
			}
			logger.LogAttrs(context.WithoutCancel(ctx), level, "eval_dataset_outcome", fields...)
		}
	}()
	if err := client.Capture.emit(work, stage, "started", manifest); err != nil {
		return report, err
	}
	seed, mapping, canonical, initial, err := VerifySeed(work, manifest, corpus, client, native)
	if err != nil {
		return report, err
	}
	report.ImportedRows = len(mapping.BySourceDocID)
	report.Identity.SeedImage = manifest.SeedImage
	report.Identity.SeedRelease = seed.BaseRelease
	report.Identity.SeedPreparationWorkers = seed.ImportWorkers
	if err = client.Capture.EmitContext(work, "seed_verified", map[string]any{"seed_image": manifest.SeedImage, "corpus_rows": len(mapping.BySourceDocID), "canonical_evidence": len(canonical), "scope": native.Scope}); err != nil {
		return report, err
	}
	report.Identity.CanonicalHash = initial.CanonicalHash
	initial.ProviderAttempts = nil
	report.NativeMeasurements = append(report.NativeMeasurements, initial)
	pausedAndDrained = true
	checkpoint, err := newScoreCheckpoint(manifest.OutputDir, report)
	if err != nil {
		stage = "score checkpoint"
		return report, fmt.Errorf("initialize evaluation score checkpoint: %w", err)
	}
	client.Capture.checkpoint.Store(checkpoint)
	client.Capture.saveCheckpointFailure(client.Capture.FirstFailure())
	advance("ontology off", "ontology_off_scoring")
	scoringStarted = phaseStarted
	report.Scores, err = runCondition(work, manifest, corpus, questions, mapping, canonical, client, transport, checkpoint)
	native.RequireRuntimeUsage = len(report.Scores) != 0
	if err != nil {
		return report, err
	}
	advance("final verification", "final_verification")
	_, _, _, final, err := VerifySeed(work, manifest, corpus, client, native)
	if err != nil {
		return report, err
	}
	if err := work.Err(); err != nil {
		return report, err
	}
	if !time.Now().Before(manifest.ModelDeadline) {
		return report, context.DeadlineExceeded
	}
	final.ProviderAttempts = nil
	report.NativeMeasurements = append(report.NativeMeasurements, final)
	report.ModelVerified = true
	return report, nil
}

func elapsedMS(started time.Time) float64 {
	return float64(time.Since(started)) / float64(time.Millisecond)
}

func runCondition(ctx context.Context, manifest Manifest, corpus []CorpusItem, questions []Question, mapping KnowledgeMapping, canonical map[string]CanonicalEvidence, client *HTTPClient, transport modelprovider.StructuredTransport, checkpoint *scoreCheckpoint) ([]CaseScore, error) {
	const condition = scoringCondition
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := client.Capture.EmitContext(ctx, "condition_started", map[string]string{"condition": condition}); err != nil {
		return nil, err
	}
	policies := []string{"single", "multi_turn"}
	progress := captureProgress{Stage: "questions", Condition: condition, Unit: "answers", Total: len(questions) * len(policies)}
	if err := client.Capture.emitProgressContext(ctx, progress, false); err != nil {
		return nil, err
	}
	bySource := map[string]CorpusItem{}
	for _, item := range corpus {
		bySource[item.SourceDocID] = item
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var scores []CaseScore
	var first error
	var progressWriter sync.Mutex
	lastProgress := time.Now()
	fail := func(err error) {
		mu.Lock()
		if first == nil {
			first = err
		}
		mu.Unlock()
		cancel()
	}
	for range manifest.effectiveAnswerWorkers() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ordinal := range jobs {
				question := questions[ordinal]
				initial := memoryCall(ctx, client, "recall_memory", map[string]any{"query": question.Query, "limit": 10, "topic_limit": 3})
				if err := validateInitial(initial); err != nil {
					initial.Error = err.Error()
				}
				observation := Observation{QuestionID: question.ID, Condition: condition, Initial: initial, Hosts: []HostResult{}}
				if err := client.Capture.EmitContext(ctx, "initial_recall", map[string]any{"question_id": question.ID, "condition": condition, "call": initial}); err != nil {
					fail(err)
					return
				}
				scorer, err := newAnswerScorer(question, observation, bySource, mapping, canonical)
				if err != nil {
					fail(err)
					return
				}
				for _, policy := range policies {
					if ctx.Err() != nil {
						break
					}
					host := RunHost(ctx, client, transport, manifest, HostQuestion{ID: question.ID, Query: question.Query}, initial, policy, condition)
					observation.Hosts = append(observation.Hosts, host)
					score, err := scorer(host)
					if err != nil {
						fail(err)
						break
					}
					if checkpoint != nil {
						if err := checkpoint.save(ordinal, score); err != nil {
							fail(fmt.Errorf("persist evaluation score checkpoint for question %d policy %s: %w", ordinal, policy, err))
							break
						}
					}
					mu.Lock()
					scores = append(scores, score)
					if score.Completed {
						progress.Completed++
					} else {
						progress.Failed++
					}
					mu.Unlock()
				}
				err = client.Capture.EmitTerminal(ctx, "observation", observationReceipt(observation))
				// Workers can continue scoring while another worker publishes progress.
				if err == nil && progressWriter.TryLock() {
					if time.Since(lastProgress) >= time.Minute {
						mu.Lock()
						current := progress
						mu.Unlock()
						err = client.Capture.emitProgressContext(ctx, current, true)
						lastProgress = time.Now()
					}
					progressWriter.Unlock()
				}
				if err != nil {
					fail(err)
				}
				if ctx.Err() != nil {
					return
				}
			}
		}()
	}
schedule:
	for ordinal := range questions {
		select {
		case jobs <- ordinal:
		case <-ctx.Done():
			break schedule
		}
	}
	close(jobs)
	wg.Wait()
	if err := client.Capture.emitProgressContext(ctx, progress, true); first == nil {
		first = err
	}
	if err := client.Capture.EmitTerminal(ctx, "condition_finished", map[string]string{"condition": condition}); first == nil {
		first = err
	}
	if first != nil {
		return scores, first
	}
	return scores, ctx.Err()
}

func observationReceipt(observation Observation) any {
	hosts := make([]HostResult, len(observation.Hosts))
	for i, host := range observation.Hosts {
		if len(host.Calls) != 0 {
			host.Calls = host.Calls[1:]
		}
		hosts[i] = host
	}
	return struct {
		Version   int          `json:"version"`
		Question  string       `json:"question_id"`
		Condition string       `json:"condition"`
		Hosts     []HostResult `json:"hosts"`
	}{2, observation.QuestionID, observation.Condition, hosts}
}
