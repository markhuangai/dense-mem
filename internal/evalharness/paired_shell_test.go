package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestPairShellHTTPRequestsJSON(t *testing.T) {
	script, err := filepath.Abs("../../tests/eval/scripts/paired_eval.sh")
	require.NoError(t, err)
	t.Setenv("PAIR_HTTP_TEST_TOKEN", "fixture-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "unsupported Accept header", http.StatusNotAcceptable)
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var request struct {
			Method string `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Method != "tools/list" {
			http.Error(w, "invalid RPC", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer server.Close()
	command := exec.Command("bash", "-c", `set -Eeuo pipefail
source "$1"
pair_http "$2" PAIR_HTTP_TEST_TOKEN POST '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'`, "paired-http-test", script, server.URL)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`, string(output))
}

func TestPairShellRecollectsOnlyVerifiedWindowRollover(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "eval-runner")
	output, err := exec.Command("go", "build", "-o", binary, "../../cmd/eval-runner").CombinedOutput()
	require.NoError(t, err, "%s", output)
	script, err := filepath.Abs("../../tests/eval/scripts/paired_eval.sh")
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		exitCode int
		requests int32
	}{
		{"matching", 0, 1}, {"recover", 0, 2}, {"exhausted", 75, 3},
		{"unverified_change", 1, 1}, {"foreign_scope", 1, 1}, {"missing_field", 1, 1},
		{"bad_capacity", 1, 1}, {"negative_work", 1, 1}, {"bad_provider_receipt", 1, 1},
		{"bad_import_receipt", 1, 1}, {"http_failure", 4, 1},
		{"expired", 124, 0}, {"expires_during_collection", 124, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := pairedFixture(t)
			configPath := filepath.Join(t.TempDir(), "pair.json")
			snapshot := pairWindowRolloverSnapshot(t, c.Candidate)
			var database map[string]any
			var status ontology.MaintenanceStatus
			require.NoError(t, json.Unmarshal(snapshot.Database, &database))
			require.NoError(t, json.Unmarshal(snapshot.Ontology, &status))
			switch tc.name {
			case "unverified_change":
				database["observed_at"] = status.Window.StartsAt.Add(time.Second)
			case "foreign_scope":
				database["scope"].(map[string]any)["generation"] = c.Candidate.Scope.Generation + 1
			case "missing_field":
				delete(database, "scope_verified")
			case "bad_capacity":
				status.Window.Policy.MaxConcurrency++
			case "negative_work":
				status.ActiveWork = -1
			case "bad_provider_receipt":
				database["provider_attempts"] = []any{map[string]any{}}
			case "bad_import_receipt":
				database["imports"] = []any{map[string]any{}}
			}
			var requests, mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutations.Add(1)
					http.Error(w, "unexpected mutation", http.StatusMethodNotAllowed)
					return
				}
				n := requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer fixture-token" || r.URL.Path != "/control/api/snapshot-fixture" {
					http.Error(w, "invalid fixture request", http.StatusForbidden)
					return
				}
				if tc.name == "http_failure" {
					http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
					return
				}
				if tc.name == "matching" || (tc.name == "recover" && n > 1) {
					database["legacy_window"].(map[string]any)["id"] = status.Window.ID
					database["observed_at"] = status.ObservedAt
				}
				if tc.name == "expires_during_collection" {
					expired := c
					expired.Deadline = time.Now().Add(-time.Second)
					if err := writePairJSON(configPath, expired); err != nil {
						t.Error(err)
						http.Error(w, "fixture deadline write failed", http.StatusInternalServerError)
						return
					}
				}
				current := map[string]any{"database": database, "ontology": status, "search": snapshot.Search, "community": snapshot.Community}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"data": current}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			c.Candidate.Runtime.ControlURL = server.URL
			if tc.name == "expired" {
				c.Deadline = time.Now().Add(-time.Second)
			}
			require.NoError(t, writePairJSON(configPath, c))
			command := exec.Command("bash", "-c", `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate; pair_stage_dir=""; pair_interrupted=false
trap 'pair_on_error "$?"' ERR
pair_begin drain
pair_snapshot() { pair_control /snapshot-fixture > "$1"; }
pair_collect_snapshot "$pair_stage_dir/observation-result.json" observe --pair-stage drain`)
			command.Env = append(os.Environ(), "PAIR_RUNNER="+binary, "PAIR_CONFIG="+configPath, "PAIR_SCRIPT="+script, "PAIR_CONTROL_TOKEN=fixture-token")
			output, err := command.CombinedOutput()
			if tc.exitCode == 0 {
				require.NoError(t, err, "%s", output)
			} else {
				var exited *exec.ExitError
				require.ErrorAs(t, err, &exited, "%s", output)
				require.Equal(t, tc.exitCode, exited.ExitCode(), "%s", output)
			}
			require.Equal(t, tc.requests, requests.Load(), "%s", output)
			require.Zero(t, mutations.Load(), "snapshot recollection must not submit provider or import work")
			stage := filepath.Join(c.OutDir, "candidate", "drain", "attempt-0001")
			paths, err := filepath.Glob(filepath.Join(stage, "snapshot-*.json"))
			require.NoError(t, err)
			require.Len(t, paths, int(tc.requests), "each full or rejected input must remain available")
			if tc.exitCode == 0 {
				var result struct {
					Observation PairObservation    `json:"observation"`
					Import      PairImportProgress `json:"import"`
					Readiness   PairReadiness      `json:"readiness"`
				}
				require.NoError(t, readPairJSON(filepath.Join(stage, "observation-result.json"), &result))
				require.Equal(t, status.Window.ID, result.Observation.WindowID)
				require.NoFileExists(t, filepath.Join(stage, "failure.json"))
				if tc.name == "recover" {
					var rejected PairNativeSnapshot
					require.NoError(t, readPairJSON(paths[0], &rejected))
					_, err := DecodePairObservation(c.Candidate, rejected, false)
					require.ErrorIs(t, err, ErrPairSnapshotWindowChanged)
				}
			} else {
				require.NoFileExists(t, filepath.Join(stage, "observation-result.json"))
				if tc.exitCode != 124 {
					require.FileExists(t, filepath.Join(stage, "failure.json"))
					process, err := PairProcessIdentity(os.Getpid())
					require.NoError(t, err)
					_, err = BeginPairStage(c, "candidate", "drain", process)
					require.ErrorContains(t, err, "retained failure; automatic retry is forbidden")
				}
			}
			require.NoDirExists(t, filepath.Join(c.OutDir, "candidate", "drain", "attempt-0002"))
		})
	}
}

