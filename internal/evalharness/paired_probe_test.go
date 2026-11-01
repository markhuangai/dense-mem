package evalharness

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPairProbeSingleSubmissionsAndNativeSerialization(t *testing.T) {
	for _, scenario := range []string{"current", "legacy", "serial_failure", "parallel_failure"} {
		t.Run(scenario, func(t *testing.T) {
			c := pairedFixture(t)
			items := []CorpusItem{}
			ids := []string{"doc-alpha", "doc-beta", "doc-three", "doc-four", "doc-five"}
			for _, id := range ids {
				items = append(items, CorpusItem{SourceDocID: id, Content: "Exact Unicode evidence: café " + id})
			}
			require.NoError(t, writeJSONL(filepath.Join(filepath.Dir(c.SeedManifest), "corpus.jsonl"), items))
			_, _, _, _, _, _, hash, err := loadRunInputs(c.SeedManifest, c.Suite)
			require.NoError(t, err)
			c.SeedHash = hash
			if scenario == "legacy" {
				c.Baseline.Protocol, c.Baseline.Release, c.Baseline.SourceRevision = "none", "v2.6.5", "49ba9d36acaf19bde5a8ddbe7552fd04621d19bd"
			}
			t.Setenv(c.Baseline.Runtime.APIKeyEnv, "fixture-key")
			var mu sync.Mutex
			inputs := []map[string]any{}
			parallel := 0
			ready := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Params struct {
						Name      string
						Arguments map[string]any
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.URL.Path != "/mcp" || request.Params.Name != "remember" || r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Error("probe did not use authenticated native MCP Remember")
				}
				mu.Lock()
				inputs = append(inputs, request.Params.Arguments)
				index := len(inputs)
				if index > 1 {
					parallel++
					if parallel == 4 {
						close(ready)
					}
				}
				mu.Unlock()
				if index > 1 {
					select {
					case <-ready:
					case <-time.After(5 * time.Second):
						t.Error("four requests were not concurrent")
					}
				}
				if (scenario == "serial_failure" && index == 1) || (scenario == "parallel_failure" && index == 2) {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				response, _ := json.Marshal(map[string]any{"submission_id": "attempt-" + stringValue(request.Params.Arguments["idempotency_key"]), "processing_state": "completed", "evidence": []map[string]any{{"evidence_id": "evidence"}}})
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": map[string]any{"content": []map[string]string{{"type": "text", "text": string(response)}}}})
			}))
			defer server.Close()
			c.Baseline.Runtime.BaseURL = server.URL
			directory := filepath.Join(c.OutDir, "qualification")
			err = RunPairProbe(c, "baseline", ids, directory, io.Discard)
			if strings.HasSuffix(scenario, "failure") {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			var rows []PairProbeRow
			require.NoError(t, readPairJSON(filepath.Join(directory, "responses.json"), &rows))
			want := 5
			if scenario == "serial_failure" {
				want = 1
			}
			require.Len(t, rows, want)
			require.Len(t, inputs, want)
			byKey := map[string]map[string]any{}
			for _, input := range inputs {
				byKey[stringValue(input["idempotency_key"])] = input
			}
			require.Len(t, byKey, want)
			for i, row := range rows {
				require.Equal(t, ids[i], row.SourceDocID)
				require.Equal(t, 1, row.ClientSubmissions)
				require.GreaterOrEqual(t, row.ElapsedSeconds, 0.0)
				wantInput := corpusRememberInput(items[i])
				if scenario == "legacy" {
					delete(wantInput, "target_space")
				}
				encoded, err := json.Marshal(wantInput)
				require.NoError(t, err)
				actual, err := json.Marshal(byKey["eval:"+ids[i]])
				require.NoError(t, err)
				require.JSONEq(t, string(encoded), string(actual))
				if i > 0 {
					require.False(t, row.StartedAt.Before(rows[0].FinishedAt))
				}
			}
			if !strings.HasSuffix(scenario, "failure") {
				imports := []PairImportAttempt{}
				for i, item := range items {
					hash, err := pairRequestHash(item)
					version := "dense-mem.v2.7.0"
					if scenario == "legacy" {
						hash, err = pairLegacyRequestHash(item)
						version = "dense-mem.v2.6.3"
					}
					require.NoError(t, err)
					imports = append(imports, PairImportAttempt{Scope: c.Baseline.Scope, SourceDocID: item.SourceDocID, AttemptID: stringValue(rows[i].Response["submission_id"]),
						RequestHash: hash, ContractVersion: version, Outcome: "completed", EvidenceIDs: []string{"evidence-" + item.SourceDocID}, ContentHashes: []string{pairBytesHash([]byte(item.Content))}, Live: true})
				}
				snapshot := commandNativeSnapshot(t, c.Baseline, imports)
				progress, err := VerifyPairProbe(c, "baseline", directory, snapshot)
				require.NoError(t, err)
				require.True(t, progress.Complete)
				if scenario == "current" {
					disabled := snapshot
					disabled.Ontology = json.RawMessage(`{"enabled":false,"paused":false}`)
					disabled.Community = json.RawMessage(`{"legacy_processing_retired":true,"effective_config":{"enabled":false}}`)
					_, err := VerifyPairProbe(c, "baseline", directory, disabled)
					require.ErrorContains(t, err, "enabled and paused before ingestion")
				}
				for _, change := range []func(*PairImportAttempt){
					func(a *PairImportAttempt) { a.Scope.OwnerID = "other" },
					func(a *PairImportAttempt) { a.ContentHashes = []string{"changed"} },
					func(a *PairImportAttempt) { a.Live = false },
					func(a *PairImportAttempt) { a.AttemptID = "different-operation" },
					func(a *PairImportAttempt) { a.Outcome = "failed" },
				} {
					changed := append([]PairImportAttempt(nil), imports...)
					change(&changed[0])
					_, err := VerifyPairProbe(c, "baseline", directory, commandNativeSnapshot(t, c.Baseline, changed))
					require.Error(t, err)
				}
				snapshot.Search = json.RawMessage(`{"status":"drifted","expected_documents":5,"current_documents":4,"drifted_documents":1}`)
				_, err = VerifyPairProbe(c, "baseline", directory, snapshot)
				require.ErrorContains(t, err, "current search")
			}
			require.ErrorContains(t, RunPairProbe(c, "baseline", ids, directory, io.Discard), "must be new")
			require.Len(t, inputs, want)
		})
	}
}
