package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"reflect"

	"github.com/markhuangai/dense-mem/internal/observability"
)

func loadExecutionEvents(directory string) ([]executionEvent, error) {
	var events []executionEvent
	err := readJSONL(filepath.Join(directory, measurementJournal), &events)
	return events, err
}

func executionOutcomes(plan ExecutionMeasurements, events []executionEvent, complete bool) (map[string]int, error) {
	outcomes := map[string]int{}
	planned := map[string]bool{}
	for kind, ids := range map[string][]string{"import": plan.Imports, "recall": plan.Cases} {
		for _, id := range ids {
			key := kind + ":" + id
			if id == "" || planned[key] {
				return nil, fmt.Errorf("duplicate or missing planned work identity")
			}
			planned[key] = true
		}
	}
	operations, attempts := map[string]executionEvent{}, map[string]executionEvent{}
	terminalOps, terminalAttempts, work, traces := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	ended := false
	for index, event := range events {
		if event.Sequence != index+1 || event.CaptureID != plan.CaptureID || event.RunID != plan.RunID || event.At.IsZero() || ended {
			return nil, fmt.Errorf("execution journal sequence, identity or terminal order mismatch")
		}
		switch event.Event {
		case "run_started":
			if index != 0 {
				return nil, fmt.Errorf("duplicate execution start")
			}
		case "operation_started":
			if event.OperationID == "" || operations[event.OperationID].OperationID != "" {
				return nil, fmt.Errorf("duplicate or missing operation identity")
			}
			if event.Kind != "setup" {
				key := event.Kind + ":" + event.ItemID
				if !planned[key] || work[key] {
					return nil, fmt.Errorf("unexpected or duplicate work identity %s", key)
				}
				work[key] = true
			}
			operations[event.OperationID] = event
		case "operation_finished":
			op := operations[event.OperationID]
			if op.OperationID == "" || terminalOps[event.OperationID] || !sameOperation(op, event) || !validElapsed(event.ElapsedMS) || !validExecutionOutcome(event.Outcome) {
				return nil, fmt.Errorf("invalid or duplicate operation outcome")
			}
			if event.Outcome == "reused" && event.Kind != "import" {
				return nil, fmt.Errorf("only an imported source can reuse a durable receipt")
			}
			terminalOps[event.OperationID] = true
			operations[event.OperationID] = event
		case "attempt_started":
			op := operations[event.OperationID]
			if op.OperationID == "" || terminalOps[event.OperationID] || !sameOperation(op, event) || event.AttemptID == "" || attempts[event.AttemptID].AttemptID != "" {
				return nil, fmt.Errorf("invalid or duplicate attempt identity")
			}
			attempts[event.AttemptID] = event
		case "attempt_finished":
			first := attempts[event.AttemptID]
			if first.AttemptID == "" || terminalAttempts[event.AttemptID] || !sameOperation(first, event) || first.Method != event.Method || !validElapsed(event.ElapsedMS) || !validExecutionOutcome(event.Outcome) || event.Outcome == "reused" {
				return nil, fmt.Errorf("invalid or duplicate attempt outcome")
			}
			terminalAttempts[event.AttemptID] = true
			attempts[event.AttemptID] = event
		case "retry_scheduled":
			op := operations[event.OperationID]
			if op.OperationID == "" || terminalOps[event.OperationID] || !sameOperation(op, event) || event.RetryDelayMS <= 0 {
				return nil, fmt.Errorf("invalid retry record")
			}
		case "trace_saved":
			op := operations[event.OperationID]
			if !terminalOps[event.OperationID] || op.Kind != "recall" || op.Outcome != "completed" || op.ItemID != event.ItemID || traces[event.ItemID] || event.TraceHash == "" {
				return nil, fmt.Errorf("invalid or duplicate saved trace")
			}
			traces[event.ItemID] = true
		case "run_finished":
			if !validExecutionOutcome(event.Outcome) || event.Outcome == "reused" {
				return nil, fmt.Errorf("invalid execution outcome")
			}
			ended = true
			if complete && event.Outcome != "completed" {
				return nil, fmt.Errorf("execution did not complete")
			}
		default:
			return nil, fmt.Errorf("unknown execution event %q", event.Event)
		}
		if index == 0 && event.Event != "run_started" {
			return nil, fmt.Errorf("execution start is missing")
		}
	}
	for id, op := range operations {
		state := "unfinished"
		if terminalOps[id] {
			state = op.Outcome
		}
		outcomes[op.Kind+"_"+state]++
		if complete && (!terminalOps[id] || (state != "completed" && state != "reused")) {
			return nil, fmt.Errorf("operation %s is %s", id, state)
		}
		if complete && op.Kind == "recall" && !traces[op.ItemID] {
			return nil, fmt.Errorf("completed recall has no saved trace")
		}
	}
	for id, attempt := range attempts {
		state := "unfinished"
		if terminalAttempts[id] {
			state = attempt.Outcome
		}
		outcomes["attempt_"+state]++
		if complete && !terminalAttempts[id] {
			return nil, fmt.Errorf("unfinished HTTP attempt")
		}
	}
	for key := range planned {
		if !work[key] {
			outcomes["not_dispatched"]++
			if complete {
				return nil, fmt.Errorf("scheduled work %s was not dispatched", key)
			}
		}
	}
	if complete && (!ended || len(events) == 0) {
		return nil, fmt.Errorf("execution terminal record is missing")
	}
	return outcomes, nil
}

