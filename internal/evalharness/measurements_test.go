package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func measurementFixture(t *testing.T) (RunOptions, *bytes.Buffer) {
	t.Helper()
	root := writeEvalFixture(t)
	mappingPath := filepath.Join(root, "mapping.json")
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "frag-alpha", SourceDocID: "doc-alpha"}, true)
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "frag-beta", SourceDocID: "doc-beta"}, true)
	require.NoError(t, writeJSONFile(mappingPath, mapping))
	var output bytes.Buffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return RunOptions{Mode: "candidate", SeedManifestPath: filepath.Join(root, "seed_manifest.json"), SuitePath: filepath.Join(root, "suite.jsonl"), MappingPath: mappingPath, OutDir: filepath.Join(root, "capture"), RunID: "measured-fixture", APIKey: "private-measurement-credential", Logger: logger}, &output
}

func measurementServer(t *testing.T, handle func(http.ResponseWriter, *http.Request, map[string]any)) *httptest.Server {
	t.Helper()
	return newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		handle(w, r, input)
	}))
}

func measurementRecall(w http.ResponseWriter, input map[string]any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"case_id": input["case_id"], "query": input["query"], "latency_ms": 1, "ranked_refs": []map[string]any{{"type": "fragment", "id": "frag-alpha"}}})
}

func TestExecutionMeasurementsSuccessRetryAndExactReplay(t *testing.T) {
	opts, logs := measurementFixture(t)
	var calls atomic.Int32
	server := measurementServer(t, func(w http.ResponseWriter, r *http.Request, input map[string]any) {
		require.NotEmpty(t, r.Header.Get("X-Correlation-ID"))
		if calls.Add(1) == 1 {
			http.Error(w, "recoverable provider failure", http.StatusServiceUnavailable)
			return
		}
		measurementRecall(w, input)
	})
	defer server.Close()
	opts.BaseURL = server.URL
	summary, err := Run(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, 2, summary.ScoredCaseCount)
	require.NoError(t, ValidateExecutionCapture(opts.OutDir))
	require.NoError(t, ValidateSavedExecutionCapture(opts.OutDir))
	traces, err := LoadRecallTraces(filepath.Join(opts.OutDir, "recall_traces.jsonl"))
	require.NoError(t, err)
	require.Len(t, traces, 2)
	require.GreaterOrEqual(t, *traces[0].ClientElapsedMS, 750.0)
	require.Equal(t, int64(1), traces[0].LatencyMS)
	require.Equal(t, "first_query", traces[0].ClientPhase)
	require.Equal(t, "subsequent_query", traces[1].ClientPhase)
	var plan ExecutionMeasurements
	require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, measurementManifest), &plan))
	require.Equal(t, 1, plan.Outcomes["attempt_failed"])
	require.Equal(t, 2, plan.Outcomes["attempt_completed"])
	original, err := pairArtifactHashes(opts.OutDir)
	require.NoError(t, err)
	replay := opts
	replay.TracesPath = filepath.Join(opts.OutDir, "recall_traces.jsonl")
	replay.MappingPath = filepath.Join(opts.OutDir, "knowledge_mapping.json")
	replay.OutDir = filepath.Join(filepath.Dir(opts.OutDir), "replay")
	_, err = Run(context.Background(), replay)
	require.NoError(t, err)
	require.Equal(t, int32(3), calls.Load())
	require.NoError(t, ValidateExecutionCapture(replay.OutDir))
	require.NoError(t, ValidateSavedExecutionCapture(replay.OutDir))
	require.Equal(t, int32(3), calls.Load())
	after, err := pairArtifactHashes(opts.OutDir)
	require.NoError(t, err)
	require.Equal(t, original, after)
	require.Contains(t, logs.String(), "evaluation_retry_scheduled")
	require.Contains(t, logs.String(), "recoverable provider failure")
	require.Contains(t, logs.String(), "ai_exchange_payload")
	final, level := measurementFinalLog(t, logs)
	require.Equal(t, "INFO", level)
	require.Equal(t, 2, final.Counts["recall_completed"])
	require.Equal(t, "recall:case-2", final.LastCompleted)
}

