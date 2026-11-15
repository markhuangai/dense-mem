package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

type completion struct {
	ImportedRows       int                    `json:"imported_rows"`
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
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if err := native.Pause(shutdown); err != nil {
			runErr = errors.Join(runErr, err)
		}
		if native.RuntimeLogs != nil {
			if err := native.RuntimeLogs(shutdown); err != nil {
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
			report.CaptureComplete = true
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
	if err := native.ResolveScope(work); err != nil {
		return report, err
	}
	before, err := native.Snapshot(work)
	if err != nil {
		return report, err
	}
	if before.EvidenceCount != 0 || before.ForeignSources != 0 || before.Ontology.Enabled || !before.Ontology.Paused {
		return report, errors.New("runner instance must be fresh with ontology disabled and paused")
	}
	stage = "import"
	mapping, err := client.ImportCorpusWithConcurrency(work, corpus, DefaultImportConcurrency)
	report.ImportedRows = len(mapping.BySourceDocID)
	if err != nil {
		return report, err
	}
	canonical, err := native.VerifyMappings(work, corpus, mapping)
	if err != nil {
		return report, err
	}
	for source, parts := range mapping.Partitions {
		if err = client.Capture.Emit("mapping", map[string]any{"source_id": source, "partitions": parts}); err != nil {
			return report, err
		}
	}
	for id, evidence := range canonical {
		if err = client.Capture.Emit("canonical", map[string]any{"evidence_id": id, "evidence": evidence}); err != nil {
			return report, err
		}
	}
	initial, err := native.Snapshot(work)
	if err != nil {
		return report, err
	}
	if err = NativeReady(initial, false, true, manifest, native.Scope.TeamID); err != nil {
		return report, err
	}
	report.Identity.CanonicalHash = initial.CanonicalHash
	report.NativeMeasurements = append(report.NativeMeasurements, initial)
	stage = "ontology off"
	scores, err := runCondition(work, manifest, corpus, questions, mapping, canonical, client, transport, "off")
	report.Scores = append(report.Scores, scores...)
	if err != nil {
		return report, err
	}
	stage = "ontology convergence"
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
	stage = "ontology on"
	scores, err = runCondition(work, manifest, corpus, questions, mapping, canonical, client, transport, "on")
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
				for _, policy := range []string{"single", "multi_turn"} {
					observation.Hosts = append(observation.Hosts, RunHost(ctx, client, transport, manifest, HostQuestion{ID: question.ID, Query: question.Query}, initial, policy, condition))
				}
				err := client.Capture.Emit("observation", observation)
				var measured []CaseScore
				if err == nil {
					measured, err = ScoreObservation(question, observation, bySource, mapping, canonical)
				}
				mu.Lock()
				scores = append(scores, measured...)
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

func ReplayReport(events []Event, manifest Manifest, corpus []CorpusItem, questions []Question) (Report, error) {
	if len(events) < 2 {
		return Report{}, errors.New("replay unavailable: no complete capture")
	}
	var recorded Manifest
	if err := decodePayload(events[0], &recorded); err != nil {
		return Report{}, err
	}
	if recorded.SourceRevision != manifest.SourceRevision || recorded.Image != manifest.Image || recorded.RegistryHash != manifest.RegistryHash || recorded.Dataset != manifest.Dataset || recorded.RunID != manifest.RunID || recorded.Model != manifest.Model || recorded.ContextWindowTokens != manifest.ContextWindowTokens || recorded.EmbeddingModel != manifest.EmbeddingModel || recorded.EmbeddingDimensions != manifest.EmbeddingDimensions {
		return Report{}, errors.New("replay identity differs from pinned campaign")
	}
	report := NewReport(recorded, len(corpus), len(questions))
	mapping := newKnowledgeMapping()
	canonical := map[string]CanonicalEvidence{}
	bySource := map[string]CorpusItem{}
	for _, item := range corpus {
		bySource[item.SourceDocID] = item
	}
	byQuestion := map[string]Question{}
	for _, q := range questions {
		byQuestion[q.ID] = q
	}
	validated := false
	for _, event := range events[1 : len(events)-1] {
		switch event.Kind {
		case "mapping":
			if validated {
				return report, errors.New("replay mappings changed after observation")
			}
			var value struct {
				SourceID   string              `json:"source_id"`
				Partitions []ImportedPartition `json:"partitions"`
			}
			if err := decodePayload(event, &value); err != nil {
				return report, err
			}
			if _, exists := mapping.Partitions[value.SourceID]; exists {
				return report, errors.New("duplicate replay mapping")
			}
			if mapping.Partitions == nil {
				mapping.Partitions = map[string][]ImportedPartition{}
			}
			mapping.Partitions[value.SourceID] = value.Partitions
		case "canonical":
			if validated {
				return report, errors.New("replay canonical Evidence changed after observation")
			}
			var value struct {
				EvidenceID string            `json:"evidence_id"`
				Evidence   CanonicalEvidence `json:"evidence"`
			}
			if err := decodePayload(event, &value); err != nil {
				return report, err
			}
			if _, exists := canonical[value.EvidenceID]; exists {
				return report, errors.New("duplicate replay canonical identity")
			}
			canonical[value.EvidenceID] = value.Evidence
		case "observation":
			if !validated {
				if err := ValidateMappings(corpus, mapping, canonical); err != nil {
					return report, fmt.Errorf("replay unavailable: %w", err)
				}
				validated = true
			}
			var observation Observation
			if err := decodePayload(event, &observation); err != nil {
				return report, err
			}
			question, exists := byQuestion[observation.QuestionID]
			if !exists {
				return report, errors.New("replay contains an unknown question")
			}
			scores, err := ScoreObservation(question, observation, bySource, mapping, canonical)
			if err != nil {
				return report, err
			}
			report.Scores = append(report.Scores, scores...)
		}
	}
	var terminal completion
	if err := json.Unmarshal(events[len(events)-1].Payload, &terminal); err != nil {
		return report, err
	}
	report.ImportedRows, report.ElapsedMS, report.Failure, report.Identity.CanonicalHash, report.NativeMeasurements = terminal.ImportedRows, terminal.ElapsedMS, terminal.Failure, terminal.CanonicalHash, terminal.NativeMeasurements
	report.ServerUsage = terminal.ServerUsage
	if terminal.Failure == "" && (len(mapping.Partitions) != len(corpus) || len(terminal.NativeMeasurements) != 3) {
		return report, errors.New("replay unavailable: required mapping or preservation evidence missing")
	}
	if terminal.Failure == "" && !validated {
		if err := ValidateMappings(corpus, mapping, canonical); err != nil {
			return report, fmt.Errorf("replay unavailable: %w", err)
		}
	}
	for _, measurement := range terminal.NativeMeasurements {
		if measurement.CanonicalHash != terminal.CanonicalHash {
			return report, errors.New("replay canonical preservation failed")
		}
	}
	report.CaptureComplete = true
	if err := FinalizeReport(&report); err != nil {
		return report, err
	}
	return report, nil
}
