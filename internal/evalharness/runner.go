package evalharness

import (
	"context"
	"errors"
	"fmt"
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
	manifest.AnswerWorkers = manifest.effectiveAnswerWorkers()
	if manifest.AnswerWorkers < defaultAnswerWorkers || manifest.AnswerWorkers > maxAnswerWorkers {
		return report, errors.New("answer_workers must be 4..10")
	}
	if err := os.MkdirAll(manifest.OutputDir, 0700); err != nil {
		return report, err
	}
	if err := client.Capture.Emit("started", manifest); err != nil {
		return report, err
	}
	report.Timings = maps.Clone(manifest.Timings)
	if report.Timings == nil {
		report.Timings = map[string]float64{}
	}
	stage, timing, phaseStarted := "seed verification", "seed_verification", time.Now()
	advance := func(next, key string) {
		report.Timings[timing] = elapsedMS(phaseStarted)
		stage, timing, phaseStarted = next, key, time.Now()
		if client.Capture.Logger != nil {
			client.Capture.Logger.Info("evaluation phase started", "run_id", manifest.RunID, "stage", stage, "answer_workers", manifest.AnswerWorkers)
		}
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
		if logsComplete && native.RuntimeLogs != nil {
			captureStarted := time.Now()
			preserve, stopCapture := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Minute)
			if err := native.RuntimeLogs(preserve); err != nil {
				logsComplete = false
				if runErr == nil {
					stage = "log capture"
				}
				runErr = errors.Join(runErr, fmt.Errorf("protected runtime capture: %w", err))
			}
			stopCapture()
			report.Timings["runtime_log_capture"] = elapsedMS(captureStarted)
		}
		report.ServerUsage = native.ServerUsage
		report.ElapsedMS = elapsedMS(started)
		if runErr != nil {
			report.Failure = stage + " incomplete"
			_ = client.Capture.Emit("campaign_failed", map[string]any{"stage": stage, "error": client.Capture.ProtectError(runErr)})
			runErr = fmt.Errorf("%s: %w", report.Failure, runErr)
		}
		terminal := completion{ImportedRows: report.ImportedRows, CanonicalHash: report.Identity.CanonicalHash, ElapsedMS: report.ElapsedMS, Failure: report.Failure, NativeMeasurements: report.NativeMeasurements, ServerUsage: report.ServerUsage, Timings: report.Timings, ModelVerified: report.ModelVerified}
		if err := client.Capture.Emit("finished", terminal); err != nil {
			runErr = errors.Join(runErr, err)
			report.Failure = "log capture incomplete"
		} else {
			report.CaptureComplete = logsComplete
		}
		if err := FinalizeReport(&report); err != nil {
			runErr = errors.Join(runErr, err)
		}
		if err := writeJSON(filepath.Join(manifest.OutputDir, "scores.json"), report); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	work, stop := context.WithDeadline(ctx, manifest.ModelDeadline)
	defer stop()
	seed, mapping, canonical, initial, err := VerifySeed(work, manifest, corpus, client, native)
	if err != nil {
		return report, err
	}
	report.ImportedRows = len(mapping.BySourceDocID)
	report.Identity.SeedImage = manifest.SeedImage
	report.Identity.SeedRelease = seed.BaseRelease
	report.Identity.SeedPreparationWorkers = seed.ImportWorkers
	if err = client.Capture.Emit("seed_verified", map[string]any{"seed_image": manifest.SeedImage, "corpus_rows": len(mapping.BySourceDocID), "canonical_evidence": len(canonical)}); err != nil {
		return report, err
	}
	report.Identity.CanonicalHash = initial.CanonicalHash
	report.NativeMeasurements = append(report.NativeMeasurements, initial)
	advance("ontology off", "ontology_off_scoring")
	scores, err := runCondition(work, manifest, corpus, questions, mapping, canonical, client, transport, "off")
	report.Scores = append(report.Scores, scores...)
	if err != nil {
		return report, err
	}
	advance("ontology convergence", "ontology_convergence_and_drain")
	if err = native.EnableOntology(work, manifest); err != nil {
		return report, err
	}
	if _, err := waitNative(work, native, manifest, false, initial.CanonicalHash); err != nil {
		return report, err
	}
	if err = native.Pause(work); err != nil {
		return report, err
	}
	frozen, err := waitNative(work, native, manifest, true, initial.CanonicalHash)
	if err != nil {
		return report, err
	}
	pausedAndDrained = true
	report.NativeMeasurements = append(report.NativeMeasurements, frozen)
	advance("ontology on", "ontology_on_scoring")
	scores, err = runCondition(work, manifest, corpus, questions, mapping, canonical, client, transport, "on")
	report.Scores = append(report.Scores, scores...)
	if err != nil {
		return report, err
	}
	advance("final verification", "final_verification")
	final, err := native.Snapshot(work)
	if err != nil {
		return report, err
	}
	if err = NativeReady(final, true, true, manifest, native.Scope.TeamID); err != nil {
		return report, err
	}
	if final.CanonicalHash != initial.CanonicalHash {
		return report, errors.New("answer campaign changed canonical Evidence")
	}
	if err := work.Err(); err != nil {
		return report, err
	}
	if !time.Now().Before(manifest.ModelDeadline) {
		return report, context.DeadlineExceeded
	}
	report.ModelVerified = true
	report.NativeMeasurements = append(report.NativeMeasurements, final)
	return report, nil
}

