package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func runFixturePairCommand(t *testing.T, c PairConfig, command PairCommand) ([]byte, error) {
	t.Helper()
	command.ConfigPath = filepath.Join(filepath.Dir(c.OutDir), "pair-command.json")
	require.NoError(t, writePairJSON(command.ConfigPath, c))
	var out bytes.Buffer
	err := RunPairCommand(command, &out)
	return out.Bytes(), err
}

func commandNativeSnapshot(t *testing.T, r PairRelease, imports []PairImportAttempt) PairNativeSnapshot {
	t.Helper()
	encode := func(value any) json.RawMessage {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		return raw
	}
	database := pairDatabaseSnapshot{ObservedAt: time.Now(), Scope: r.Scope, ScopeVerified: true, CanonicalHash: pairBytesHash(nil), SourceHash: pairBytesHash(nil), Imports: imports, ProviderAttempts: []PairProviderAttempt{}}
	status := ontology.MaintenanceStatus{ObservedAt: time.Now(), Enabled: true, Paused: true, DiscoveryComplete: true, CoverageComplete: true,
		Window: &ontology.MaintenanceWindow{ID: "window", Policy: domain.OntologyMaintenanceConfig{MaxConcurrency: r.Capacity.Global, TeamMaxConcurrency: r.Capacity.Team, ContextWindowTokens: r.Capacity.ContextWindowTokens, Model: r.Models.Ontology}}}
	return PairNativeSnapshot{Database: encode(database), Ontology: encode(status),
		Search: encode(map[string]any{"status": "converged", "expected_documents": 0, "current_documents": 0, "drifted_documents": 0,
			"contract": map[string]any{"provider": r.Models.Provider, "model": r.Models.EmbeddingModel, "dimensions": r.Models.EmbeddingDimensions, "index_strategy": r.Models.EmbeddingIndexStrategy}}),
		Community: json.RawMessage(`{"projection_coverage":{"pending_topics":0,"stale_topics":0,"failed_topics":0,"coverage_complete":true}}`)}
}

