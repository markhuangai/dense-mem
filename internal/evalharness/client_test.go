package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
)

type importStatusTransport func(*http.Request) (*http.Response, error)

func (f importStatusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestImporterReportsEveryMinuteWhileWaitingAndStopsAfterCompletion(t *testing.T) {
	for _, scenario := range []struct {
		name, blockedMethod string
		cancelled           bool
	}{
		{"intake success", "tools/call", false},
		{"intake cancelled", "tools/call", true},
		{"catalog success", "tools/list", false},
		{"catalog cancelled", "tools/list", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs bytes.Buffer
				entered, release := make(chan struct{}), make(chan struct{})
				item := CorpusItem{SourceDocID: "source", Content: "Fixed public corpus text."}
				transport := importStatusTransport(func(request *http.Request) (*http.Response, error) {
					var rpc struct{ Method string }
					require.NoError(t, json.NewDecoder(request.Body).Decode(&rpc))
					if rpc.Method == scenario.blockedMethod {
						close(entered)
						select {
						case <-request.Context().Done():
							return nil, request.Context().Err()
						case <-release:
						}
					}
					var result map[string]any
					if rpc.Method == "tools/list" {
						tools := []mcpToolDefinition{}
						for _, tool := range registry.ContractTools() {
							tools = append(tools, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
						}
						result = map[string]any{"tools": tools}
					} else {
						result = map[string]any{"structuredContent": map[string]any{"processing_state": "completed", "submission_id": uuid.NewString(), "evidence": []any{map[string]any{
							"evidence_index": 0, "partition_index": 0, "source_item_id": uuid.NewString(), "occurrence_id": uuid.NewString(), "evidence_id": uuid.NewString(),
							"span_start": 0, "span_end": len([]rune(item.Content)), "content_hash": knowledge.EvidenceContentHash(item.Content), "disposition": "stored",
						}}}}
					}
					body, err := json.Marshal(map[string]any{"result": result})
					require.NoError(t, err)
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
				})
				client := &HTTPClient{BaseURL: "http://synthetic.invalid", APIKey: "synthetic", Client: &http.Client{Transport: transport}, Capture: captureFixture(&logs)}
				snapshot := func() []byte {
					client.Capture.mu.Lock()
					defer client.Capture.mu.Unlock()
					return bytes.Clone(logs.Bytes())
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := client.ImportCorpus(ctx, []CorpusItem{item})
					done <- err
				}()
				select {
				case <-entered:
				case err := <-done:
					t.Fatalf("import returned before the blocked request: %v", err)
				}
				synctest.Wait()
				time.Sleep(59 * time.Second)
				synctest.Wait()
				require.Len(t, capturedProgress(t, snapshot()), 1)
				time.Sleep(time.Second)
				synctest.Wait()
				require.Len(t, capturedProgress(t, snapshot()), 2)
				time.Sleep(time.Minute)
				synctest.Wait()
				require.Len(t, capturedProgress(t, snapshot()), 3)
				if scenario.cancelled {
					cancel()
				} else {
					close(release)
				}
				err := <-done
				if scenario.cancelled {
					require.ErrorContains(t, err, "context canceled")
				} else {
					require.NoError(t, err)
				}
				time.Sleep(time.Minute)
				synctest.Wait()
				updates := capturedProgress(t, logs.Bytes())
				require.Len(t, updates, 4, "periodic reporting stops before the final update")
				for _, update := range updates[:3] {
					require.Equal(t, captureProgress{Stage: "import", Unit: "documents", Total: 1, Remaining: 1}, update)
				}
				final := captureProgress{Stage: "import", Unit: "documents", Total: 1, Completed: 1}
				if scenario.cancelled {
					final.Completed, final.Failed = 0, 1
					if scenario.blockedMethod == "tools/list" {
						final.Failed, final.Remaining = 0, 1
					}
				}
				require.Equal(t, final, updates[3])
				require.NotContains(t, logs.String(), item.Content)
			})
		})
	}
}