func sameOperation(a, b executionEvent) bool {
	return a.OperationID == b.OperationID && a.Kind == b.Kind && a.ItemID == b.ItemID && a.Phase == b.Phase
}
func validElapsed(value *float64) bool {
	return value != nil && *value >= 0 && !math.IsNaN(*value) && !math.IsInf(*value, 0)
}
func validExecutionOutcome(value string) bool {
	return value == "completed" || value == "failed" || value == "cancelled" || value == "reused"
}

// ValidateExecutionCapture preserves legacy captures and requires complete sealed measurements when provenance is present.
func ValidateExecutionCapture(directory string, loggers ...*observability.Logger) (err error) {
	defer func() {
		if err != nil {
			var logger *observability.Logger
			if len(loggers) > 0 {
				logger = loggers[0]
			}
			logCaptureRejection(logger, directory, "", 0, err)
		}
	}()
	return validateExecutionCapture(directory)
}

// ValidateSavedExecutionCapture distinguishes saved runs from standalone mapping inputs.
func ValidateSavedExecutionCapture(directory string, loggers ...*observability.Logger) error {
	_, err := readSavedRunConfig(directory)
	if err == nil {
		return ValidateExecutionCapture(directory, loggers...)
	}
	err = fmt.Errorf("capture run config: %w", err)
	var logger *observability.Logger
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	logCaptureRejection(logger, directory, "", 0, err)
	return err
}

func readSavedRunConfig(directory string) (*RunConfig, error) {
	path := filepath.Join(directory, "run_config.json")
	var config *RunConfig
	if err := readJSONFile(path, &config); err != nil {
		return nil, err
	}
	if config == nil {
		return nil, errors.New("run config must be a JSON object")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, errors.New("run config must contain one complete JSON object")
	}
	return config, nil
}