func TestPairCommandValidatesRealBinaryAndRetainsRunIdentity(t *testing.T) {
	source, err := filepath.Abs("../..")
	require.NoError(t, err)
	clone := filepath.Join(t.TempDir(), "source")
	output, err := exec.Command("git", "clone", "--quiet", "--shared", source, clone).CombinedOutput()
	require.NoError(t, err, "%s", output)
	output, err = exec.Command("git", "-C", clone, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, "%s", output)
	revision := strings.TrimSpace(string(output))
	binary := filepath.Join(t.TempDir(), "eval-runner")
	build := exec.Command("go", "build", "-o", binary, "./cmd/eval-runner")
	build.Dir = clone
	output, err = build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	hash, err := PairFileHash(binary)
	require.NoError(t, err)
	c := pairedFixture(t)
	c.Scorer = PairBinary{Path: binary, SourceRevision: revision, SHA256: hash}
	for _, release := range []*PairRelease{&c.Baseline, &c.Candidate} {
		release.Runner, release.SourceRevision = c.Scorer, revision
	}
	t.Run("baseline_proof_before_identity", func(t *testing.T) {
		c := c
		c.OutDir = filepath.Join(t.TempDir(), "capture-source")
		writePairCompletedStage(t, c, "baseline", "import")
		capture := filepath.Join(writePairCompletedStage(t, c, "baseline", "capture"), "native")
		captureHashes, err := pairArtifactHashes(capture)
		require.NoError(t, err)
		proofHash, err := PairFileHash(filepath.Join(capture, "pair_capture.json"))
		require.NoError(t, err)
		c.OutDir = filepath.Join(t.TempDir(), "paired-output")
		c.Baseline.CaptureDir = capture
		c.Baseline.CaptureSHA256 = pairBytesHash([]byte("incorrect baseline proof"))
		identity := filepath.Join(c.OutDir, "identity.json")
		_, err = runFixturePairCommand(t, c, PairCommand{Action: "validate"})
		require.ErrorContains(t, err, "baseline capture proof hash mismatch")
		require.NoFileExists(t, identity)
		afterCapture, err := pairArtifactHashes(capture)
		require.NoError(t, err)
		require.Equal(t, captureHashes, afterCapture)

		c.Baseline.CaptureSHA256 = proofHash
		_, err = runFixturePairCommand(t, c, PairCommand{Action: "validate"})
		require.NoError(t, err)
		original, err := os.ReadFile(identity)
		require.NoError(t, err)
		assertRetained := func() {
			t.Helper()
			after, err := os.ReadFile(identity)
			require.NoError(t, err)
			require.Equal(t, original, after)
			afterCapture, err := pairArtifactHashes(capture)
			require.NoError(t, err)
			require.Equal(t, captureHashes, afterCapture)
		}
		assertRetained()
		_, err = runFixturePairCommand(t, c, PairCommand{Action: "validate"})
		require.NoError(t, err)
		assertRetained()

		changed := c
		changed.Baseline.CaptureSHA256 = pairBytesHash([]byte("incorrect baseline proof"))
		_, err = runFixturePairCommand(t, changed, PairCommand{Action: "validate"})
		require.ErrorContains(t, err, "baseline capture proof hash mismatch")
		assertRetained()
		changed = c
		changed.Candidate.Models.Remember = "changed"
		_, err = runFixturePairCommand(t, changed, PairCommand{Action: "validate"})
		require.ErrorContains(t, err, "paired run identity changed")
		assertRetained()

		require.NoError(t, os.WriteFile(filepath.Join(capture, "recall_traces.jsonl"), []byte("modified capture\n"), 0o600))
		_, err = runFixturePairCommand(t, c, PairCommand{Action: "validate"})
		require.ErrorContains(t, err, "capture artifacts differ from their immutable receipt")
		after, err := os.ReadFile(identity)
		require.NoError(t, err)
		require.Equal(t, original, after)
	})
	output, err = runFixturePairCommand(t, c, PairCommand{Action: "validate"})
	require.NoError(t, err)
	var result PairConfig
	require.NoError(t, json.Unmarshal(output, &result))
	require.Equal(t, revision, result.Candidate.SourceRevision)
	identity := filepath.Join(c.OutDir, "identity.json")
	original, err := os.ReadFile(identity)
	require.NoError(t, err)
	c.Deadline = c.Deadline.Add(time.Hour)
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "validate"})
	require.NoError(t, err)
	after, err := os.ReadFile(identity)
	require.NoError(t, err)
	require.Equal(t, original, after, "extending observation must preserve the original run identity and start")
	c.Candidate.Models.Remember = "changed"
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "validate"})
	require.ErrorContains(t, err, "run identity changed")
	after, err = os.ReadFile(identity)
	require.NoError(t, err)
	require.Equal(t, original, after)
	wrong := c.Scorer
	wrong.SourceRevision = strings.Repeat("f", 40)
	require.ErrorContains(t, VerifyPairBinary(wrong), "pinned clean source")
	c.OutDir = filepath.Join(t.TempDir(), "failed-run")
	c.Baseline.Runner.SHA256 = pairBytesHash([]byte("different binary"))
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "validate"})
	require.ErrorContains(t, err, "hash mismatch")
	require.NoFileExists(t, filepath.Join(c.OutDir, "identity.json"))
}

func TestPairCommandClaimsOnlyFreshOrMatchingRuntime(t *testing.T) {
	c := pairedFixture(t)
	input := filepath.Join(t.TempDir(), "native.json")
	require.NoError(t, writePairJSON(input, commandNativeSnapshot(t, c.Candidate, []PairImportAttempt{})))
	command := PairCommand{Action: "claim-runtime", Side: "candidate", Input: input}
	output, err := runFixturePairCommand(t, c, command)
	require.NoError(t, err)
	require.JSONEq(t, `{"fresh":true}`, string(output))
	claim := filepath.Join(c.Candidate.Runtime.StateDir, "paired_identity.json")
	original, err := os.ReadFile(claim)
	require.NoError(t, err)
	imports := pairFixtureObservation(t, c, c.Candidate).Imports
	require.NoError(t, writePairJSON(input, commandNativeSnapshot(t, c.Candidate, imports)))
	output, err = runFixturePairCommand(t, c, command)
	require.NoError(t, err)
	require.JSONEq(t, `{"fresh":false}`, string(output))
	c.RunID = "another-run"
	_, err = runFixturePairCommand(t, c, command)
	require.ErrorContains(t, err, "belongs to another paired run")
	after, err := os.ReadFile(claim)
	require.NoError(t, err)
	require.Equal(t, original, after)
	c.Candidate.Runtime.StateDir = filepath.Join(t.TempDir(), "unclaimed")
	_, err = runFixturePairCommand(t, c, command)
	require.ErrorContains(t, err, "fresh runtime")
	require.NoFileExists(t, filepath.Join(c.Candidate.Runtime.StateDir, "paired_identity.json"))
}