func TestImporterReportsFinalCountsWhenCatalogDiscoveryFails(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "cancelled"}[cancelled], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			var logs bytes.Buffer
			client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client(), Capture: captureFixture(&logs)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			mapping, err := client.ImportCorpus(ctx, []CorpusItem{{SourceDocID: "source", Content: "Exact public corpus."}})
			require.Error(t, err)
			require.Empty(t, mapping.BySourceDocID)
			progress := capturedProgress(t, logs.Bytes())
			require.Len(t, progress, 2)
			require.Equal(t, captureProgress{Stage: "import", Unit: "documents", Total: 1, Remaining: 1}, progress[0])
			require.Equal(t, progress[0], progress[1])
		})
	}
}

func TestImporterRetriesOnlyOneVerifiedTerminalFailureAndPreservesInput(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(map[bool]string{true: "recovery", false: "exhausted"}[recover], func(t *testing.T) {
			inputs := []map[string]any{}
			attempt := uuid.NewString()
			item := CorpusItem{SourceDocID: "source", Content: "First.\n第二。"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var rpc struct {
					Method string `json:"method"`
					Params struct {
						Arguments map[string]any `json:"arguments"`
					} `json:"params"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&rpc))
				if rpc.Method == "tools/list" {
					tools := []mcpToolDefinition{}
					for _, tool := range registry.ContractTools() {
						tools = append(tools, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
					}
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tools": tools}}))
					return
				}
				inputs = append(inputs, rpc.Params.Arguments)
				out := map[string]any{"processing_state": "failed", "submission_id": attempt, "errors": []any{map[string]any{"code": "provider_unavailable", "message": "provider failed", "retryable": false, "next_action": "resubmit_remember"}}}
				failed := true
				if recover && len(inputs) == 2 {
					out = map[string]any{"processing_state": "completed", "submission_id": uuid.NewString(), "evidence": []any{map[string]any{"evidence_index": 0, "partition_index": 0, "source_item_id": uuid.NewString(), "occurrence_id": uuid.NewString(), "evidence_id": uuid.NewString(), "span_start": 0, "span_end": len([]rune(item.Content)), "content_hash": knowledge.EvidenceContentHash(item.Content), "disposition": "stored"}}}
					failed = false
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"isError": failed, "structuredContent": out}}))
			}))
			defer server.Close()
			var logs bytes.Buffer
			client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client(), Capture: captureFixture(&logs)}
			mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{item})
			progress := capturedProgress(t, logs.Bytes())
			require.Len(t, progress, 2, "retry attempts count one terminal document")
			require.Equal(t, captureProgress{Stage: "import", Unit: "documents", Total: 1, Remaining: 1}, progress[0])
			if recover {
				require.NoError(t, err)
				require.Len(t, mapping.Partitions["source"], 1)
				require.Equal(t, captureProgress{Stage: "import", Unit: "documents", Total: 1, Completed: 1}, progress[1])
			} else {
				require.Error(t, err)
				require.Empty(t, mapping.BySourceDocID)
				require.Equal(t, captureProgress{Stage: "import", Unit: "documents", Total: 1, Failed: 1}, progress[1])
			}
			require.Len(t, inputs, 2)
			require.Equal(t, "eval:source", inputs[0]["idempotency_key"])
			require.Equal(t, "eval:source:resubmit1", inputs[1]["idempotency_key"])
			evidence := inputs[0]["evidence"].([]any)[0].(map[string]any)
			require.NotContains(t, evidence, "source_key", "corpus intake must remain eligible for semantic reuse")
			require.NotContains(t, evidence, "source_revision")
			require.Equal(t, "source", evidence["metadata"].(map[string]any)["source_doc_id"])
			delete(inputs[0], "idempotency_key")
			delete(inputs[1], "idempotency_key")
			require.Equal(t, inputs[0], inputs[1])
		})
	}
}

func TestConcurrentImporterReportsMonotonicDocumentProgress(t *testing.T) {
	const workers = 20
	items := make([]CorpusItem, workers+1)
	for i := range items {
		items[i] = CorpusItem{SourceDocID: uuid.NewString(), Content: "Exact public text."}
	}
	started := make(chan struct{}, len(items))
	release := make(chan struct{})
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string
			Params struct{ Arguments map[string]any }
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&rpc))
		if rpc.Method == "tools/list" {
			tools := []mcpToolDefinition{}
			for _, tool := range registry.ContractTools() {
				tools = append(tools, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
			}
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tools": tools}}))
			return
		}
		evidence := rpc.Params.Arguments["evidence"].([]any)[0].(map[string]any)
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		content := evidence["content"].(string)
		out := map[string]any{"processing_state": "completed", "submission_id": uuid.NewString(), "evidence": []any{map[string]any{
			"evidence_index": 0, "partition_index": 0, "source_item_id": uuid.NewString(), "occurrence_id": uuid.NewString(), "evidence_id": uuid.NewString(),
			"span_start": 0, "span_end": len([]rune(content)), "content_hash": knowledge.EvidenceContentHash(content), "disposition": "stored",
		}}}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": out}}))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var logs bytes.Buffer
	client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client(), Capture: captureFixture(&logs)}
	var mapping KnowledgeMapping
	done := make(chan error, 1)
	go func() {
		var err error
		mapping, err = client.ImportCorpusWithConcurrency(ctx, items, workers)
		done <- err
	}()
	for range workers {
		select {
		case <-started:
		case err := <-done:
			require.NoError(t, err)
			t.Fatal("import returned before all workers reached the server")
		case <-ctx.Done():
			t.Fatal("configured workers did not reach the server concurrently")
		}
	}
	require.EqualValues(t, workers, active.Load())
	close(release)
	err := <-done
	require.NoError(t, err)
	require.EqualValues(t, workers, peak.Load())
	require.Len(t, mapping.BySourceDocID, len(items))
	progress := capturedProgress(t, logs.Bytes())
	require.Equal(t, []captureProgress{
		{Stage: "import", Unit: "documents", Total: len(items), Remaining: len(items)},
		{Stage: "import", Unit: "documents", Total: len(items), Completed: len(items)},
	}, progress)
	for _, event := range capturedEvents(t, logs.Bytes()) {
		require.NotEqual(t, "import_attempt", event.Kind, "successful documents do not produce individual logs")
	}
}

func TestPartitionReceiptValidationIsAtomicAndExact(t *testing.T) {
	item := CorpusItem{SourceDocID: "source", Content: "First.\n第二。"}
	boundary := len([]rune("First.\n"))
	entries := []any{map[string]any{"evidence_id": "first", "disposition": "stored", "evidence_index": 0, "partition_index": 0, "source_item_id": "original", "occurrence_id": "one", "span_start": 0, "span_end": boundary, "content_hash": knowledge.EvidenceContentHash("First.\n")}, map[string]any{"evidence_id": "second", "disposition": "stored", "evidence_index": 0, "partition_index": 1, "source_item_id": "original", "occurrence_id": "two", "span_start": boundary, "span_end": len([]rune(item.Content)), "content_hash": knowledge.EvidenceContentHash("第二。")}}
	sourceItem := uuid.NewString()
	for _, raw := range entries {
		entry := raw.(map[string]any)
		entry["evidence_id"] = uuid.NewString()
		entry["occurrence_id"] = uuid.NewString()
		entry["source_item_id"] = sourceItem
	}
	output := map[string]any{"processing_state": "completed", "evidence": entries}
	mapping, err := (&HTTPClient{}).importTerminalRememberResult(item, output, newKnowledgeMapping())
	require.NoError(t, err)
	require.Len(t, mapping.Partitions["source"], 2)
	for _, field := range []string{"span_start", "span_end", "content_hash", "source_item_id", "occurrence_id", "partition_index"} {
		t.Run(field, func(t *testing.T) {
			second := entries[1].(map[string]any)
			saved := second[field]
			second[field] = "invalid"
			original := newKnowledgeMapping()
			_, err := (&HTTPClient{}).importTerminalRememberResult(item, output, original)
			require.Error(t, err)
			require.Empty(t, original.BySourceDocID)
			second[field] = saved
		})
	}
}

func TestProductionCatalogIsUnchangedAndEvaluationCallsAreRetired(t *testing.T) {
	reg, err := registry.BuildActive(registry.Dependencies{})
	require.NoError(t, err)
	wanted := []string{"remember", "ingest_session", "retract_evidence", "recall_memory", "trace_memory", "submit_recall_session_feedback", "export_memory_pack"}
	require.ElementsMatch(t, wanted, registry.ContractToolNames())
	require.Len(t, reg.List(), len(wanted))
	for _, name := range []string{"eval_list_knowledge_refs", "eval_run_recall_case"} {
		_, found := reg.Get(name)
		require.False(t, found)
	}
}

func TestMCPClientLogsProtectedTransportFailure(t *testing.T) {
	const endpoint = "http://internal-mcp.invalid"
	const key = "mcp-diagnostics-canary-9483"
	const admittedText = "ordinary request is not dumped"
	failure := errors.New("connection reset by peer for " + key)
	for _, stage := range []string{"request", "response_body", "protection unavailable"} {
		t.Run(stage, func(t *testing.T) {
			var logs bytes.Buffer
			capture := captureFixture(&logs)
			if stage == "protection unavailable" {
				capture.Protector = nil
			} else {
				capture.Protector = NewRunnerCredentialProtector(key)
			}
			transport := importStatusTransport(func(*http.Request) (*http.Response, error) {
				if stage == "response_body" {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(failure))}, nil
				}
				return nil, failure
			})
			client := &HTTPClient{BaseURL: endpoint, APIKey: key, Client: &http.Client{Transport: transport}, Capture: capture}
			err := client.CallTool(context.Background(), "remember", map[string]string{"content": admittedText}, nil)
			require.Error(t, err)
			for _, forbidden := range []string{key, endpoint, admittedText} {
				require.NotContains(t, logs.String(), forbidden)
				require.NotContains(t, err.Error(), forbidden)
			}
			if stage == "protection unavailable" {
				require.ErrorContains(t, err, "payload protection unavailable")
				return
			}
			require.NotContains(t, err.Error(), "connection reset by peer")
			events := capturedEvents(t, logs.Bytes())
			require.Len(t, events, 1)
			require.Equal(t, "runtime_transport_failure", events[0].Kind)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(events[0].Payload, &payload))
			require.Equal(t, "runner_to_dense_mem", payload["link"])
			require.Equal(t, stage, payload["stage"])
			require.Equal(t, "remember", payload["mcp_tool"])
			require.Contains(t, payload["error"], "connection reset by peer")
		})
	}
}

func TestMCPResponseDecodingAndWireFailures(t *testing.T) {
	const key = "mcp-key-canary-9483"
	for _, item := range []struct {
		name, body, failure string
		status              int
		interrupted         bool
	}{
		{"structured", "{\"result\":{\"structuredContent\":{\"number\":9007199254740993}}}", "", 200, false},
		{"text", "{\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"{\\\"number\\\":9007199254740993}\"}]}}", "", 200, false},
		{"protocol", "{\"error\":{\"code\":-32603,\"message\":\"mcp-key-canary-9483\"}}", "protocol error -32603", 200, false},
		{"missing result", "{\"result\":{}}", "no structured result", 200, false},
		{"invalid text", "{\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"invalid\"}]}}", "invalid MCP content", 200, false},
		{"malformed JSON", "{", "invalid HTTP JSON", 200, false},
		{"trailing JSON", "{}{}", "invalid HTTP JSON", 200, false},
		{"unavailable", "{\"private\":\"mcp-key-canary-9483\"}", "status 503", 503, false},
		{"interrupted", "{\"result\":", "body interrupted", 200, true},
		{"structured rejection", "{\"result\":{\"isError\":true,\"structuredContent\":{\"errors\":[{\"message\":\"intake declined\"}]}}}", "intake declined", 200, false},
		{"bounded rejection", "{\"result\":{\"isError\":true,\"structuredContent\":{\"errors\":[{\"code\":\"permission_denied\"}]}}}", "permission_denied", 200, false},
		{"partition rejection", "{\"result\":{\"isError\":true,\"structuredContent\":{\"evidence\":[{\"errors\":[{\"message\":\"partition declined\"}]}]}}}", "partition declined", 200, false},
		{"partition code", "{\"result\":{\"isError\":true,\"structuredContent\":{\"evidence\":[{\"errors\":[{\"code\":\"partition_invalid\"}]}]}}}", "partition_invalid", 200, false},
		{"empty rejection", "{\"result\":{\"isError\":true,\"structuredContent\":{}}}", "MCP recall_memory:", 200, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
				require.Equal(t, "tools/call", r.Header.Get("Mcp-Method"))
				require.Equal(t, "recall_memory", r.Header.Get("Mcp-Name"))
				if item.interrupted {
					w.Header().Set("Content-Length", "100")
				}
				w.WriteHeader(item.status)
				_, err := w.Write([]byte(item.body))
				require.NoError(t, err)
			}))
			defer server.Close()
			var logs bytes.Buffer
			capture := captureFixture(&logs)
			capture.Protector = NewRunnerCredentialProtector(key)
			require.NoError(t, capture.Emit("started", map[string]string{"model": "synthetic"}))
			client := &HTTPClient{BaseURL: server.URL, APIKey: key, Client: server.Client(), ExchangeCapture: capture}
			out := map[string]any{"stale": "prior question"}
			err := client.CallTool(context.Background(), "recall_memory", map[string]any{"query": "public question"}, &out)
			if item.failure != "" {
				require.ErrorContains(t, err, item.failure)
				require.NotContains(t, err.Error(), key)
			} else {
				require.NoError(t, err)
				require.Equal(t, json.Number("9007199254740993"), out["number"])
				require.NotContains(t, out, "stale")
			}
			require.NoError(t, capture.Emit("finished", map[string]bool{"complete": err == nil}))
			var protected strings.Builder
			for _, event := range capturedEvents(t, logs.Bytes()) {
				body, err := json.Marshal(event)
				require.NoError(t, err)
				protected.Write(body)
			}
			require.NotContains(t, protected.String(), key)
			require.Contains(t, protected.String(), "answer_wire_response")
		})
	}
	t.Run("oversized response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, err := w.Write(bytes.Repeat([]byte("x"), (16<<20)+1))
			require.NoError(t, err)
		}))
		defer server.Close()
		require.ErrorContains(t, (&HTTPClient{Client: server.Client()}).doJSON(context.Background(), http.MethodGet, server.URL, "", nil, nil), "exceeds capture bound")
	})
	t.Run("transport and empty control response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		client := &HTTPClient{Client: server.Client()}
		require.NoError(t, client.doJSON(context.Background(), http.MethodPost, server.URL, "", nil, nil))
		server.Close()
		var logs bytes.Buffer
		client.ExchangeCapture = captureFixture(&logs)
		require.ErrorContains(t, client.doJSON(context.Background(), http.MethodPost, server.URL, "", nil, nil), "dependency unavailable")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorContains(t, client.doJSON(ctx, http.MethodPost, server.URL, "", nil, nil), "context canceled")
		require.ErrorContains(t, client.doJSON(ctx, http.MethodPost, "://invalid", "", nil, nil), "invalid request configuration")
	})
}

func TestImporterReconcilesTransientFailureWithTheSameOperationKey(t *testing.T) {
	item := CorpusItem{SourceDocID: "source", SourceDataset: "synthetic", Content: "Exact source.", Labels: []string{"public"}}
	requests := make(chan map[string]any, 2)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string
			Params struct{ Arguments map[string]any }
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&rpc))
		if rpc.Method == "tools/list" {
			tools := []mcpToolDefinition{}
			for _, tool := range registry.ContractTools() {
				tools = append(tools, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
			}
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tools": tools}}))
			return
		}
		requests <- rpc.Params.Arguments
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		result := map[string]any{"processing_state": "completed", "submission_id": uuid.NewString(), "evidence": []any{map[string]any{
			"evidence_index": 0, "partition_index": 0, "source_item_id": uuid.NewString(), "occurrence_id": uuid.NewString(), "evidence_id": uuid.NewString(),
			"span_start": 0, "span_end": len([]rune(item.Content)), "content_hash": knowledge.EvidenceContentHash(item.Content), "disposition": "stored",
		}}}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": result}}))
	}))
	defer server.Close()
	var logs bytes.Buffer
	capture := captureFixture(&logs)
	client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client(), Capture: capture}
	mapping, err := client.ImportCorpusWithConcurrency(context.Background(), []CorpusItem{item}, 4)
	require.NoError(t, err)
	require.Len(t, mapping.Partitions["source"], 1)
	require.Equal(t, <-requests, <-requests, "transient recovery preserves exact Evidence, target and operation key")
	require.EqualValues(t, 2, attempts.Load())
}

func TestImporterQueuesConfirmedGatewayTimeoutsWithTheSameRequest(t *testing.T) {
	for _, scenario := range []struct {
		name              string
		status, failures  int
		attempts          int
		requeued          bool
		direct, foreign   bool
		cancelWhileQueued bool
	}{
		{name: "assessment 524 recovers on final allowed attempt", status: 524, failures: 5, attempts: 6, requeued: true},
		{name: "assessment 504 exhausts five retries", status: 504, failures: 6, attempts: 6, requeued: true},
		{name: "direct 504 recovers", status: 504, failures: 1, attempts: 2, requeued: true, direct: true},
		{name: "final 502 supersedes earlier timeout", status: 502, failures: 6, attempts: 2},
		{name: "foreign diagnostics cannot extend retries", status: 524, failures: 6, attempts: 2, foreign: true},
		{name: "cancellation leaves the timeout queued", status: 524, failures: 1, attempts: 1, requeued: true, cancelWhileQueued: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			teamID := uuid.NewString()
			diagnosticTeamID := teamID
			if scenario.foreign {
				diagnosticTeamID = uuid.NewString()
			}
			const canary = "private-response-canary"
			corpus := []CorpusItem{{SourceDocID: "slow", Content: "Keep this exact."}, {SourceDocID: "ready", Content: "Ready to store."}}
			var requests []map[string]any
			var lastAttempt, lastCorrelation string
			var slowCalls, lookups int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					lookups++
					require.Equal(t, "/control/api/teams/"+teamID+"/remember-attempts/"+lastAttempt, r.URL.Path)
					require.Equal(t, "Bearer control-canary", r.Header.Get("Authorization"))
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
						"team_id": diagnosticTeamID, "attempt_id": lastAttempt, "correlation_id": lastCorrelation,
						"outcome": "failed", "failed_phase": "assessment", "error_code": "provider_unavailable", "retryable": true,
						"diagnostics": map[string]any{"provider_exchanges": []map[string]any{
							{"component": "assessor", "status_code": 524},
							{"component": "assessor", "status_code": scenario.status, "response_body": canary},
						}},
					}}))
					return
				}
				require.Equal(t, "Bearer team-canary", r.Header.Get("Authorization"))
				var rpc struct {
					Method string
					Params struct{ Arguments map[string]any }
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&rpc))
				if rpc.Method == "tools/list" {
					tools := []mcpToolDefinition{}
					for _, tool := range registry.ContractTools() {
						tools = append(tools, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
					}
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tools": tools}}))
					return
				}
				requests = append(requests, rpc.Params.Arguments)
				slow := rpc.Params.Arguments["idempotency_key"] == "eval:slow"
				if slow {
					slowCalls++
				} else if scenario.cancelWhileQueued {
					cancel()
				}
				result := map[string]any{}
				failed := slow && slowCalls <= scenario.failures
				if failed && scenario.direct {
					w.WriteHeader(scenario.status)
					return
				}
				if failed {
					lastAttempt, lastCorrelation = uuid.NewString(), uuid.NewString()
					result = map[string]any{
						"processing_state": "failed", "submission_id": lastAttempt, "correlation_id": lastCorrelation,
						"errors": []map[string]any{{"code": "provider_unavailable", "message": "provider returned a server error",
							"retryable": true, "next_action": "retry_same_request", "reason_code": "provider_server_failure",
							"details": map[string]any{"component": "remember.assessment"}}},
					}
				} else {
					content := corpus[1].Content
					if slow {
						content = corpus[0].Content
					}
					result = map[string]any{"processing_state": "completed", "submission_id": uuid.NewString(), "evidence": []map[string]any{{
						"evidence_index": 0, "partition_index": 0, "source_item_id": uuid.NewString(), "occurrence_id": uuid.NewString(), "evidence_id": uuid.NewString(),
						"span_start": 0, "span_end": len([]rune(content)), "content_hash": knowledge.EvidenceContentHash(content), "disposition": "stored",
					}}}
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"isError": failed, "structuredContent": result}}))
			}))
			defer server.Close()
			var logs bytes.Buffer
			client := &HTTPClient{BaseURL: server.URL, APIKey: "team-canary", Client: server.Client(), Capture: captureFixture(&logs)}
			native := &Native{Client: client, Scope: Scope{TeamID: teamID}, ControlURL: server.URL, ControlToken: "control-canary"}
			mapping, err := client.importCorpus(ctx, corpus, 1, native)
			server.Close()

			if scenario.attempts > scenario.failures {
				require.NoError(t, err)
				require.Len(t, mapping.BySourceDocID, 2)
			} else {
				require.Error(t, err)
				require.NotContains(t, mapping.BySourceDocID, "slow")
			}
			require.Equal(t, scenario.attempts, slowCalls)
			if scenario.requeued {
				require.Len(t, requests, scenario.attempts+1)
				require.Equal(t, "eval:ready", requests[1]["idempotency_key"])
				require.Contains(t, logs.String(), `"gateway_http_status":`)
			} else {
				require.Len(t, requests, scenario.attempts)
				require.NotContains(t, logs.String(), `"gateway_http_status":`)
			}
			for index, request := range requests {
				if !scenario.requeued || index != 1 {
					require.Equal(t, requests[0], request, "retries preserve the complete Remember request")
				}
			}
			if scenario.direct {
				require.Zero(t, lookups)
			} else {
				require.Equal(t, min(scenario.attempts, scenario.failures), lookups)
			}
			if scenario.foreign {
				require.Contains(t, logs.String(), `"provider_status":"unavailable"`)
			}
			for _, private := range []string{canary, "team-canary", "control-canary"} {
				require.NotContains(t, logs.String(), private)
			}
		})
	}
}

func TestMCPContractDiscoveryRejectsIncompleteAndMixedCatalogs(t *testing.T) {
	schema := func(version string) map[string]any {
		return map[string]any{"properties": map[string]any{"contract_version": map[string]any{"enum": []any{version}}}}
	}
	for _, item := range []struct {
		name    string
		change  func([]mcpToolDefinition, int) []mcpToolDefinition
		allowed bool
	}{
		{"production", nil, true},
		{"duplicate", func(tools []mcpToolDefinition, index int) []mcpToolDefinition { return append(tools, tools[index]) }, false},
		{"missing name", func(tools []mcpToolDefinition, index int) []mcpToolDefinition { tools[index].Name = ""; return tools }, false},
		{"missing remember", func(tools []mcpToolDefinition, index int) []mcpToolDefinition {
			return append(tools[:index], tools[index+1:]...)
		}, false},
		{"missing Recall", func(tools []mcpToolDefinition, _ int) []mcpToolDefinition {
			for index, tool := range tools {
				if tool.Name == registry.ToolRecallMemory {
					return append(tools[:index], tools[index+1:]...)
				}
			}
			return tools
		}, false},
		{"missing schema", func(tools []mcpToolDefinition, index int) []mcpToolDefinition {
			tools[index].OutputSchema = nil
			return tools
		}, false},
		{"retired tool", func(tools []mcpToolDefinition, _ int) []mcpToolDefinition {
			return append(tools, mcpToolDefinition{Name: "eval_run_recall_case"})
		}, false},
		{"unsupported version", func(tools []mcpToolDefinition, index int) []mcpToolDefinition {
			tools[index].OutputSchema = schema("dense-mem.v0")
			return tools
		}, false},
		{"compatible variants", func(tools []mcpToolDefinition, index int) []mcpToolDefinition {
			tools[index].OutputSchema = map[string]any{"oneOf": []any{schema(domain.ContractVersion), map[string]any{"type": "null"}}}
			return tools
		}, true},
		{"mixed variants", func(tools []mcpToolDefinition, index int) []mcpToolDefinition {
			tools[index].OutputSchema = map[string]any{"oneOf": []any{schema(domain.ContractVersion), schema("dense-mem.v0")}}
			return tools
		}, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			tools := []mcpToolDefinition{}
			for _, tool := range registry.ContractTools() {
				tools = append(tools, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
			}
			body, err := json.Marshal(tools)
			require.NoError(t, err)
			tools = nil
			require.NoError(t, json.Unmarshal(body, &tools))
			for index, tool := range tools {
				if tool.Name == registry.ToolRemember && item.change != nil {
					tools = item.change(tools, index)
					break
				}
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, "tools/list", r.Header.Get("Mcp-Method"))
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tools": tools}}))
			}))
			defer server.Close()
			client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client()}
			_, err = client.ensureContract(context.Background())
			if item.allowed {
				require.NoError(t, err)
				_, err = client.ensureContract(context.Background())
				require.NoError(t, err)
				require.EqualValues(t, 1, calls.Load(), "validated discovery is reused")
			} else {
				require.Error(t, err)
			}
		})
	}
	_, err := (&HTTPClient{}).ensureContract(context.Background())
	require.ErrorContains(t, err, "base URL")
	_, err = (&HTTPClient{BaseURL: "http://unused.invalid"}).ensureContract(context.Background())
	require.ErrorContains(t, err, "API key")
}
