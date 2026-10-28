package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type PairProcess struct {
	PID       int    `json:"pid"`
	BootID    string `json:"boot_id"`
	StartTick string `json:"start_tick"`
}

func PairProcessIdentity(pid int) (PairProcess, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return PairProcess{}, err
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return PairProcess{}, err
	}
	_, fields, ok := strings.Cut(string(stat), ") ")
	parts := strings.Fields(fields)
	if !ok || len(parts) < 20 {
		return PairProcess{}, fmt.Errorf("invalid process identity")
	}
	return PairProcess{PID: pid, BootID: strings.TrimSpace(string(boot)), StartTick: parts[19]}, nil
}

type PairStageIntent struct {
	SchemaVersion string      `json:"schema_version"`
	ConfigHash    string      `json:"config_hash"`
	Side          string      `json:"side"`
	Stage         string      `json:"stage"`
	Attempt       int         `json:"attempt"`
	StartedAt     time.Time   `json:"started_at"`
	Deadline      time.Time   `json:"deadline"`
	Process       PairProcess `json:"process"`
}

type PairStageReceipt struct {
	Intent     PairStageIntent   `json:"intent"`
	FinishedAt time.Time         `json:"finished_at"`
	Files      map[string]string `json:"files"`
}

type PairStageStart struct {
	Directory string `json:"directory"`
	Reused    bool   `json:"reused"`
	Fresh     bool   `json:"fresh"`
}

func pairConfigHash(c PairConfig) (string, error) {
	c.Deadline = time.Time{}
	return canonicalJSONHash(c)
}