func TestPairCommandObservationDistinguishesPreflightCapacityAndFreeze(t *testing.T) {
	c := pairedFixture(t)
	input := filepath.Join(t.TempDir(), "native.json")
	snapshot := commandNativeSnapshot(t, c.Candidate, []PairImportAttempt{})
	require.NoError(t, writePairJSON(input, snapshot))
	command := PairCommand{Action: "observe", Side: "candidate", Input: input, Stage: "preflight", Fresh: true}
	output, err := runFixturePairCommand(t, c, command)
	require.NoError(t, err)
	var observed struct {
		Import    PairImportProgress `json:"import"`
		Readiness PairReadiness      `json:"readiness"`
	}
	require.NoError(t, json.Unmarshal(output, &observed))
	require.False(t, observed.Import.Complete)
	require.Len(t, observed.Import.PendingSourceIDs, 2)
	var status ontology.MaintenanceStatus
	require.NoError(t, json.Unmarshal(snapshot.Ontology, &status))
	writeStatus := func() {
		var err error
		snapshot.Ontology, err = json.Marshal(status)
		require.NoError(t, err)
		require.NoError(t, writePairJSON(input, snapshot))
	}
	status.Enabled = false
	writeStatus()
	output, err = runFixturePairCommand(t, c, command)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(output, &observed))
	require.Equal(t, []string{"capacity_configuration"}, observed.Readiness.WaitingFor)
	command.Stage = "drain"
	_, err = runFixturePairCommand(t, c, command)
	require.ErrorContains(t, err, "remain enabled")
	status.Enabled, status.Paused = true, false
	writeStatus()
	command.Frozen = true
	_, err = runFixturePairCommand(t, c, command)
	require.ErrorContains(t, err, "not frozen")
	status.Paused = true
	status.Window.Policy.TeamMaxConcurrency = 1
	writeStatus()
	_, err = runFixturePairCommand(t, c, command)
	require.ErrorContains(t, err, "capacity is not active")
	command.Stage, command.Frozen = "capacity", false
	_, err = runFixturePairCommand(t, c, command)
	require.NoError(t, err, "capacity inspection must expose queued policy before completion can reject it")
	command.Side = "unknown"
	_, err = runFixturePairCommand(t, c, command)
	require.ErrorContains(t, err, "side must be")
}

func TestPairCommandStageLifecycleRejectsUnprovenAndFailedWork(t *testing.T) {
	c := pairedFixture(t)
	command := PairCommand{Action: "begin", Side: "candidate", Stage: "validate", PID: os.Getpid()}
	output, err := runFixturePairCommand(t, c, command)
	require.NoError(t, err)
	var stage PairStageStart
	require.NoError(t, json.Unmarshal(output, &stage))
	require.True(t, stage.Fresh)
	finish := PairCommand{Action: "finish", Directory: stage.Directory}
	_, err = runFixturePairCommand(t, c, finish)
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(stage.Directory, "receipt.json"))
	_, err = Run(context.Background(), RunOptions{Mode: "validate", SeedManifestPath: c.SeedManifest, SuitePath: c.Suite, OutDir: filepath.Join(stage.Directory, "native")})
	require.NoError(t, err)
	require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "native-exit.json"), map[string]any{"exit_code": 0, "finished_at": time.Now().UTC()}))
	_, err = runFixturePairCommand(t, c, finish)
	require.NoError(t, err)
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "verify", Directory: stage.Directory})
	require.NoError(t, err)
	output, err = runFixturePairCommand(t, c, PairCommand{Action: "latest", Side: "candidate", Stage: "validate"})
	require.NoError(t, err)
	var latest PairStageStart
	require.NoError(t, json.Unmarshal(output, &latest))
	require.Equal(t, stage.Directory, latest.Directory)
	require.True(t, latest.Reused)
	command.Stage = "import"
	output, err = runFixturePairCommand(t, c, command)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(output, &stage))
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "fail", Directory: stage.Directory})
	require.NoError(t, err)
	_, err = runFixturePairCommand(t, c, command)
	require.ErrorContains(t, err, "retained failure")
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "finish", Directory: stage.Directory})
	require.ErrorContains(t, err, "failed stage")
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "fail", Directory: t.TempDir()})
	require.ErrorContains(t, err, "belong to this paired output")
}