func TestExecutionMeasurementsQualityRejectionsRemainReplayable(t *testing.T) {
	for _, gate := range []string{"threshold", "release"} {
		t.Run(gate, func(t *testing.T) {
			opts, logs := measurementFixture(t)
			var calls atomic.Int32
			server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) {
				calls.Add(1)
				measurementRecall(w, input)
			})
			defer server.Close()
			opts.BaseURL = server.URL
			gateFile := "gate_result.json"
			if gate == "threshold" {
				minimum := 1.0
				opts.Gates.MinRecallAtK = &minimum
			} else {
				manifest, err := LoadSeedManifest(opts.SeedManifestPath)
				require.NoError(t, err)
				seedHash, err := SeedHash(opts.SeedManifestPath, manifest)
				require.NoError(t, err)
				policy := releaseGateTestPolicy()
				policy.SeedID, policy.SeedHash = manifest.SeedID, seedHash
				opts.ReleaseGatePolicyPath = filepath.Join(t.TempDir(), "policy.json")
				require.NoError(t, writeJSONFile(opts.ReleaseGatePolicyPath, policy))
				gateFile = "release_gate_result.json"
			}
			summary, err := Run(context.Background(), opts)
			require.ErrorContains(t, err, "gate check failed")
			require.Equal(t, 2, summary.ScoredCaseCount)
			require.Less(t, summary.AverageRecallAtK, 1.0)
			require.NoError(t, ValidateExecutionCapture(opts.OutDir))
			var rejected struct {
				Passed bool `json:"passed"`
			}
			require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, gateFile), &rejected))
			require.False(t, rejected.Passed)
			final, level := measurementFinalLog(t, logs)
			require.Equal(t, "INFO", level)
			require.Equal(t, "completed", final.Outcome)
			require.Equal(t, 2, final.Counts["recall_completed"])
			original, err := pairArtifactHashes(opts.OutDir)
			require.NoError(t, err)
			replay := opts
			replay.Gates, replay.ReleaseGatePolicyPath = GateOptions{}, ""
			replay.TracesPath = filepath.Join(opts.OutDir, "recall_traces.jsonl")
			replay.MappingPath = filepath.Join(opts.OutDir, "knowledge_mapping.json")
			replay.OutDir = filepath.Join(t.TempDir(), "replay")
			_, err = Run(context.Background(), replay)
			require.NoError(t, err)
			require.NoError(t, ValidateExecutionCapture(replay.OutDir))
			_, err = CompareRunDirs(opts.OutDir, replay.OutDir, t.TempDir(), opts.Logger)
			require.NoError(t, err)
			after, err := pairArtifactHashes(opts.OutDir)
			require.NoError(t, err)
			require.Equal(t, original, after)
			require.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestExecutionMeasurementsRetainPartialFailureAndUnscheduledWork(t *testing.T) {
	opts, logs := measurementFixture(t)
	var cases []Case
	require.NoError(t, readJSONL(filepath.Join(filepath.Dir(opts.SeedManifestPath), "cases.jsonl"), &cases))
	third := cases[1]
	third.CaseID = "case-3"
	cases = append(cases, third)
	require.NoError(t, writeJSONL(filepath.Join(filepath.Dir(opts.SeedManifestPath), "cases.jsonl"), cases))
	var qrels []QRel
	require.NoError(t, readJSONL(filepath.Join(filepath.Dir(opts.SeedManifestPath), "qrels.jsonl"), &qrels))
	extra := qrels[1]
	extra.CaseID = third.CaseID
	qrels = append(qrels, extra)
	require.NoError(t, writeJSONL(filepath.Join(filepath.Dir(opts.SeedManifestPath), "qrels.jsonl"), qrels))
	require.NoError(t, writeJSONL(opts.SuitePath, []SuiteCase{{CaseID: "case-1"}, {CaseID: "case-2"}, {CaseID: "case-3"}}))
	var manifest SeedManifest
	require.NoError(t, readJSONFile(opts.SeedManifestPath, &manifest))
	if manifest.Counts == nil {
		manifest.Counts = map[string]int{}
	}
	manifest.Counts["cases"] = 3
	manifest.Counts["qrels"] = 3
	require.NoError(t, writeJSONFile(opts.SeedManifestPath, manifest))
	var calls atomic.Int32
	server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) {
		calls.Add(1)
		if input["case_id"] == "case-2" {
			_, _ = w.Write([]byte("{"))
			return
		}
		measurementRecall(w, input)
	})
	defer server.Close()
	opts.BaseURL = server.URL
	_, err := Run(context.Background(), opts)
	require.Error(t, err)
	require.Equal(t, int32(2), calls.Load())
	traces, err := LoadRecallTraces(filepath.Join(opts.OutDir, "recall_traces.jsonl"))
	require.NoError(t, err)
	require.Len(t, traces, 1)
	var plan ExecutionMeasurements
	require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, measurementManifest), &plan))
	require.Equal(t, "incomplete", plan.Status)
	require.Equal(t, 1, plan.Outcomes["recall_completed"])
	require.Equal(t, 1, plan.Outcomes["recall_failed"])
	require.Equal(t, 1, plan.Outcomes["not_dispatched"])
	require.ErrorContains(t, ValidateExecutionCapture(opts.OutDir), "incomplete")
	require.Contains(t, logs.String(), "evaluation_operation_finished")
	require.Contains(t, logs.String(), "case-2")
	require.Contains(t, logs.String(), "unexpected EOF")
	final, level := measurementFinalLog(t, logs)
	require.Equal(t, "ERROR", level)
	require.Equal(t, 1, final.Counts["recall_failed"])
	require.Equal(t, 1, final.Counts["not_dispatched"])
	require.Equal(t, "recall:case-1", final.LastCompleted)
	t.Log(logs.String())
}