func BeginPairStage(c PairConfig, side, stage string, process PairProcess) (PairStageStart, error) {
	result := PairStageStart{}
	if _, err := pairRelease(c, side); err != nil {
		return result, err
	}
	switch stage {
	case "validate", "capacity", "import", "drain", "capture", "score":
	default:
		return result, fmt.Errorf("invalid paired stage")
	}
	if !time.Now().Before(c.Deadline) {
		return result, fmt.Errorf("paired observation deadline reached: %w", context.DeadlineExceeded)
	}
	root := filepath.Join(c.OutDir, side, stage)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return result, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return result, err
	}
	hash, err := pairConfigHash(c)
	if err != nil {
		return result, err
	}
	previous := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "attempt-") {
			return result, fmt.Errorf("unexpected paired stage artifact")
		}
		n, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "attempt-"))
		if err != nil || n < 1 {
			return result, fmt.Errorf("invalid paired stage attempt")
		}
		previous = max(previous, n)
	}
	if previous > 0 {
		dir := filepath.Join(root, fmt.Sprintf("attempt-%04d", previous))
		var intent PairStageIntent
		if err := readPairJSON(filepath.Join(dir, "intent.json"), &intent); err != nil {
			return result, err
		}
		if intent.ConfigHash != hash || intent.Side != side || intent.Stage != stage || intent.Attempt != previous {
			return result, fmt.Errorf("paired stage identity changed")
		}
		if _, err := os.Stat(filepath.Join(dir, "receipt.json")); err == nil {
			if err := VerifyPairStage(c, dir); err != nil {
				return result, err
			}
			return PairStageStart{Directory: dir, Reused: true}, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		if _, err := os.Stat(filepath.Join(dir, "failure.json")); err == nil {
			return result, fmt.Errorf("paired stage has a retained failure; automatic retry is forbidden")
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		active, err := PairProcessIdentity(intent.Process.PID)
		if err == nil && active == intent.Process {
			return result, fmt.Errorf("paired stage is still owned by its live process")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("cannot prove paired stage process stopped: %w", err)
		}
		var native PairProcess
		if err := readPairJSON(filepath.Join(dir, "native-process.json"), &native); err == nil {
			current, err := PairProcessIdentity(native.PID)
			if err == nil && current == native {
				return result, fmt.Errorf("native subprocess is still running")
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return result, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		}

		if stage == "capture" || stage == "score" {
			if err := FinishPairStage(c, dir); err != nil {
				return result, fmt.Errorf("interrupted %s is not proven complete: %w", stage, err)
			}
			return PairStageStart{Directory: dir, Reused: true}, nil
		}
		if err := writePairJSON(filepath.Join(dir, "interrupted.json"), map[string]any{"observed_at": time.Now().UTC(), "original_process": intent.Process}); err != nil {
			return result, err
		}
	}
	dir := filepath.Join(root, fmt.Sprintf("attempt-%04d", previous+1))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return result, err
	}
	intent := PairStageIntent{SchemaVersion: PairSchema, ConfigHash: hash, Side: side, Stage: stage, Attempt: previous + 1, StartedAt: time.Now().UTC(), Deadline: c.Deadline, Process: process}
	if err := writePairJSON(filepath.Join(dir, "intent.json"), intent); err != nil {
		return result, err
	}
	if stage == "import" {
		_, corpus, _, _, _, _, _, err := loadRunInputs(c.SeedManifest, c.Suite)
		if err != nil {
			return result, err
		}
		requests := make([]map[string]string, 0, len(corpus))
		for _, item := range corpus {
			hash, err := pairRequestHash(item)
			if err != nil {
				return result, err
			}
			requests = append(requests, map[string]string{"source_doc_id": item.SourceDocID, "idempotency_key": "eval:" + item.SourceDocID, "request_hash": hash})
		}
		if err := writePairJSON(filepath.Join(dir, "requests.json"), requests); err != nil {
			return result, err
		}
	}
	return PairStageStart{Directory: dir, Fresh: previous == 0}, nil
}

func FinishPairStage(c PairConfig, dir string) error {
	var intent PairStageIntent
	if err := readPairJSON(filepath.Join(dir, "intent.json"), &intent); err != nil {
		return err
	}
	hash, err := pairConfigHash(c)
	if err != nil {
		return err
	}
	if intent.SchemaVersion != PairSchema || intent.ConfigHash != hash || intent.StartedAt.IsZero() {
		return fmt.Errorf("paired stage intent is incompatible")
	}
	if _, err := os.Stat(filepath.Join(dir, "receipt.json")); err == nil {
		return VerifyPairStage(c, dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "failure.json")); err == nil {
		return fmt.Errorf("cannot complete a failed stage")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := validatePairStageOutputs(c, intent, dir); err != nil {
		return err
	}
	if intent.Stage == "capture" {
		if err := sealPairCapture(c, intent.Side, dir); err != nil {
			return err
		}
	}
	files, err := pairArtifactHashes(dir)
	if err != nil {
		return err
	}
	return writePairJSON(filepath.Join(dir, "receipt.json"), PairStageReceipt{Intent: intent, FinishedAt: time.Now().UTC(), Files: files})
}

func VerifyPairStage(c PairConfig, dir string) error {
	var receipt PairStageReceipt
	if err := readPairJSON(filepath.Join(dir, "receipt.json"), &receipt); err != nil {
		return err
	}
	hash, err := pairConfigHash(c)
	if err != nil {
		return err
	}
	if receipt.Intent.ConfigHash != hash || receipt.Intent.SchemaVersion != PairSchema || receipt.FinishedAt.Before(receipt.Intent.StartedAt) {
		return fmt.Errorf("paired stage receipt identity or time is invalid")
	}
	files, err := pairArtifactHashes(dir)
	if err != nil {
		return err
	}
	a, err := canonicalJSONHash(files)
	if err != nil {
		return err
	}
	b, err := canonicalJSONHash(receipt.Files)
	if err != nil {
		return err
	}
	if a != b {
		return fmt.Errorf("paired stage artifacts changed after completion")
	}
	return validatePairStageOutputs(c, receipt.Intent, dir)
}

func pairArtifactHashes(dir string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "receipt.json" {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("paired artifacts must be regular files")
		}
		hash, err := PairFileHash(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = hash
		return nil
	})
	return files, err
}

func validatePairStageOutputs(c PairConfig, intent PairStageIntent, dir string) error {
	r, err := pairRelease(c, intent.Side)
	if err != nil {
		return err
	}
	if intent.Stage == "validate" || intent.Stage == "import" || intent.Stage == "capture" || intent.Stage == "score" {
		var exit struct {
			Code       *int      `json:"exit_code"`
			FinishedAt time.Time `json:"finished_at"`
		}
		if err := readPairJSON(filepath.Join(dir, "native-exit.json"), &exit); err != nil {
			return err
		}
		if exit.Code == nil || *exit.Code != 0 || exit.FinishedAt.Before(intent.StartedAt) {
			return fmt.Errorf("native stage has no successful completion")
		}
	}

	switch intent.Stage {
	case "validate":
		var summary Summary
		if err := readJSONFile(filepath.Join(dir, "native", "summary.json"), &summary); err != nil {
			return err
		}
		if summary.SeedHash != c.SeedHash || summary.Mode != "validate" {
			return fmt.Errorf("native input validation is incompatible")
		}
	case "capacity", "import", "drain", "capture":
		var observation PairObservation
		if err := readPairJSON(filepath.Join(dir, "observation.json"), &observation); err != nil {
			return err
		}
		if observation.Scope != r.Scope {
			return fmt.Errorf("stage observation has a different scope")
		}
		if r.Protocol == "adr0014" && (observation.Policy != r.Capacity || observation.WindowID == "" || observation.Model != r.Models.Ontology) {
			return fmt.Errorf("configured maintenance capacity is not active")
		}
		if intent.Stage == "capacity" {
			if r.Protocol != "none" && (!observation.Enabled || !observation.Paused) {
				return fmt.Errorf("capacity preflight must remain enabled and paused")
			}
			return nil
		}
		_, corpus, _, _, _, _, _, err := loadRunInputs(c.SeedManifest, c.Suite)
		if err != nil {
			return err
		}
		progress, err := EvaluatePairImport(r.Scope, corpus, observation.Imports, false)
		if err != nil {
			return err
		}
		if !progress.Complete {
			return fmt.Errorf("paired import is incomplete")
		}
		if intent.Stage == "import" {
			return validatePairLiveMapping(c, filepath.Join(dir, "native"), observation.Imports)
		}
		imported, err := latestPairStage(c, intent.Side, "import")
		if err != nil {
			return err
		}
		var original PairObservation
		if err := readPairJSON(filepath.Join(imported, "observation.json"), &original); err != nil {
			return err
		}
		if original.CanonicalHash != observation.CanonicalHash || original.SourceHash != observation.SourceHash {
			return fmt.Errorf("ontology processing changed canonical input or import receipts")
		}
		ready, err := EvaluatePairReadiness(r.Protocol, observation, true)
		if err != nil {
			return err
		}
		if !ready.Ready {
			return fmt.Errorf("paired capture is not frozen: %v", ready.WaitingFor)
		}
		if intent.Stage == "capture" {
			var before PairObservation
			if err := readPairJSON(filepath.Join(dir, "before.json"), &before); err != nil {
				return err
			}
			if before.CanonicalHash != observation.CanonicalHash || before.SourceHash != observation.SourceHash || before.WindowID != observation.WindowID {
				return fmt.Errorf("runtime changed during capture")
			}
			if err := ValidatePairCapture(c, filepath.Join(dir, "native")); err != nil {
				return err
			}
		}
	case "score":
		if err := validatePairScore(c, intent.Side, dir); err != nil {
			return err
		}
		if err := ValidatePairCapture(c, filepath.Join(dir, "native")); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown paired stage receipt")
	}
	return nil
}

func validatePairMapping(c PairConfig, dir string) error {
	var mapping KnowledgeMapping
	var config RunConfig
	if err := readJSONFile(filepath.Join(dir, "knowledge_mapping.json"), &mapping); err != nil {
		return err
	}
	if err := readJSONFile(filepath.Join(dir, "run_config.json"), &config); err != nil {
		return err
	}
	hash, err := canonicalJSONHash(mapping)
	if err != nil {
		return err
	}
	if hash != config.MappingHash || config.SeedHash != c.SeedHash {
		return fmt.Errorf("native mapping or seed hash mismatch")
	}
	_, corpus, _, qrels, _, suite, _, err := loadRunInputs(c.SeedManifest, c.Suite)
	if err != nil {
		return err
	}
	wanted := sourceDocIDIndexForCorpus(corpus)
	for id := range wanted {
		if mapping.BySourceDocID[id].ID == "" {
			return fmt.Errorf("missing source mapping %s", id)
		}
	}
	for id := range mapping.BySourceDocID {
		if _, ok := wanted[id]; !ok {
			return fmt.Errorf("unknown source mapping %s", id)
		}
	}
	return validateRequiredQRelMappings(IndexQrels(qrels), suite, mapping)
}

func ValidatePairCapture(c PairConfig, dir string) error {
	if err := validatePairMapping(c, dir); err != nil {
		return err
	}
	_, _, _, _, _, suite, _, err := loadRunInputs(c.SeedManifest, c.Suite)
	if err != nil {
		return err
	}
	var summary Summary
	if err := readJSONFile(filepath.Join(dir, "summary.json"), &summary); err != nil {
		return err
	}
	if summary.SeedHash != c.SeedHash || summary.CaseCount != len(suite) || summary.ScoredCaseCount != len(suite) || summary.UnmappedSourceRefs != 0 {
		return fmt.Errorf("native capture must score the exact cohort with zero unmapped sources")
	}
	traces, err := LoadRecallTraces(filepath.Join(dir, "recall_traces.jsonl"))
	if err != nil {
		return err
	}
	var scores []RetrievalScore
	if err := readJSONL(filepath.Join(dir, "retrieval_scores.jsonl"), &scores); err != nil {
		return err
	}
	wanted, traceIDs, scoreIDs := []string{}, []string{}, []string{}
	for _, row := range suite {
		wanted = append(wanted, row.CaseID)
	}
	for _, row := range traces {
		traceIDs = append(traceIDs, row.CaseID)
	}
	for _, row := range scores {
		scoreIDs = append(scoreIDs, row.CaseID)
	}
	sort.Strings(wanted)
	sort.Strings(traceIDs)
	sort.Strings(scoreIDs)
	w, _ := json.Marshal(wanted)
	a, _ := json.Marshal(traceIDs)
	b, _ := json.Marshal(scoreIDs)
	if string(w) != string(a) || string(w) != string(b) {
		return fmt.Errorf("native capture contains missing, duplicate or unknown cases")
	}
	return nil
}

func validatePairScore(c PairConfig, side, dir string) error {
	release, err := pairRelease(c, side)
	if err != nil {
		return err
	}
	capture := release.CaptureDir
	if capture == "" {
		stage, err := latestPairStage(c, side, "capture")
		if err != nil {
			return err
		}
		capture = filepath.Join(stage, "native")
	}
	proof, err := VerifyPairCaptureProof(c, release, capture)
	if err != nil {
		return err
	}
	var recorded PairCaptureProof
	if err := readPairJSON(filepath.Join(dir, "capture-proof.json"), &recorded); err != nil {
		return err
	}
	a, err := canonicalJSONHash(proof)
	if err != nil {
		return err
	}
	b, err := canonicalJSONHash(recorded)
	if err != nil {
		return err
	}
	if a != b {
		return fmt.Errorf("score input capture identity changed")
	}
	var scorer PairBinary
	if err := readPairJSON(filepath.Join(dir, "scorer.json"), &scorer); err != nil {
		return err
	}
	if scorer != c.Scorer {
		return fmt.Errorf("common scorer identity differs")
	}
	for _, name := range []string{"recall_traces.jsonl", "knowledge_mapping.json"} {
		hash, err := PairFileHash(filepath.Join(dir, "native", name))
		if err != nil {
			return err
		}
		if hash != proof.Files[name] {
			return fmt.Errorf("common scoring changed original %s", name)
		}
	}
	return nil
}

func validatePairLiveMapping(c PairConfig, dir string, imports []PairImportAttempt) error {
	if err := validatePairMapping(c, dir); err != nil {
		return err
	}
	var mapping KnowledgeMapping
	if err := readJSONFile(filepath.Join(dir, "knowledge_mapping.json"), &mapping); err != nil {
		return err
	}
	for _, row := range completedPairImports(imports) {
		ref := mapping.BySourceDocID[row.SourceDocID]
		found := false
		for _, id := range row.EvidenceIDs {
			if id == ref.ID && (ref.Type == "evidence" || ref.Type == "fragment") {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("mapping for %s does not match live import evidence", row.SourceDocID)
		}
	}
	return nil
}

func CompletePairImportMapping(c PairConfig, side, dir string) error {
	if !pairWithin(c.OutDir, dir) {
		return fmt.Errorf("import stage must belong to this paired run")
	}
	release, err := pairRelease(c, side)
	if err != nil {
		return err
	}
	var config RunConfig
	native := filepath.Join(dir, "native")
	if err := readPairJSON(filepath.Join(native, "run_config.json"), &config); err != nil {
		return err
	}
	if config.SeedHash != c.SeedHash || config.Mode != "import" {
		return fmt.Errorf("incompatible native import")
	}
	var mapping KnowledgeMapping
	if err := readPairJSON(filepath.Join(native, "knowledge_mapping.json"), &mapping); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(context.Background(), c.Deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	var observation PairObservation
	if err := readPairJSON(filepath.Join(dir, "observation.json"), &observation); err != nil {
		return err
	}
	_, corpus, _, _, _, _, _, err := loadRunInputs(c.SeedManifest, c.Suite)
	if err != nil {
		return err
	}
	progress, err := EvaluatePairImport(release.Scope, corpus, observation.Imports, false)
	if err != nil {
		return err
	}
	if observation.Scope != release.Scope || !progress.Complete {
		return fmt.Errorf("paired import is incomplete or has a different scope")
	}
	client := HTTPClient{BaseURL: release.Runtime.BaseURL, APIKey: os.Getenv(release.Runtime.APIKeyEnv)}
	evidence, err := client.ExportEvidenceMapping(ctx, 500)
	if err != nil {
		return err
	}
	mergeKnowledgeMapping(&mapping, evidence)
	for _, row := range completedPairImports(observation.Imports) {
		for _, id := range row.EvidenceIDs {
			addSourceMapping(&mapping, Ref{Type: "evidence", ID: id, SourceDocID: row.SourceDocID}, true)
		}
	}
	config.MappingHash, err = canonicalJSONHash(mapping)
	if err != nil {
		return err
	}
	if err := writePairJSON(filepath.Join(native, "knowledge_mapping.json"), mapping); err != nil {
		return err
	}
	return writePairJSON(filepath.Join(native, "run_config.json"), config)
}