func TestPairShellConvergenceReadDeadline(t *testing.T) {
	script, err := filepath.Abs("../../tests/eval/scripts/paired_eval.sh")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, path string
		remaining  time.Duration
		exitCode   int
		requests   int32
		canceled   bool
	}{
		{"slow_convergence", "/search/convergence", 4 * time.Minute, 0, 1, false},
		{"ordinary_read_timeout", "/ontology/status", 4 * time.Minute, 4, 1, true},
		{"campaign_deadline", "/search/convergence", 3 * time.Second, 124, 1, true},
		{"expired_campaign", "/search/convergence", -time.Second, 124, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			handled := make(chan bool, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-token" {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				// A valid convergence scan can exceed the ordinary 30-second control-read limit.
				timer := time.NewTimer(31 * time.Second)
				defer timer.Stop()
				select {
				case <-r.Context().Done():
					handled <- true
					return
				case <-timer.C:
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"data":{"status":"converged"}}`))
					handled <- false
				}
			}))
			t.Cleanup(server.Close)
			config := pairedFixture(t)
			config.Candidate.Runtime.ControlURL = server.URL
			config.Deadline = time.Now().Add(tc.remaining)
			path := filepath.Join(t.TempDir(), "pair.json")
			require.NoError(t, writePairJSON(path, config))
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-c", `set -Eeuo pipefail
source "$1"
PAIR_CONFIG="$2"; pair_side=candidate
pair_control "$3"`, "paired-control-timeout-test", script, path, tc.path)
			command.Env = append(os.Environ(), "PAIR_CONTROL_TOKEN=fixture-token")
			output, err := command.CombinedOutput()
			if tc.exitCode == 0 {
				require.NoError(t, err, "%s", output)
				require.JSONEq(t, `{"status":"converged"}`, string(output))
			} else {
				var exited *exec.ExitError
				require.ErrorAs(t, err, &exited, "%s", output)
				require.Equal(t, tc.exitCode, exited.ExitCode(), "%s", output)
				if tc.requests > 0 {
					require.Contains(t, string(output), "curl: (28)")
				}
			}
			require.Equal(t, tc.requests, requests.Load())
			if tc.requests > 0 {
				select {
				case canceled := <-handled:
					require.Equal(t, tc.canceled, canceled)
				case <-time.After(time.Second):
					t.Fatal("HTTP handler did not observe completion or cancellation")
				}
			}
		})
	}
}

func TestPairShellMaintenanceKeysRespectNativeContract(t *testing.T) {
	script, err := filepath.Abs("../../tests/eval/scripts/paired_eval.sh")
	require.NoError(t, err)
	keys := map[string]bool{}
	for _, tc := range []struct{ name, runID, side, stage, attempt, action string }{
		{"short", "pair-fixture", "candidate", "capacity", "attempt-0001", "pause"},
		{"boundary", strings.Repeat("r", 91), "baseline", "capacity", "attempt-0001", "pause"},
		{"overflow", strings.Repeat("r", 91), "candidate", "capacity", "attempt-0001", "pause"},
		{"maximum", strings.Repeat("r", 96), "candidate", "capacity", "attempt-0001", "pause"},
		{"side", strings.Repeat("r", 96), "baseline", "capacity", "attempt-0001", "pause"},
		{"stage", strings.Repeat("r", 96), "candidate", "drain", "attempt-0001", "pause"},
		{"attempt", strings.Repeat("r", 96), "candidate", "capacity", "attempt-0002", "pause"},
		{"action", strings.Repeat("r", 96), "candidate", "capacity", "attempt-0001", "resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := pairedFixture(t)
			c.RunID = tc.runID
			config := filepath.Join(t.TempDir(), "pair.json")
			require.NoError(t, writePairJSON(config, c))
			_, err := LoadPairConfig(config)
			require.NoError(t, err)
			dir := filepath.Join(t.TempDir(), tc.attempt)
			require.NoError(t, os.Mkdir(dir, 0o700))
			command := exec.Command("bash", "-c", `set -Eeuo pipefail
source "$1"
PAIR_CONFIG="$2"; pair_side="$3"; pair_stage="$4"; pair_stage_dir="$5"
pair_control() { printf '%s\n' "$3"; }
pair_command "$6"
cat "$pair_stage_dir/$6.json"`, "paired-key-test", script, config, tc.side, tc.stage, dir, tc.action)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
			var body struct {
				OperationKey string `json:"operation_key"`
			}
			require.NoError(t, json.Unmarshal(output, &body))
			_, err = ontology.PrepareMaintenanceCommand(domain.OntologyMaintenanceCommand{OperationKey: body.OperationKey, Action: tc.action})
			require.NoError(t, err, "key has %d bytes", len(body.OperationKey))
			original := strings.Join([]string{tc.runID, tc.side, tc.stage, tc.attempt, tc.action}, ":")
			if len(original) <= 128 {
				require.Equal(t, original, body.OperationKey, "preserve existing replay identities")
			}
			require.False(t, keys[body.OperationKey], "distinct commands must not share a key")
			keys[body.OperationKey] = true
			replay := exec.Command(command.Path, command.Args[1:]...)
			replayed, err := replay.CombinedOutput()
			require.NoError(t, err, "%s", replayed)
			require.Equal(t, output, replayed)
		})
	}
}

func TestPairShellDeadlineResumeAndCapturePreservation(t *testing.T) {
	t.Setenv("PAIR_API_KEY", "fixture")
	binary := filepath.Join(t.TempDir(), "eval-runner")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/eval-runner")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	script, err := filepath.Abs("../../tests/eval/scripts/paired_eval.sh")
	require.NoError(t, err)
	run := func(configPath, body string) ([]byte, error) {
		command := exec.Command("bash", "-c", body)
		command.Env = append(os.Environ(), "PAIR_RUNNER="+binary, "PAIR_CONFIG="+configPath, "PAIR_SCRIPT="+script)
		return command.CombinedOutput()
	}
	t.Run("http_failure_before_deadline_blocks_retry", func(t *testing.T) {
		requestedAt := make(chan time.Time, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestedAt <- time.Now()
			http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		c := pairedFixture(t)
		c.Candidate.Runtime.ControlURL = server.URL
		c.Deadline = time.Now().Add(3 * time.Second)
		configPath := filepath.Join(t.TempDir(), "pair.json")
		require.NoError(t, writePairJSON(configPath, c))
		t.Setenv("PAIR_CONTROL_TOKEN", "fixture-token")
		output, err := run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate; pair_stage_dir=""; pair_interrupted=false
trap 'pair_on_error "$?"' ERR
pair_begin import
jq() {
  if [[ "$*" == '-e .data' ]]; then sleep 4; fi
  command jq "$@"
}
pair_control /search/convergence`)
		var exited *exec.ExitError
		require.ErrorAs(t, err, &exited, "%s", output)
		require.Equal(t, 4, exited.ExitCode(), "%s", output)
		require.Contains(t, string(output), "curl: (22)")
		select {
		case observed := <-requestedAt:
			require.True(t, observed.Before(c.Deadline), "HTTP error must precede the deadline")
		default:
			t.Fatal("control request did not reach the HTTP server")
		}
		require.True(t, time.Now().After(c.Deadline))
		stage := filepath.Join(c.OutDir, "candidate", "import", "attempt-0001")
		require.FileExists(t, filepath.Join(stage, "failure.json"))
		c.Deadline = time.Now().Add(time.Minute)
		require.NoError(t, writePairJSON(configPath, c))
		process, err := PairProcessIdentity(os.Getpid())
		require.NoError(t, err)
		_, err = BeginPairStage(c, "candidate", "import", process)
		require.ErrorContains(t, err, "retained failure; automatic retry is forbidden")
		require.NoFileExists(t, filepath.Join(stage, "interrupted.json"))
		require.NoDirExists(t, filepath.Join(c.OutDir, "candidate", "import", "attempt-0002"))
	})
	t.Run("successful_native_completion", func(t *testing.T) {
		c := pairedFixture(t)
		configPath := filepath.Join(t.TempDir(), "pair.json")
		require.NoError(t, writePairJSON(configPath, c))
		process, err := PairProcessIdentity(os.Getpid())
		require.NoError(t, err)
		stage, err := BeginPairStage(c, "candidate", "validate", process)
		require.NoError(t, err)
		var intent PairStageIntent
		require.NoError(t, readPairJSON(filepath.Join(stage.Directory, "intent.json"), &intent))
		t.Setenv("PAIR_NATIVE_SUCCESS_STAGE", stage.Directory)
		output, err := run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate; pair_stage=validate; pair_stage_dir="$PAIR_NATIVE_SUCCESS_STAGE"; pair_interrupted=false
pair_native "$PAIR_RUNNER" --mode validate --seed "$(jq -er '.seed_manifest' "$PAIR_CONFIG")" --suite "$(jq -er '.suite' "$PAIR_CONFIG")" --out "$pair_stage_dir/native"`)
		require.NoError(t, err, "%s", output)
		var completion struct {
			Code       int       `json:"exit_code"`
			FinishedAt time.Time `json:"finished_at"`
		}
		require.NoError(t, readPairJSON(filepath.Join(stage.Directory, "native-exit.json"), &completion))
		require.Zero(t, completion.Code)
		require.False(t, completion.FinishedAt.Before(intent.StartedAt), "completion %s precedes start %s", completion.FinishedAt, intent.StartedAt)
		require.NotZero(t, completion.FinishedAt.Nanosecond(), "native completion lost subsecond precision")
		require.False(t, completion.FinishedAt.After(time.Now()))
		require.FileExists(t, filepath.Join(stage.Directory, "native-process.json"))
		require.NoError(t, FinishPairStage(c, stage.Directory))
		require.NoError(t, VerifyPairStage(c, stage.Directory))
	})
	c := pairedFixture(t)
	c.Deadline = time.Now().Add(3 * time.Second)
	configPath := filepath.Join(t.TempDir(), "pair.json")
	require.NoError(t, writePairJSON(configPath, c))
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate; pair_stage_dir=""; pair_interrupted=false
trap 'pair_on_error "$?"' ERR
pair_begin drain
sleep 3
pair_wait`)
	var exited *exec.ExitError
	require.ErrorAs(t, err, &exited, "%s", output)
	require.Equal(t, 124, exited.ExitCode(), "%s", output)
	first := filepath.Join(c.OutDir, "candidate", "drain", "attempt-0001")
	require.FileExists(t, filepath.Join(first, "intent.json"))
	require.NoFileExists(t, filepath.Join(first, "failure.json"))
	c.Deadline = time.Now().Add(time.Minute)
	require.NoError(t, writePairJSON(configPath, c))
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate
pair_begin drain`)
	require.NoError(t, err, "%s", output)
	require.FileExists(t, filepath.Join(first, "interrupted.json"))
	require.FileExists(t, filepath.Join(c.OutDir, "candidate", "drain", "attempt-0002", "intent.json"))

	imported, err := BeginPairStage(c, "candidate", "import", PairProcess{PID: 1, BootID: "prior-monitor", StartTick: "1"})
	require.NoError(t, err)
	require.NoError(t, writePairJSON(filepath.Join(imported.Directory, "native", "run_config.json"), RunConfig{Mode: "import", SeedHash: c.SeedHash}))
	require.NoError(t, writePairJSON(filepath.Join(imported.Directory, "native", "knowledge_mapping.json"), newKnowledgeMapping()))
	c.Deadline = time.Now().Add(-time.Second)
	require.NoError(t, writePairJSON(configPath, c))
	t.Setenv("PAIR_IMPORT_STAGE", imported.Directory)
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_stage_dir="$PAIR_IMPORT_STAGE"; pair_interrupted=false
trap 'pair_on_error "$?"' ERR
pair_tool complete-import-mapping --pair-side candidate --pair-directory "$pair_stage_dir"`)
	require.ErrorAs(t, err, &exited, "%s", output)
	require.Equal(t, 124, exited.ExitCode(), "%s", output)
	require.NoFileExists(t, filepath.Join(imported.Directory, "failure.json"))
	c.Deadline = time.Now().Add(time.Minute)
	require.NoError(t, writePairJSON(configPath, c))
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate
pair_begin import`)
	require.NoError(t, err, "%s", output)
	require.FileExists(t, filepath.Join(imported.Directory, "interrupted.json"))

	beforeLaunch, err := BeginPairStage(c, "candidate", "validate", PairProcess{PID: 1, BootID: "prior-launch", StartTick: "1"})
	require.NoError(t, err)
	c.Deadline = time.Now().Add(-time.Second)
	require.NoError(t, writePairJSON(configPath, c))
	t.Setenv("PAIR_VALIDATE_STAGE", beforeLaunch.Directory)
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate; pair_stage=validate; pair_stage_dir="$PAIR_VALIDATE_STAGE"; pair_interrupted=false
trap 'pair_on_error "$?"' ERR
pair_native "$PAIR_RUNNER" --mode validate`)
	require.ErrorAs(t, err, &exited, "%s", output)
	require.Equal(t, 124, exited.ExitCode(), "%s", output)
	require.NoFileExists(t, filepath.Join(beforeLaunch.Directory, "failure.json"))
	require.NoFileExists(t, filepath.Join(beforeLaunch.Directory, "native-process.json"))
	require.NoFileExists(t, filepath.Join(beforeLaunch.Directory, "native.log"))
	c.Deadline = time.Now().Add(time.Minute)
	require.NoError(t, writePairJSON(configPath, c))
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate
pair_begin validate`)
	require.NoError(t, err, "%s", output)
	require.FileExists(t, filepath.Join(beforeLaunch.Directory, "interrupted.json"))
	for _, destination := range []string{"same", "nested", "parent", "symlink"} {
		t.Run(destination, func(t *testing.T) {
			c := pairedFixture(t)
			capture := filepath.Join(t.TempDir(), "original")
			require.NoError(t, os.Mkdir(capture, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(capture, "evidence.json"), []byte("original capture"), 0o600))
			before, err := pairArtifactHashes(capture)
			require.NoError(t, err)
			c.Baseline.CaptureDir, c.Baseline.CaptureSHA256 = capture, c.Baseline.Runner.SHA256
			switch destination {
			case "same":
				c.OutDir = capture
			case "nested":
				c.OutDir = filepath.Join(capture, "comparison")
			case "parent":
				c.OutDir = filepath.Dir(capture)
			case "symlink":
				c.OutDir = filepath.Join(t.TempDir(), "alias")
				require.NoError(t, os.Symlink(capture, c.OutDir))
			}
			path := filepath.Join(t.TempDir(), "pair.json")
			require.NoError(t, writePairJSON(path, c))
			output, err := run(path, `source "$PAIR_SCRIPT"; pair_main`)
			require.Error(t, err, "%s", output)
			require.Contains(t, string(output), "must not overlap")
			after, err := pairArtifactHashes(capture)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