func TestExecutionMeasurementsCancellationAndInterruptedIntent(t *testing.T) {
	for _, during := range []string{"request", "backoff"} {
		t.Run(during, func(t *testing.T) {
			opts, logs := measurementFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			requested := make(chan struct{}, 1)
			server := measurementServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				requested <- struct{}{}
				if during == "backoff" {
					http.Error(w, "retry later", http.StatusServiceUnavailable)
					return
				}
				<-r.Context().Done()
			})
			defer server.Close()
			opts.BaseURL = server.URL
			finished := make(chan error, 1)
			go func() { _, err := Run(ctx, opts); finished <- err }()
			<-requested
			if during == "backoff" {
				require.Eventually(t, func() bool {
					body, _ := os.ReadFile(filepath.Join(opts.OutDir, measurementJournal))
					return bytes.Contains(body, []byte("retry_scheduled"))
				}, 2*time.Second, 5*time.Millisecond)
			}
			cancel()
			require.ErrorIs(t, <-finished, context.Canceled)
			var plan ExecutionMeasurements
			require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, measurementManifest), &plan))
			require.Equal(t, 1, plan.Outcomes["recall_cancelled"])
			final, level := measurementFinalLog(t, logs)
			require.Equal(t, "ERROR", level)
			require.Equal(t, 1, final.Counts["recall_cancelled"])
			require.Equal(t, "none", final.LastCompleted)
			t.Log(logs.String())
		})
	}
	opts, _ := measurementFixture(t)
	journal, err := newExecutionJournal(opts, RunConfig{RunID: opts.RunID, Mode: opts.Mode}, nil, []SuiteCase{{CaseID: "case-1"}})
	require.NoError(t, err)
	_, op, err := journal.start(context.Background(), "recall", "case-1")
	require.NoError(t, err)
	require.NotNil(t, op)
	require.NoError(t, journal.file.Close())
	require.NoError(t, journal.traces.Close())
	events, err := loadExecutionEvents(opts.OutDir)
	require.NoError(t, err)
	outcomes, err := executionOutcomes(journal.manifest, events, false)
	require.NoError(t, err)
	require.Equal(t, 1, outcomes["recall_unfinished"])
	_, err = executionOutcomes(journal.manifest, events, true)
	require.ErrorContains(t, err, "unfinished")
}

func TestExecutionMeasurementsConcurrentImportsAndPersistenceBarrier(t *testing.T) {
	opts, _ := measurementFixture(t)
	journal, err := newExecutionJournal(opts, RunConfig{RunID: opts.RunID, Mode: "import"}, nil, nil)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for index := 0; index < 12; index++ {
		wg.Go(func() {
			_, op, err := journal.start(context.Background(), "setup", "export")
			require.NoError(t, err)
			require.NoError(t, op.finish(nil, "completed"))
		})
	}
	wg.Wait()
	require.NoError(t, writeJSONFile(filepath.Join(opts.OutDir, "knowledge_mapping.json"), newKnowledgeMapping()))
	require.NoError(t, writeJSONFile(filepath.Join(opts.OutDir, "summary.json"), Summary{RunID: opts.RunID, Mode: "import"}))
	require.NoError(t, journal.finish(nil))
	events, err := loadExecutionEvents(opts.OutDir)
	require.NoError(t, err)
	require.Len(t, events, 26)
	opts, _ = measurementFixture(t)
	journal, err = newExecutionJournal(opts, RunConfig{RunID: opts.RunID, Mode: opts.Mode}, nil, nil)
	require.NoError(t, err)
	require.NoError(t, journal.file.Close())
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := &HTTPClient{BaseURL: server.URL, APIKey: opts.APIKey, journal: journal}
	_, err = client.RunRecallCase(context.Background(), Case{CaseID: "case-1", Query: "question"})
	require.Error(t, err)
	require.Zero(t, calls.Load())
	require.Error(t, journal.finish(errors.New("persistence failed")))
}

func TestValidateSavedExecutionCapture(t *testing.T) {
	for _, test := range []struct {
		body  string
		valid bool
	}{
		{`{}`, true}, {`{"mode":"import","legacy_field":true}`, true}, {"{}\n \t", true},
		{`null`, false}, {`[]`, false}, {`"config"`, false}, {`1`, false}, {`true`, false}, {``, false},
		{`{}garbage`, false}, {`{} {}`, false}, {`{"run_id":"partial-run","mode":false}`, false},
	} {
		t.Run(test.body, func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "run_config.json"), []byte(test.body), 0o600))
			err := ValidateSavedExecutionCapture(directory)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "capture run config")
			}
		})
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "standalone_mapping.json")
	body := []byte(`{"by_source_doc_id":{}}`)
	require.NoError(t, os.WriteFile(path, body, 0o600))
	require.NoError(t, ValidateExecutionCapture(directory))
	require.ErrorContains(t, ValidateSavedExecutionCapture(directory), "capture run config")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, body, after)
}