func TestPairCommandInterfacesAndCapturedRuntimeFence(t *testing.T) {
	c := pairedFixture(t)
	input := filepath.Join(t.TempDir(), "response.json")
	for _, action := range []string{"check-config", "sql-import", "sql-ontology", "process"} {
		output, err := runFixturePairCommand(t, c, PairCommand{Action: action, PID: os.Getpid()})
		require.NoError(t, err)
		require.NotEmpty(t, output)
	}
	tools := map[string]any{"result": map[string]any{"tools": []map[string]string{{"name": "remember"}, {"name": "eval_list_knowledge_refs"}, {"name": "eval_run_recall_case"}}}}
	require.NoError(t, writePairJSON(input, tools))
	output, err := runFixturePairCommand(t, c, PairCommand{Action: "tools", Side: "candidate", Input: input})
	require.NoError(t, err)
	require.JSONEq(t, `{"verified":true}`, string(output))
	for _, payload := range []string{`{"result":{"tools":[{"name":"remember"},{"name":"remember"}]}}`, `{"error":{"code":-1}}`, `{}`} {
		require.NoError(t, os.WriteFile(input, []byte(payload), 0o600))
		_, err = runFixturePairCommand(t, c, PairCommand{Action: "tools", Side: "candidate", Input: input})
		require.Error(t, err)
	}
	native := commandNativeSnapshot(t, c.Candidate, []PairImportAttempt{})
	require.NoError(t, writePairJSON(input, map[string]any{"status": 200, "body": `{"data":` + string(native.Ontology) + `}`}))
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "protocol", Side: "candidate", Input: input})
	require.NoError(t, err)
	for _, side := range []string{"baseline", "candidate"} {
		writePairCompletedStage(t, c, side, "import")
		writePairCompletedStage(t, c, side, "capture")
		writePairCompletedStage(t, c, side, "score")
	}
	stage, err := latestPairStage(c, "candidate", "capture")
	require.NoError(t, err)
	capture := filepath.Join(stage, "native")
	original, err := pairArtifactHashes(capture)
	require.NoError(t, err)
	current := pairFixtureObservation(t, c, c.Candidate)
	require.NoError(t, writePairJSON(input, current))
	proof := PairCommand{Action: "capture-proof", Side: "candidate", Directory: capture, Input: input}
	_, err = runFixturePairCommand(t, c, proof)
	require.NoError(t, err)
	current.CanonicalHash = pairBytesHash([]byte("different canonical state"))
	require.NoError(t, writePairJSON(input, current))
	_, err = runFixturePairCommand(t, c, proof)
	require.ErrorContains(t, err, "changed before reuse")
	output, err = runFixturePairCommand(t, c, PairCommand{Action: "report"})
	require.NoError(t, err)
	var report PairReport
	require.NoError(t, json.Unmarshal(output, &report))
	require.Equal(t, "complete", report.Status)
	require.Equal(t, 2, report.Sides["candidate"].Scores.ScoredCaseCount)
	after, err := pairArtifactHashes(capture)
	require.NoError(t, err)
	require.Equal(t, original, after)
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "unknown"})
	require.ErrorContains(t, err, "unknown paired action")
}

func TestPairCommandAuthenticatesPreparedSessionAndContainerCapacity(t *testing.T) {
	c := pairedFixture(t)
	r := c.Candidate
	input := filepath.Join(t.TempDir(), "response.json")
	session := map[string]any{"data": map[string]any{"team": map[string]any{"id": r.Scope.TeamID},
		"credential":    map[string]any{"id": r.Runtime.CredentialID, "team_id": r.Scope.TeamID},
		"memory_access": map[string]any{"team_shared": map[string]any{"write": true}}}}
	require.NoError(t, writePairJSON(input, session))
	stdin, err := os.Open(input)
	require.NoError(t, err)
	originalStdin := os.Stdin
	os.Stdin = stdin
	defer func() { os.Stdin = originalStdin; stdin.Close() }()
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "session", Side: "candidate", Input: "-"})
	require.NoError(t, err)
	session["data"].(map[string]any)["memory_access"] = map[string]any{"team_shared": map[string]any{"write": false}}
	require.NoError(t, writePairJSON(input, session))
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "session", Side: "candidate", Input: input})
	require.ErrorContains(t, err, "shared write access")
	for _, fault := range []string{"none", "stopped", "foreign_project", "missing_port", "dimensions", "provider", "invalid_pool", "saturated_pool"} {
		t.Run(fault, func(t *testing.T) {
			server := pairContainer{ID: "server", Image: r.ImageID}
			server.State.Running = true
			server.Config.Labels = map[string]string{"com.docker.compose.project": r.Runtime.Project, "org.opencontainers.image.revision": r.SourceRevision, "org.opencontainers.image.version": r.Release}
			server.Config.Env = []string{"AI_API_URL=http://provider", "AI_VERIFIER_MODEL=fixture", "AI_API_EMBEDDING_MODEL=embedding", "AI_API_EMBEDDING_DIMENSIONS=3", "POSTGRES_MAX_OPEN_CONNS=8"}
			require.NoError(t, json.Unmarshal([]byte(`{"Ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"18080"}],"8090/tcp":[{"HostIp":"127.0.0.1","HostPort":"18090"}]}}`), &server.NetworkSettings))
			postgres := server
			postgres.ID = "postgres"
			switch fault {
			case "stopped":
				server.State.Running = false
			case "foreign_project":
				server.Config.Labels["com.docker.compose.project"] = "another-runtime"
			case "missing_port":
				server.NetworkSettings.Ports = nil
			case "dimensions":
				server.Config.Env[3] = "AI_API_EMBEDDING_DIMENSIONS=unknown"
			case "provider":
				server.Config.Env[0] = "AI_API_URL=http://different-provider"
			case "invalid_pool":
				server.Config.Env[4] = "POSTGRES_MAX_OPEN_CONNS=unknown"
			case "saturated_pool":
				server.Config.Env[4] = "POSTGRES_MAX_OPEN_CONNS=4"
			}
			require.NoError(t, writePairJSON(input, []pairContainer{server, postgres}))
			output, err := runFixturePairCommand(t, c, PairCommand{Action: "containers", Side: "candidate", Input: input})
			if fault == "none" {
				require.NoError(t, err)
				require.JSONEq(t, `{"verified":true}`, string(output))
			} else {
				require.Error(t, err)
				require.Empty(t, output)
			}
		})
	}
}