func validateExecutionCapture(directory string) error {
	var config RunConfig
	configErr := readJSONFile(filepath.Join(directory, "run_config.json"), &config)
	traces, traceErr := LoadRecallTraces(filepath.Join(directory, "recall_traces.jsonl"))
	measured := config.MeasurementSchema != "" || hasMeasuredTraces(traces)
	if _, err := os.Stat(filepath.Join(directory, measurementManifest)); err == nil {
		measured = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !measured {
		return nil
	}
	if configErr != nil {
		return fmt.Errorf("measurement run config: %w", configErr)
	}
	if traceErr != nil {
		return fmt.Errorf("measurement traces: %w", traceErr)
	}
	var plan ExecutionMeasurements
	if err := readJSONFile(filepath.Join(directory, measurementManifest), &plan); err != nil {
		return fmt.Errorf("measurement manifest: %w", err)
	}
	if plan.SchemaVersion != MeasurementSchema || plan.CaptureID == "" || plan.RunID == "" || plan.Status != "completed" || plan.SeedHash != config.SeedHash {
		return fmt.Errorf("measurement provenance is incompatible or incomplete")
	}
	names := measurementArtifactNames(plan)
	if len(plan.Artifacts) != len(names) {
		return fmt.Errorf("measurement artifact inventory is incomplete or unexpected")
	}
	for _, name := range names {
		expected := plan.Artifacts[name]
		hash, err := PairFileHash(filepath.Join(directory, name))
		if err != nil {
			return fmt.Errorf("measurement artifact %s: %w", name, err)
		}
		if expected == "" || hash != expected {
			return fmt.Errorf("measurement artifact hash mismatch: %s expected %s observed %s", name, expected, hash)
		}
	}
	if plan.Artifacts["suite.jsonl"] != plan.SuiteHash {
		return fmt.Errorf("measurement suite identity mismatch")
	}

	events, err := loadExecutionEvents(directory)
	if err != nil {
		return err
	}
	outcomes, err := executionOutcomes(plan, events, true)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(outcomes, plan.Outcomes) {
		return fmt.Errorf("measurement outcome counts differ from journal")
	}
	suite, err := LoadSuite(filepath.Join(directory, "suite.jsonl"))
	if err != nil {
		return err
	}
	if len(plan.Cases) > 0 {
		if len(suite) != len(plan.Cases) || len(traces) != len(plan.Cases) {
			return fmt.Errorf("measurement cohort is incomplete")
		}
		for index, row := range suite {
			if row.CaseID != plan.Cases[index] {
				return fmt.Errorf("measurement scheduled cohort differs from suite")
			}
		}
	}
	operations, hashes := map[string]executionEvent{}, map[string]string{}
	for _, event := range events {
		if event.Event == "operation_finished" {
			operations[event.OperationID] = event
		}
		if event.Event == "trace_saved" {
			hashes[event.ItemID] = event.TraceHash
		}
	}
	seen := map[string]bool{}
	for _, trace := range traces {
		op := operations[trace.ClientOperationID]
		hash, err := canonicalJSONHash(trace)
		if err != nil {
			return err
		}
		if seen[trace.CaseID] || trace.MeasurementCaptureID != plan.CaptureID || op.Kind != "recall" || op.ItemID != trace.CaseID || !validElapsed(trace.ClientElapsedMS) || *trace.ClientElapsedMS != *op.ElapsedMS || trace.ClientPhase != op.Phase || hash != hashes[trace.CaseID] {
			return fmt.Errorf("trace measurement provenance mismatch: %s", trace.CaseID)
		}
		seen[trace.CaseID] = true
	}
	return nil
}

func hasMeasuredTraces(traces []RecallTrace) bool {
	for _, trace := range traces {
		if trace.MeasurementCaptureID != "" {
			return true
		}
	}
	return false
}

func validateMeasurementReplay(opts RunOptions, manifest *SeedManifest, traces []RecallTrace, suite []SuiteCase, seedHash string) (err error) {
	directory := filepath.Dir(opts.TracesPath)
	defer func() {
		if err != nil {
			logCaptureRejection(opts.Logger, directory, seedHash, len(suite), err)
		}
	}()
	if err := validateExecutionCapture(directory); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(directory, measurementManifest)); errors.Is(err, os.ErrNotExist) && !hasMeasuredTraces(traces) {
		return nil
	}

	var plan ExecutionMeasurements
	if err := readJSONFile(filepath.Join(directory, measurementManifest), &plan); err != nil {
		return err
	}
	selectedHash, err := PairFileHash(opts.TracesPath)
	if err != nil {
		return err
	}
	if selectedHash != plan.Artifacts["recall_traces.jsonl"] {
		return fmt.Errorf("selected trace artifact hash mismatch: expected %s observed %s", plan.Artifacts["recall_traces.jsonl"], selectedHash)
	}
	suiteHash, err := PairFileHash(opts.SuitePath)
	if err != nil {
		return err
	}
	mappingHash, err := PairFileHash(opts.MappingPath)
	if err != nil {
		return fmt.Errorf("measured replay requires the original --mapping: %w", err)
	}
	if seedHash != plan.SeedHash || suiteHash != plan.SuiteHash || mappingHash != plan.Artifacts["knowledge_mapping.json"] {
		return fmt.Errorf("measured replay seed, suite or mapping mismatch")
	}
	if opts.OutDir != "" {
		names := append(measurementArtifactNames(plan), measurementManifest, "seed_manifest.json")
		sourceNames := append(append([]string(nil), names...), "gate_result.json", "release_gate_result.json", "release_gate_input_result.json")
		if opts.Gates.Any() {
			names = append(names, "gate_result.json")
		}
		if opts.ReleaseGatePolicyPath != "" {
			names = append(names, "release_gate_result.json", "release_gate_input_result.json")
		}
		var sourceFiles []os.FileInfo
		for _, name := range sourceNames {
			info, err := os.Stat(filepath.Join(directory, name))
			if errors.Is(err, os.ErrNotExist) && plan.Artifacts[name] == "" && name != measurementManifest {
				continue
			}
			if err != nil {
				return err
			}
			sourceFiles = append(sourceFiles, info)
		}
		inputPaths := append(seedArtifactPaths(opts.SeedManifestPath, manifest), opts.SuitePath, opts.TracesPath, opts.MappingPath)
		if opts.ReleaseGatePolicyPath != "" {
			inputPaths = append(inputPaths, opts.ReleaseGatePolicyPath)
		}
		for _, path := range inputPaths {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			sourceFiles = append(sourceFiles, info)
		}
		for _, name := range names {
			outputInfo, err := os.Stat(filepath.Join(opts.OutDir, name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			for _, sourceInfo := range sourceFiles {
				if os.SameFile(sourceInfo, outputInfo) {
					return fmt.Errorf("measured replay requires a separate output directory")
				}
			}
		}
	}
	return nil
}

func copyMeasurementReplay(opts RunOptions, traces []RecallTrace) error {
	if !hasMeasuredTraces(traces) {
		return nil
	}
	for _, name := range []string{measurementJournal, "recall_traces.jsonl", "knowledge_mapping.json", "suite.jsonl"} {
		source := filepath.Join(filepath.Dir(opts.TracesPath), name)
		if name == "recall_traces.jsonl" {
			source = opts.TracesPath
		}
		body, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(opts.OutDir, name), body, 0o600); err != nil {
			return err
		}
	}
	var plan ExecutionMeasurements
	if err := readJSONFile(filepath.Join(filepath.Dir(opts.TracesPath), measurementManifest), &plan); err != nil {
		return err
	}
	if err := sealMeasurementArtifacts(opts.OutDir, &plan, true); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(opts.OutDir, measurementManifest), plan)
}