func TestExecutionMeasurementsRejectCorruptionAndCaseSet(t *testing.T) {
	for _, corruption := range []string{"remove_journal", "duplicate_event", "missing_trace", "wrong_mapping", "unknown_case", "timing", "duplicate_trace"} {
		t.Run(corruption, func(t *testing.T) {
			opts, _ := measurementFixture(t)
			var calls atomic.Int32
			server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) {
				calls.Add(1)
				measurementRecall(w, input)
			})
			defer server.Close()
			opts.BaseURL = server.URL
			_, err := Run(context.Background(), opts)
			require.NoError(t, err)
			path := filepath.Join(opts.OutDir, measurementJournal)
			switch corruption {
			case "remove_journal":
				require.NoError(t, os.Remove(path))
			case "duplicate_event":
				body, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, append(body, bytes.Split(body, []byte("\n"))[0]...), 0o600))
			case "wrong_mapping":
				require.NoError(t, os.WriteFile(filepath.Join(opts.OutDir, "knowledge_mapping.json"), []byte("{}"), 0o600))
			default:
				traces, err := LoadRecallTraces(filepath.Join(opts.OutDir, "recall_traces.jsonl"))
				require.NoError(t, err)
				switch corruption {
				case "missing_trace":
					traces = traces[:1]
				case "unknown_case":
					traces[0].CaseID = "unknown"
				case "timing":
					*traces[0].ClientElapsedMS = 0
				case "duplicate_trace":
					traces = append(traces, traces[0])
				}
				require.NoError(t, writeJSONL(filepath.Join(opts.OutDir, "recall_traces.jsonl"), traces))
			}
			original, err := pairArtifactHashes(opts.OutDir)
			require.NoError(t, err)
			beforeCalls := calls.Load()
			require.Error(t, ValidateExecutionCapture(opts.OutDir))
			require.Error(t, ValidateSavedExecutionCapture(opts.OutDir))
			_, err = CompareRunDirs(opts.OutDir, opts.OutDir, "")
			require.Error(t, err)
			after, err := pairArtifactHashes(opts.OutDir)
			require.NoError(t, err)
			require.Equal(t, original, after)
			require.Equal(t, beforeCalls, calls.Load())
		})
	}
	suite := []SuiteCase{{CaseID: "a"}}
	for _, traces := range [][]RecallTrace{nil, {{CaseID: "a"}, {CaseID: "a"}}, {{CaseID: "a"}, {CaseID: "other"}}} {
		_, _, err := ScoreTraces("run", "candidate", "seed", "hash", "suite", suite, map[string]Case{"a": {Limit: 10}}, map[string]QRel{"a": {RequiredRefs: []Ref{{Type: "evidence", ID: "one"}}}}, traces, newKnowledgeMapping())
		require.Error(t, err)
	}
}

func TestMeasurementLogsProtectCredentialsAndReconstructFailure(t *testing.T) {
	opts, output := measurementFixture(t)
	const password = "private-url-credential+680/one"
	const configured = "private-existing-configured-credential"
	opts.Protector = observability.NewCredentialProtector(configured)
	var calls int
	server := measurementServer(t, func(w http.ResponseWriter, r *http.Request, input map[string]any) {
		require.Equal(t, "Bearer "+opts.APIKey, r.Header.Get("Authorization"))
		calls++
		if calls == 1 {
			http.Error(w, fmt.Sprintf("admitted failure %s %s %s; preserve this cause", opts.APIKey, password, configured), http.StatusServiceUnavailable)
			return
		}
		measurementRecall(w, input)
	})
	defer server.Close()
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	address.User = url.UserPassword("fixture-user", password)
	opts.BaseURL = address.String()
	_, err = Run(context.Background(), opts)
	require.NoError(t, err)
	require.NotContains(t, output.String(), opts.APIKey)
	require.NotContains(t, output.String(), password)
	require.NotContains(t, output.String(), configured)
	require.NotContains(t, output.String(), "fixture-user")
	require.NotContains(t, output.String(), "Authorization")
	var config RunConfig
	require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, "run_config.json"), &config))
	require.Equal(t, server.URL, config.BaseURL)
	require.NoError(t, ValidateExecutionCapture(opts.OutDir))
	chunks := map[string]map[int]observability.DiagnosticBodyChunk{}
	for _, line := range strings.Split(output.String(), "\n") {
		var event struct {
			Level     string                            `json:"level"`
			Message   string                            `json:"msg"`
			RunID     string                            `json:"run_id"`
			Direction string                            `json:"direction"`
			AttemptID string                            `json:"attempt_id"`
			Payload   observability.DiagnosticBodyChunk `json:"payload_chunk"`
		}
		if json.Unmarshal([]byte(line), &event) != nil || event.RunID != opts.RunID || event.Message != "ai_exchange_payload" {
			continue
		}
		key := event.AttemptID + ":" + event.Direction
		if chunks[key] == nil {
			chunks[key] = map[int]observability.DiagnosticBodyChunk{}
		}
		chunks[key][event.Payload.Index] = event.Payload
	}
	recovered := false
	for _, parts := range chunks {
		var body strings.Builder
		for index := 0; index < len(parts); index++ {
			body.WriteString(parts[index].Body)
		}
		if strings.Contains(body.String(), "preserve this cause") {
			recovered = true
			require.Contains(t, body.String(), "[REDACTED]")
		}
	}
	require.True(t, recovered, "DEBUG logs alone must reconstruct the failed response and corrective retry")
	require.Contains(t, output.String(), `"level":"WARN"`)
	t.Log(output.String())
}

func TestMeasurementReturnedFailureProtectsCredentialsAndPreservesCause(t *testing.T) {
	opts, output := measurementFixture(t)
	const configured = "private-configured-terminal-credential"
	opts.Protector = observability.NewCredentialProtector(configured)
	server := measurementServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		require.Equal(t, "Bearer "+opts.APIKey, r.Header.Get("Authorization"))
		http.Error(w, fmt.Sprintf("admitted rejection: %s %s", opts.APIKey, configured), http.StatusBadRequest)
	})
	defer server.Close()
	opts.BaseURL = server.URL
	_, err := Run(context.Background(), opts)
	require.ErrorContains(t, err, "admitted rejection")
	require.ErrorContains(t, err, "[REDACTED]")
	var status *HTTPStatusError
	require.ErrorAs(t, err, &status)
	require.Equal(t, http.StatusBadRequest, status.StatusCode)
	journal, readErr := os.ReadFile(filepath.Join(opts.OutDir, measurementJournal))
	require.NoError(t, readErr)
	for _, secret := range []string{opts.APIKey, configured} {
		require.NotContains(t, err.Error()+output.String()+string(journal), secret)
	}
}