func TestPairCommandRejectsWrongSideAndUnreadableNativeInputs(t *testing.T) {
	c := pairedFixture(t)
	input := filepath.Join(t.TempDir(), "malformed.json")
	require.NoError(t, os.WriteFile(input, []byte("not JSON"), 0o600))
	for _, action := range []string{"protocol", "claim-runtime", "containers", "session", "tools", "observe", "begin", "capture-proof", "complete-import-mapping"} {
		t.Run(action+"/side", func(t *testing.T) {
			output, err := runFixturePairCommand(t, c, PairCommand{Action: action, Side: "unknown", PID: os.Getpid(), Input: input, Directory: filepath.Join(c.OutDir, "candidate", "import")})
			require.ErrorContains(t, err, "side must be")
			require.Empty(t, output)
		})
	}
	for _, action := range []string{"protocol", "claim-runtime", "containers", "session", "tools", "observe"} {
		t.Run(action+"/input", func(t *testing.T) {
			output, err := runFixturePairCommand(t, c, PairCommand{Action: action, Side: "candidate", Input: input})
			require.Error(t, err)
			require.Empty(t, output)
		})
	}
	require.NoDirExists(t, c.OutDir)
	_, err := runFixturePairCommand(t, c, PairCommand{Action: "begin", Side: "candidate", Stage: "import", PID: 2147483647})
	require.Error(t, err)
	require.NoDirExists(t, c.OutDir, "a process without a verifiable identity cannot own a stage")
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "begin", Side: "candidate", Stage: "unknown", PID: os.Getpid()})
	require.ErrorContains(t, err, "invalid paired stage")
	c.Deadline = time.Now().Add(-time.Second)
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "begin", Side: "candidate", Stage: "import", PID: os.Getpid()})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoDirExists(t, c.OutDir)
}

func TestPairCommandRejectsInvalidNativeReadinessAndProtocol(t *testing.T) {
	c := pairedFixture(t)
	input := filepath.Join(t.TempDir(), "native.json")
	require.NoError(t, writePairJSON(input, map[string]any{"status": 403, "body": "forbidden"}))
	_, err := runFixturePairCommand(t, c, PairCommand{Action: "protocol", Side: "candidate", Input: input})
	require.ErrorContains(t, err, "declared readiness protocol")
	for _, fault := range []string{"scope", "receipt", "negative_work"} {
		t.Run(fault, func(t *testing.T) {
			snapshot := commandNativeSnapshot(t, c.Candidate, pairFixtureObservation(t, c, c.Candidate).Imports)
			if fault == "negative_work" {
				var status ontology.MaintenanceStatus
				require.NoError(t, json.Unmarshal(snapshot.Ontology, &status))
				status.ActiveWork = -1
				snapshot.Ontology, err = json.Marshal(status)
			} else {
				var database pairDatabaseSnapshot
				require.NoError(t, json.Unmarshal(snapshot.Database, &database))
				if fault == "scope" {
					database.Scope.Generation++
				} else {
					database.Imports[0].RequestHash = pairBytesHash([]byte("different request"))
				}
				snapshot.Database, err = json.Marshal(database)
			}
			require.NoError(t, err)
			require.NoError(t, writePairJSON(input, snapshot))
			output, err := runFixturePairCommand(t, c, PairCommand{Action: "observe", Side: "candidate", Stage: "drain", Input: input})
			require.Error(t, err)
			require.Empty(t, output)
		})
	}
}
