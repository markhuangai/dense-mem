package evalharness

import (
	"context"
	"errors"
	"fmt"
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
}

func Run(ctx context.Context, manifest Manifest, corpus []CorpusItem, questions []Question, client *HTTPClient, transport modelprovider.StructuredTransport, native *Native) (report Report, runErr error) {
	started := time.Now()
	report = NewReport(manifest, len(corpus), len(questions))
	if client == nil || client.Capture == nil || native == nil {
		return report, errors.New("runner dependencies are required")
	}
	if err := os.MkdirAll(manifest.OutputDir, 0700); err != nil {
		return report, err
	}
	if err := client.Capture.Emit("started", manifest); err != nil {
		return report, err
	}
	stage := "startup"
	defer func() {
		logsComplete := true
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if err := native.Pause(shutdown); err != nil {
			runErr = errors.Join(runErr, err)
		}
		if native.RuntimeLogs != nil {
			if err := native.RuntimeLogs(shutdown); err != nil {
				logsComplete = false
				runErr = errors.Join(runErr, err)
			}
		}
		report.ServerUsage = native.ServerUsage
		report.ElapsedMS = float64(time.Since(started)) / float64(time.Millisecond)
		if runErr != nil {
			report.Failure = stage + " incomplete"
			_ = client.Capture.Emit("campaign_failed", map[string]any{"stage": stage, "error": client.Capture.ProtectError(runErr)})
		}
		terminal := completion{ImportedRows: report.ImportedRows, CanonicalHash: report.Identity.CanonicalHash, ElapsedMS: report.ElapsedMS, Failure: report.Failure, NativeMeasurements: report.NativeMeasurements, ServerUsage: report.ServerUsage}
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
	stage = "seed verification"
	seed, mapping, canonical, initial, err := VerifySeed(work, manifest, corpus, client, native)
	if err != nil {
		return report, err
	}
	report.ImportedRows = len(mapping.BySourceDocID)
	report.Identity.SeedImage = manifest.SeedImage
	report.Identity.SeedRelease = seed.BaseRelease
	report.Identity.SeedPreparationWorkers = seed.ImportWorkers
	report.Timings = manifest.Timings
	if report.Timings == nil {
		report.Timings = map[string]float64{}
	}
	if err = client.Capture.Emit("seed_verified", map[string]any{"seed_image": manifest.SeedImage, "corpus_rows": len(mapping.BySourceDocID), "canonical_evidence": len(canonical)}); err != nil {
		return report, err
	}
	report.Identity.CanonicalHash = initial.CanonicalHash
	report.NativeMeasurements = append(report.NativeMeasurements, initial)
	stage = "ontology off"
	conditionStarted := time.Now()
	scores, err := runCondition(work, manifest, corpus, questions, mapping, canonical, client, transport, "off")
	report.Timings["ontology_off_scoring"] = float64(time.Since(conditionStarted)) / float64(time.Millisecond)
	report.Scores = append(report.Scores, scores...)
	if err != nil {
		return report, err
	}
	stage = "ontology convergence"
	conditionStarted = time.Now()
	if err = native.EnableOntology(work, manifest); err != nil {
		return report, err
	}
	converged, err := waitNative(work, native, manifest, false)
	if err != nil {
		return report, err
	}
	if converged.CanonicalHash != initial.CanonicalHash {
		return report, errors.New("ontology changed canonical Evidence or provenance")
	}
	if err = native.Pause(work); err != nil {
		return report, err
	}
	frozen, err := waitNative(work, native, manifest, true)
	if err != nil {
		return report, err
	}
	if frozen.CanonicalHash != initial.CanonicalHash {
		return report, errors.New("canonical Evidence changed while draining")
	}
	report.NativeMeasurements = append(report.NativeMeasurements, frozen)
	report.Timings["ontology_convergence_and_drain"] = float64(time.Since(conditionStarted)) / float64(time.Millisecond)
	stage = "ontology on"
	conditionStarted = time.Now()
	scores, err = runCondition(work, manifest, corpus, questions, mapping, canonical, client, transport, "on")
	report.Timings["ontology_on_scoring"] = float64(time.Since(conditionStarted)) / float64(time.Millisecond)
	report.Scores = append(report.Scores, scores...)
	if err != nil {
		return report, err
	}
	stage = "final verification"
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
	report.NativeMeasurements = append(report.NativeMeasurements, final)
	return report, nil
}

func waitNative(ctx context.Context, native *Native, manifest Manifest, paused bool) (NativeObservation, error) {
	for {
		observation, err := native.Snapshot(ctx)
		if err != nil {
			return observation, err
		}
		if err = native.Client.Capture.Emit("native_observation", observation); err != nil {
			return observation, err
		}
		if observation.Ontology.ConfigurationError != "" || observation.ForeignSources != 0 || observation.ForeignWork != 0 || observation.Ontology.Counts.Failed > 0 || observation.Ontology.Counts.Ambiguous > 0 {
			return observation, errors.New("native convergence failed")
		}
		if err = NativeReady(observation, true, paused, manifest, native.Scope.TeamID); err == nil {
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
	for range 4 {
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