func TestMeasurementCredentialProtectionCannotAlterExecutionIdentity(t *testing.T) {
	for _, test := range []struct {
		credential string
		calls      int32
	}{{"sequence", 0}, {"cause", 1}} {
		t.Run(test.credential, func(t *testing.T) {
			opts, _ := measurementFixture(t)
			opts.APIKey = test.credential
			var calls atomic.Int32
			server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				calls.Add(1)
				http.Error(w, "admitted rejection", http.StatusBadRequest)
			})
			defer server.Close()
			opts.BaseURL = server.URL
			_, err := Run(context.Background(), opts)
			require.ErrorContains(t, err, "credential protection altered execution record identity")
			require.Equal(t, test.calls, calls.Load())
			var plan ExecutionMeasurements
			require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, measurementManifest), &plan))
			require.Equal(t, []string{"case-1", "case-2"}, plan.Cases)
			events, err := loadExecutionEvents(opts.OutDir)
			require.NoError(t, err)
			outcomes, err := executionOutcomes(plan, events, false)
			require.NoError(t, err)
			require.Equal(t, 2-int(test.calls), outcomes["not_dispatched"])
			require.Equal(t, int(test.calls), outcomes["attempt_unfinished"])
			require.Error(t, ValidateExecutionCapture(opts.OutDir))
		})
	}
}

func TestMeasurementURLCredentialsProtectedOnTransportFailure(t *testing.T) {
	opts, output := measurementFixture(t)
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	const password = "private-url-transport+680/one"
	address.User = url.UserPassword("fixture-transport-user", password)
	opts.BaseURL = address.String()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = Run(ctx, opts)
	require.Error(t, err)
	require.NotContains(t, output.String(), password)
	require.NotContains(t, output.String(), "fixture-transport-user")
	require.NotContains(t, output.String(), opts.APIKey)
	require.Contains(t, output.String(), "transport_error")
	require.Contains(t, output.String(), "[REDACTED]")
	var config RunConfig
	require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, "run_config.json"), &config))
	require.Equal(t, server.URL, config.BaseURL)
	events, err := os.ReadFile(filepath.Join(opts.OutDir, measurementJournal))
	require.NoError(t, err)
	require.NotContains(t, string(events), password)
	require.NotContains(t, string(events), "fixture-transport-user")
	final, level := measurementFinalLog(t, output)
	require.Equal(t, "ERROR", level)
	require.Equal(t, "failed", final.Outcome)
	t.Log(output.String())
}

func TestMeasurementEmptyURLUserinfoPreservesAdmittedContent(t *testing.T) {
	opts, output := measurementFixture(t)
	var calls int
	server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) {
		calls++
		if calls == 1 {
			http.Error(w, "admitted contact public@example.test", http.StatusServiceUnavailable)
			return
		}
		measurementRecall(w, input)
	})
	defer server.Close()
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	address.User = url.User("")
	opts.BaseURL = address.String()
	_, err = Run(context.Background(), opts)
	require.NoError(t, err)
	require.Contains(t, output.String(), "public@example.test")
	require.NoError(t, ValidateExecutionCapture(opts.OutDir))
}

func TestMeasurementMalformedURLDoesNotExposeCredentials(t *testing.T) {
	opts, output := measurementFixture(t)
	const secret = "private-malformed-url-credential"
	opts.BaseURL = "http://fixture-user:" + secret + "%zz@127.0.0.1:1"
	_, err := Run(context.Background(), opts)
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
	require.NotContains(t, output.String(), secret)
	require.NoDirExists(t, opts.OutDir)
}

func TestExecutionMeasurementsImportIncludesContractDiscovery(t *testing.T) {
	root := writeV2EvalImportFixture(t)
	opts := RunOptions{Mode: "import", SeedManifestPath: filepath.Join(root, "seed_manifest.json"), SuitePath: filepath.Join(root, "suite.jsonl"), OutDir: filepath.Join(root, "capture"), RunID: "measured-import", APIKey: "private-import-credential", ImportSeed: true, ImportConcurrency: 2}
	server := measurementServer(t, func(w http.ResponseWriter, r *http.Request, input map[string]any) {
		switch r.URL.Path {
		case "tool:remember":
			source := strings.TrimPrefix(stringValue(input["idempotency_key"]), "eval:")
			_ = json.NewEncoder(w).Encode(map[string]any{"contract_version": "dense-mem.v2.6.2", "submission_id": "attempt-" + source, "submission_kind": "remember", "processing_state": "completed", "search_state": "current", "correlation_id": "server-" + source, "evidence": []map[string]any{{"disposition": "stored", "evidence_id": "evidence-" + source, "evidence_index": 0, "search_state": "current"}}, "relationship_results": []any{}, "errors": []any{}})
		case "tool:eval_list_knowledge_refs":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		default:
			t.Errorf("unexpected tool %s", r.URL.Path)
		}
	})
	defer server.Close()
	opts.BaseURL = server.URL
	_, err := Run(context.Background(), opts)
	require.NoError(t, err)
	require.NoError(t, ValidateExecutionCapture(opts.OutDir))
	events, err := loadExecutionEvents(opts.OutDir)
	require.NoError(t, err)
	discoveries := 0
	for _, event := range events {
		if event.Event == "attempt_finished" && event.Method == "tools/list" {
			discoveries++
			require.Equal(t, "import", event.Kind)
			require.Equal(t, "setup", event.Phase)
			require.True(t, event.Dispatched)
		}
		if event.Event == "operation_finished" && event.Kind == "import" {
			require.True(t, validElapsed(event.ElapsedMS))
			require.Equal(t, "completed", event.Outcome)
		}
	}
	require.Equal(t, 1, discoveries, "concurrent imports share one contract discovery inside the measured operation")
}