func elapsedMS(started time.Time) float64 {
	return float64(time.Since(started)) / float64(time.Millisecond)
}

func waitNative(ctx context.Context, native *Native, manifest Manifest, paused bool, canonicalHash string) (NativeObservation, error) {
	var recovery ontologyRecovery
	for {
		observation, err := native.Snapshot(ctx)
		if err != nil {
			return observation, err
		}
		if err = native.Client.Capture.Emit("native_observation", observation); err != nil {
			return observation, err
		}
		if observation.CanonicalHash != canonicalHash {
			return observation, errors.New("ontology changed canonical Evidence or provenance")
		}
		if observation.Ontology.ConfigurationError != "" || observation.ForeignSources != 0 || observation.ForeignWork != 0 || observation.Ontology.Counts.Ambiguous > 0 ||
			(paused && observation.Ontology.Counts.Failed > 0) {
			return observation, errors.New("native convergence failed")
		}
		var waiting bool
		if !paused {
			waiting, err = recovery.advance(ctx, native, manifest, observation)
			if err != nil {
				return observation, err
			}
		}
		if !waiting && NativeReady(observation, true, paused, manifest, native.Scope.TeamID) == nil {
			return observation, nil
		}
		select {
		case <-ctx.Done():
			return observation, fmt.Errorf("native convergence deadline: %w", ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}

func runCondition(ctx context.Context, manifest Manifest, corpus []CorpusItem, questions []Question, mapping KnowledgeMapping, canonical map[string]CanonicalEvidence, client *HTTPClient, transport modelprovider.StructuredTransport, condition string) ([]CaseScore, error) {
	if err := client.Capture.Emit("condition_started", map[string]string{"condition": condition}); err != nil {
		return nil, err
	}
	policies := []string{"single", "multi_turn"}
	progress := captureProgress{Stage: "questions", Condition: condition, Unit: "answers", Total: len(questions) * len(policies)}
	if err := client.Capture.emitProgress(progress); err != nil {
		return nil, err
	}
	bySource := map[string]CorpusItem{}
	for _, item := range corpus {
		bySource[item.SourceDocID] = item
	}
	jobs := make(chan Question)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var scores []CaseScore
	var first error
	for range manifest.effectiveAnswerWorkers() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for question := range jobs {
				initial := memoryCall(ctx, client, "recall_memory", map[string]any{"query": question.Query, "limit": 10, "topic_limit": 3})
				if err := validateInitial(initial); err != nil {
					initial.Error = err.Error()
				}
				observation := Observation{QuestionID: question.ID, Condition: condition, Initial: initial, Hosts: []HostResult{}}
				for _, policy := range policies {
					observation.Hosts = append(observation.Hosts, RunHost(ctx, client, transport, manifest, HostQuestion{ID: question.ID, Query: question.Query}, initial, policy, condition))
				}
				err := client.Capture.Emit("observation", observation)
				var measured []CaseScore
				if err == nil {
					measured, err = ScoreObservation(question, observation, bySource, mapping, canonical)
				}
				mu.Lock()
				scores = append(scores, measured...)
				completed := 0
				for _, score := range measured {
					if score.Completed {
						completed++
					}
				}
				progress.Completed += completed
				progress.Failed += len(policies) - completed
				if progressErr := client.Capture.emitProgress(progress); err == nil {
					err = progressErr
				}
				if err != nil && first == nil {
					first = err
				}
				mu.Unlock()
			}
		}()
	}
schedule:
	for _, question := range questions {
		select {
		case jobs <- question:
		case <-ctx.Done():
			break schedule
		}
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return scores, first
	}
	return scores, ctx.Err()
}