func logCaptureRejection(logger *observability.Logger, directory, expectedSeed string, expectedCases int, cause error) {
	if logger == nil {
		logger = observability.New(slog.LevelInfo)
	}
	config, configErr := readSavedRunConfig(directory)
	if configErr != nil {
		config = &RunConfig{}
	}
	var plan ExecutionMeasurements
	planErr := readJSONFile(filepath.Join(directory, measurementManifest), &plan)
	traces, traceErr := LoadRecallTraces(filepath.Join(directory, "recall_traces.jsonl"))
	observed := []string{}
	for _, trace := range traces {
		observed = append(observed, trace.CaseID)
	}
	observations := []string{}
	for _, err := range []error{configErr, planErr, traceErr} {
		if err != nil {
			observations = append(observations, err.Error())
		}
	}

	if expectedSeed == "" {
		expectedSeed = firstNonEmpty(plan.SeedHash, config.SeedHash)
	}
	expectedIDs := plan.Cases
	casesAvailable := expectedCases > 0 || plan.Cases != nil
	if !casesAvailable {
		if suite, err := LoadSuite(filepath.Join(directory, "suite.jsonl")); err == nil {
			casesAvailable = true
			expectedIDs = []string{}
			for _, row := range suite {
				expectedIDs = append(expectedIDs, row.CaseID)
			}
		}
	}
	if expectedCases == 0 && casesAvailable {
		expectedCases = len(expectedIDs)
	}
	logger.ErrorContext(context.Background(), "evaluation_capture_invalid", cause,
		observability.String("run_id", firstNonEmpty(plan.RunID, config.RunID, "unknown")), observability.String("capture_id", firstNonEmpty(plan.CaptureID, "unknown")),
		observability.String("capture_directory", directory), observability.String("expected_seed_hash", expectedSeed), observability.Int("expected_cases", expectedCases), observability.Bool("expected_cases_available", casesAvailable), observability.Bool("expected_seed_available", expectedSeed != ""), observability.LogAttr{Key: "expected_case_ids", Value: expectedIDs},
		observability.String("observed_seed_hash", config.SeedHash), observability.String("observed_capture_status", plan.Status), observability.LogAttr{Key: "observed_case_ids", Value: observed}, observability.LogAttr{Key: "observation_errors", Value: observations})
}

func measurementArtifactNames(plan ExecutionMeasurements) []string {
	names := []string{measurementJournal, "recall_traces.jsonl", "knowledge_mapping.json", "suite.jsonl", "run_config.json", "summary.json"}
	if len(plan.Cases) > 0 {
		names = append(names, "retrieval_scores.jsonl")
	}
	return names
}

func sealMeasurementArtifacts(directory string, plan *ExecutionMeasurements, required bool) error {
	plan.Artifacts = map[string]string{}
	for _, name := range measurementArtifactNames(*plan) {
		hash, err := PairFileHash(filepath.Join(directory, name))
		if errors.Is(err, os.ErrNotExist) && !required {
			continue
		}
		if err != nil {
			return fmt.Errorf("seal measurement artifact %s: %w", name, err)
		}
		plan.Artifacts[name] = hash
	}
	if hash := plan.Artifacts["suite.jsonl"]; hash != "" && hash != plan.SuiteHash {
		return fmt.Errorf("measurement suite changed during capture")
	}
	return nil
}