func TestMeasurementLogLevelFiltering(t *testing.T) {
	opts, _ := measurementFixture(t)
	var logs bytes.Buffer
	opts.Logger = observability.NewWithHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) { measurementRecall(w, input) })
	defer server.Close()
	opts.BaseURL = server.URL
	_, err := Run(context.Background(), opts)
	require.NoError(t, err)
	require.Contains(t, logs.String(), "evaluation_operation_finished")
	require.NotContains(t, logs.String(), "ai_exchange_payload")
	require.NotContains(t, logs.String(), "evaluation_plan")
}

func measurementFinalLog(t *testing.T, logs *bytes.Buffer) (executionEvent, string) {
	t.Helper()
	for _, line := range strings.Split(logs.String(), "\n") {
		var record struct {
			Message   string         `json:"msg"`
			Level     string         `json:"level"`
			Execution executionEvent `json:"execution"`
		}
		if json.Unmarshal([]byte(line), &record) == nil && record.Message == "evaluation_run_finished" {
			return record.Execution, record.Level
		}
	}
	t.Fatal("missing structured final execution event")
	return executionEvent{}, ""
}

func TestMeasuredReplayValidatesSelectedTraceArtifact(t *testing.T) {
	opts, logs := measurementFixture(t)
	server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) { measurementRecall(w, input) })
	defer server.Close()
	opts.BaseURL = server.URL
	_, err := Run(context.Background(), opts)
	require.NoError(t, err)
	traces, err := LoadRecallTraces(filepath.Join(opts.OutDir, "recall_traces.jsonl"))
	require.NoError(t, err)
	traces[0].RankedRefs = nil
	alternate := filepath.Join(opts.OutDir, "alternate_traces.jsonl")
	require.NoError(t, writeJSONL(alternate, traces))
	replay := opts
	replay.TracesPath = alternate
	replay.MappingPath = filepath.Join(opts.OutDir, "knowledge_mapping.json")
	replay.OutDir = filepath.Join(filepath.Dir(opts.OutDir), "altered-replay")
	_, err = Run(context.Background(), replay)
	require.ErrorContains(t, err, "selected trace artifact hash mismatch")
	require.Contains(t, logs.String(), "evaluation_capture_invalid")
	require.Contains(t, logs.String(), "observed_case_ids")
}

func TestMeasuredReplayRejectsSourceDirectoryAliases(t *testing.T) {
	opts, _ := measurementFixture(t)
	var calls atomic.Int32
	server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) {
		calls.Add(1)
		measurementRecall(w, input)
	})
	defer server.Close()
	opts.BaseURL = server.URL
	_, err := Run(context.Background(), opts)
	require.NoError(t, err)
	original, err := pairArtifactHashes(opts.OutDir)
	require.NoError(t, err)
	alias := filepath.Join(filepath.Dir(opts.OutDir), "alias")
	require.NoError(t, os.Symlink(opts.OutDir, alias))
	linked := filepath.Join(filepath.Dir(opts.OutDir), "linked")
	require.NoError(t, os.Mkdir(linked, 0o700))
	entries, err := os.ReadDir(opts.OutDir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NoError(t, os.Link(filepath.Join(opts.OutDir, entry.Name()), filepath.Join(linked, entry.Name())))
	}
	for _, test := range []struct{ name, input, output string }{
		{"same directory", opts.OutDir, opts.OutDir},
		{"output symlink", opts.OutDir, alias},
		{"input symlink", alias, opts.OutDir},
		{"linked output artifacts", opts.OutDir, linked},
	} {
		t.Run(test.name, func(t *testing.T) {
			replay := opts
			replay.TracesPath = filepath.Join(test.input, "recall_traces.jsonl")
			replay.MappingPath = filepath.Join(test.input, "knowledge_mapping.json")
			replay.OutDir = test.output
			_, err := Run(context.Background(), replay)
			require.ErrorContains(t, err, "separate output directory")
			after, err := pairArtifactHashes(opts.OutDir)
			require.NoError(t, err)
			require.Equal(t, original, after)
			require.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestMeasuredReplayRejectsIsolatedAliasedWriteDestinations(t *testing.T) {
	opts, _ := measurementFixture(t)
	var calls atomic.Int32
	server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) {
		calls.Add(1)
		measurementRecall(w, input)
	})
	defer server.Close()
	opts.BaseURL = server.URL
	_, err := Run(context.Background(), opts)
	require.NoError(t, err)
	original, err := pairArtifactHashes(opts.OutDir)
	require.NoError(t, err)
	manifest, err := LoadSeedManifest(opts.SeedManifestPath)
	require.NoError(t, err)
	var config RunConfig
	require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, "run_config.json"), &config))
	for _, name := range []string{measurementJournal, measurementManifest, "seed_manifest.json", "suite.jsonl", "run_config.json", "summary.json", "knowledge_mapping.json", "recall_traces.jsonl", "retrieval_scores.jsonl", "gate_result.json", "release_gate_result.json", "release_gate_input_result.json", "validate"} {
		t.Run(name, func(t *testing.T) {
			replay := opts
			replay.TracesPath = filepath.Join(opts.OutDir, "recall_traces.jsonl")
			replay.MappingPath = filepath.Join(opts.OutDir, "knowledge_mapping.json")
			replay.OutDir = t.TempDir()
			destination := name
			if name == "validate" {
				replay.Mode, destination = "validate", "seed_manifest.json"
			}
			require.NoError(t, os.Link(filepath.Join(opts.OutDir, measurementJournal), filepath.Join(replay.OutDir, destination)))
			if name == "gate_result.json" {
				minimum := 0.0
				replay.Gates.MinRecallAtK = &minimum
			}
			if strings.HasPrefix(name, "release_gate_") {
				policy := releaseGateTestPolicy()
				policy.SeedID, policy.SeedHash = manifest.SeedID, config.SeedHash
				if name == "release_gate_input_result.json" {
					policy.SeedHash = "sha256:unapproved"
				}
				replay.ReleaseGatePolicyPath = filepath.Join(t.TempDir(), "policy.json")
				require.NoError(t, writeJSONFile(replay.ReleaseGatePolicyPath, policy))
			}
			_, err := Run(context.Background(), replay)
			require.ErrorContains(t, err, "separate output directory")
			after, err := pairArtifactHashes(opts.OutDir)
			require.NoError(t, err)
			require.Equal(t, original, after)
			entries, err := os.ReadDir(replay.OutDir)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestMeasuredReplayPreservesAliasedInputFiles(t *testing.T) {
	opts, _ := measurementFixture(t)
	var calls atomic.Int32
	server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) {
		calls.Add(1)
		measurementRecall(w, input)
	})
	defer server.Close()
	opts.BaseURL = server.URL
	_, err := Run(context.Background(), opts)
	require.NoError(t, err)
	manifest, err := LoadSeedManifest(opts.SeedManifestPath)
	require.NoError(t, err)
	policy := releaseGateTestPolicy()
	policy.SeedID = manifest.SeedID
	policy.SeedHash, err = SeedHash(opts.SeedManifestPath, manifest)
	require.NoError(t, err)
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	require.NoError(t, writeJSONFile(policyPath, policy))
	inputs := append(seedArtifactPaths(opts.SeedManifestPath, manifest), opts.SuitePath, policyPath)
	for _, input := range inputs {
		t.Run(filepath.Base(input), func(t *testing.T) {
			before, err := PairFileHash(input)
			require.NoError(t, err)
			replay := opts
			replay.TracesPath = filepath.Join(opts.OutDir, "recall_traces.jsonl")
			replay.MappingPath = filepath.Join(opts.OutDir, "knowledge_mapping.json")
			replay.ReleaseGatePolicyPath = policyPath
			replay.OutDir = t.TempDir()
			require.NoError(t, os.Link(input, filepath.Join(replay.OutDir, "seed_manifest.json")))
			_, err = Run(context.Background(), replay)
			require.ErrorContains(t, err, "separate output directory")
			after, err := PairFileHash(input)
			require.NoError(t, err)
			require.Equal(t, before, after)
			entries, err := os.ReadDir(replay.OutDir)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestCaptureRejectionLogsAtReplayAndComparisonBoundaries(t *testing.T) {
	for _, corruption := range []string{"missing", "duplicate", "corrupt"} {
		t.Run(corruption, func(t *testing.T) {
			opts, logs := measurementFixture(t)
			server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) { measurementRecall(w, input) })
			defer server.Close()
			opts.BaseURL = server.URL
			_, err := Run(context.Background(), opts)
			require.NoError(t, err)
			path := filepath.Join(opts.OutDir, "recall_traces.jsonl")
			switch corruption {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "corrupt":
				require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
			case "duplicate":
				traces, err := LoadRecallTraces(path)
				require.NoError(t, err)
				require.NoError(t, writeJSONL(path, append(traces, traces[0])))
			}
			replay := opts
			replay.TracesPath = path
			replay.MappingPath = filepath.Join(opts.OutDir, "knowledge_mapping.json")
			replay.OutDir = filepath.Join(filepath.Dir(opts.OutDir), "rejected-replay")
			logs.Reset()
			_, err = Run(context.Background(), replay)
			require.Error(t, err)
			require.Contains(t, logs.String(), "evaluation_capture_invalid")
			require.Contains(t, logs.String(), `"level":"ERROR"`)
			require.Contains(t, logs.String(), "capture_id")
			require.Contains(t, logs.String(), "observed_case_ids")
			logs.Reset()
			_, err = CompareRunDirs(opts.OutDir, opts.OutDir, "", opts.Logger)
			require.Error(t, err)
			require.Contains(t, logs.String(), "evaluation_capture_invalid")
			logs.Reset()
			_, err = readComparisonSummary("candidate", opts.OutDir, opts.Logger)
			require.Error(t, err)
			require.Contains(t, logs.String(), "evaluation_capture_invalid")
			t.Log(logs.String())
		})
	}
}

func TestV2CohortComparisonRequiresMeasuredEvidence(t *testing.T) {
	for _, corruption := range []string{"missing_journal", "incomplete", "summary_changed"} {
		t.Run(corruption, func(t *testing.T) {
			cohortOpts := writeV2CohortValidationFixture(t)
			cohort, err := ValidateV2CohortDerivation(cohortOpts)
			require.NoError(t, err)
			opts, logs := measurementFixture(t)
			opts.SeedManifestPath, opts.SuitePath = cohortOpts.DerivedManifestPath, cohortOpts.DerivedSuitePath
			manifest, err := LoadSeedManifest(opts.SeedManifestPath)
			require.NoError(t, err)
			qrels, err := LoadQrels(opts.SeedManifestPath, manifest)
			require.NoError(t, err)
			mapping := newKnowledgeMapping()
			for _, qrel := range qrels {
				for _, ref := range qrel.RequiredRefs {
					if ref.SourceDocID != "" {
						kind := ref.Type
						if kind == "source_doc" {
							kind = "fragment"
						}
						addSourceMapping(&mapping, Ref{Type: kind, ID: "mapped-" + ref.SourceDocID, SourceDocID: ref.SourceDocID}, true)
					}
				}
			}
			require.NoError(t, writeJSONFile(opts.MappingPath, mapping))
			server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) { measurementRecall(w, input) })
			defer server.Close()
			opts.BaseURL = server.URL
			_, err = Run(context.Background(), opts)
			require.NoError(t, err)
			baseline := filepath.Join(t.TempDir(), "legacy-baseline")
			require.NoError(t, writeJSONFile(filepath.Join(baseline, "summary.json"), Summary{RunID: "legacy", SeedHash: cohort.FilteredSeedHash, CaseCount: cohort.RetainedCaseCount, ScoredCaseCount: cohort.RetainedCaseCount}))
			comparison := V2CohortComparisonOptions{Cohort: cohortOpts, BaselineRunDir: baseline, CandidateRunDir: opts.OutDir, OutDir: filepath.Join(t.TempDir(), "comparison"), Logger: opts.Logger}
			_, err = CompareV2CohortRunDirs(comparison)
			require.NoError(t, err)
			if corruption == "missing_journal" {
				require.NoError(t, os.Remove(filepath.Join(opts.OutDir, measurementJournal)))
			} else if corruption == "summary_changed" {
				var summary Summary
				require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, "summary.json"), &summary))
				summary.AverageRecallAtK += 0.25
				require.NoError(t, writeJSONFile(filepath.Join(opts.OutDir, "summary.json"), summary))
			} else {
				var plan ExecutionMeasurements
				require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, measurementManifest), &plan))
				plan.Status = "incomplete"
				require.NoError(t, writeJSONFile(filepath.Join(opts.OutDir, measurementManifest), plan))
			}
			logs.Reset()
			_, err = CompareV2CohortRunDirs(comparison)
			require.Error(t, err)
			if corruption == "missing_journal" {
				require.ErrorContains(t, err, measurementJournal)
			} else if corruption == "summary_changed" {
				require.ErrorContains(t, err, "summary.json")
			} else {
				require.ErrorContains(t, err, "incomplete")
			}
			require.Contains(t, logs.String(), "evaluation_capture_invalid")
		})
	}
}

func TestComparisonRejectsAlteredSummaryAndLogsKnownExpectations(t *testing.T) {
	opts, logs := measurementFixture(t)
	server := measurementServer(t, func(w http.ResponseWriter, _ *http.Request, input map[string]any) { measurementRecall(w, input) })
	defer server.Close()
	opts.BaseURL = server.URL
	_, err := Run(context.Background(), opts)
	require.NoError(t, err)
	var plan ExecutionMeasurements
	require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, measurementManifest), &plan))
	var summary Summary
	require.NoError(t, readJSONFile(filepath.Join(opts.OutDir, "summary.json"), &summary))
	summary.AverageRecallAtK += 0.25
	require.NoError(t, writeJSONFile(filepath.Join(opts.OutDir, "summary.json"), summary))
	logs.Reset()
	_, err = CompareRunDirs(opts.OutDir, opts.OutDir, "", opts.Logger)
	require.ErrorContains(t, err, "summary.json")
	var diagnostic struct {
		Message           string   `json:"msg"`
		ExpectedSeed      string   `json:"expected_seed_hash"`
		ExpectedCases     int      `json:"expected_cases"`
		ExpectedAvailable bool     `json:"expected_cases_available"`
		ExpectedIDs       []string `json:"expected_case_ids"`
		ObservedIDs       []string `json:"observed_case_ids"`
		RunID             string   `json:"run_id"`
		CaptureID         string   `json:"capture_id"`
	}
	found := false
	for _, line := range strings.Split(logs.String(), "\n") {
		if json.Unmarshal([]byte(line), &diagnostic) == nil && diagnostic.Message == "evaluation_capture_invalid" {
			found = true
			break
		}
	}
	require.True(t, found)
	require.Equal(t, plan.SeedHash, diagnostic.ExpectedSeed)
	require.Equal(t, 2, diagnostic.ExpectedCases)
	require.True(t, diagnostic.ExpectedAvailable)
	require.Equal(t, []string{"case-1", "case-2"}, diagnostic.ExpectedIDs)
	require.Equal(t, diagnostic.ExpectedIDs, diagnostic.ObservedIDs)
	require.Equal(t, plan.RunID, diagnostic.RunID)
	require.Equal(t, plan.CaptureID, diagnostic.CaptureID)
	require.NoError(t, os.Remove(filepath.Join(opts.OutDir, measurementJournal)))
	require.ErrorContains(t, ValidateExecutionCapture(opts.OutDir, opts.Logger), measurementJournal)
}
