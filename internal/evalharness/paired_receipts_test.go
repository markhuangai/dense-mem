package evalharness

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func pairedFixture(t *testing.T) PairConfig {
	t.Helper()
	root := writeEvalFixture(t)
	seed, suite := filepath.Join(root, "seed_manifest.json"), filepath.Join(root, "suite.jsonl")
	_, _, _, _, _, _, hash, err := loadRunInputs(seed, suite)
	require.NoError(t, err)
	suiteHash, err := PairFileHash(suite)
	require.NoError(t, err)
	binary := PairBinary{Path: filepath.Join(root, "runner"), SHA256: "sha256:" + strings.Repeat("b", 64), SourceRevision: strings.Repeat("a", 40)}
	models := PairModels{Provider: "openai", Remember: "fixture", Ontology: "fixture", BackingRevision: "unknown", VerifierEndpointSHA256: pairBytesHash([]byte("http://provider")), EmbeddingEndpointSHA256: pairBytesHash([]byte("http://provider")), EmbeddingModel: "embedding", EmbeddingDimensions: 3, EmbeddingIndexStrategy: "exact"}
	release := func(side string) PairRelease {
		return PairRelease{Release: side, SourceRevision: binary.SourceRevision, ImageID: "sha256:" + strings.Repeat("c", 64), Runner: binary,
			Scope: PairScope{TeamID: uuid.NewString(), OwnerID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1}, Models: models, Protocol: "adr0014", Capacity: PairCapacity{Global: 8, Team: 4, ContextWindowTokens: 300000}, ImportConcurrency: 1, AdmissionLimit: 4,
			Runtime: PairRuntime{Project: side, ServerContainer: side + "-server", PostgresContainer: side + "-postgres", BaseURL: "http://127.0.0.1:18080", ControlURL: "http://127.0.0.1:18090", CredentialID: uuid.NewString(), APIKeyEnv: "PAIR_API_KEY", ControlTokenEnv: "PAIR_CONTROL_TOKEN", StateDir: filepath.Join(root, side+"-runtime")}}
	}
	return PairConfig{SchemaVersion: PairSchema, RunID: "pair-fixture", SeedManifest: seed, Suite: suite, SeedHash: hash, SuiteSHA256: suiteHash, OutDir: filepath.Join(root, "pair"), Deadline: time.Now().Add(time.Hour), Baseline: release("baseline"), Candidate: release("candidate"), Scorer: binary}
}

func pairFixtureObservation(t *testing.T, c PairConfig, r PairRelease) PairObservation {
	t.Helper()
	o := drainedPairObservation()
	o.Scope, o.Policy, o.Paused, o.Model = r.Scope, r.Capacity, true, r.Models.Ontology
	_, corpus, _, _, _, _, _, err := loadRunInputs(c.SeedManifest, c.Suite)
	require.NoError(t, err)
	for _, item := range corpus {
		hash, err := pairRequestHash(item)
		require.NoError(t, err)
		o.Imports = append(o.Imports, PairImportAttempt{Scope: r.Scope, SourceDocID: item.SourceDocID, AttemptID: "attempt-" + item.SourceDocID, RequestHash: hash, Outcome: "completed", Live: true, EvidenceIDs: []string{"fragment-" + strings.TrimPrefix(item.SourceDocID, "doc-")}, ContentHashes: []string{pairBytesHash([]byte(item.Content))}})
	}
	return o
}

func writePairNativeCapture(t *testing.T, c PairConfig, dir string) {
	t.Helper()
	mapping := newKnowledgeMapping()
	for _, name := range []string{"alpha", "beta"} {
		addSourceMapping(&mapping, Ref{Type: "fragment", ID: "fragment-" + name, SourceDocID: "doc-" + name}, true)
	}
	require.NoError(t, os.MkdirAll(dir, 0o700))
	traces := []RecallTrace{{CaseID: "case-1", RankedRefs: []Ref{{Type: "fragment", ID: "fragment-alpha", Rank: 1}}}, {CaseID: "case-2", RankedRefs: []Ref{{Type: "fragment", ID: "fragment-beta", Rank: 1}}}}
	tracePath, mappingPath := filepath.Join(dir, "input-traces.jsonl"), filepath.Join(dir, "input-mapping.json")
	require.NoError(t, writeJSONL(tracePath, traces))
	require.NoError(t, writeJSONFile(mappingPath, mapping))
	_, err := Run(context.Background(), RunOptions{Mode: "baseline", SeedManifestPath: c.SeedManifest, SuitePath: c.Suite, OutDir: dir, TracesPath: tracePath, MappingPath: mappingPath, RunID: "paired-fixture", Gates: GateOptions{MaxUnmappedSourceRefs: new(int)}})
	require.NoError(t, err)
}

func writePairCompletedStage(t *testing.T, c PairConfig, side, stage string, edits ...func(*PairObservation)) string {
	t.Helper()
	process, err := PairProcessIdentity(os.Getpid())
	require.NoError(t, err)
	start, err := BeginPairStage(c, side, stage, process)
	require.NoError(t, err)
	r, err := pairRelease(c, side)
	require.NoError(t, err)
	o := pairFixtureObservation(t, c, r)
	for _, edit := range edits {
		edit(&o)
	}
	switch stage {
	case "validate":
		_, err = Run(context.Background(), RunOptions{Mode: "validate", SeedManifestPath: c.SeedManifest, SuitePath: c.Suite, OutDir: filepath.Join(start.Directory, "native")})
		require.NoError(t, err)
	case "capacity", "drain":
		require.NoError(t, writePairJSON(filepath.Join(start.Directory, "observation.json"), o))
	case "import", "capture":
		writePairNativeCapture(t, c, filepath.Join(start.Directory, "native"))
		require.NoError(t, writePairJSON(filepath.Join(start.Directory, "observation.json"), o))
		if stage == "capture" {
			require.NoError(t, writePairJSON(filepath.Join(start.Directory, "before.json"), o))
		}
	case "score":
		capture := r.CaptureDir
		if capture == "" {
			stage, err := latestPairStage(c, side, "capture")
			require.NoError(t, err)
			capture = filepath.Join(stage, "native")
		}
		proof, err := VerifyPairCaptureProof(c, r, capture)
		require.NoError(t, err)
		require.NoError(t, writePairJSON(filepath.Join(start.Directory, "capture-proof.json"), proof))
		require.NoError(t, writePairJSON(filepath.Join(start.Directory, "scorer.json"), c.Scorer))
		writePairNativeCapture(t, c, filepath.Join(start.Directory, "native"))
	}
	require.NoError(t, writePairJSON(filepath.Join(start.Directory, "native-exit.json"), map[string]any{"exit_code": 0, "finished_at": time.Now().UTC()}))
	require.NoError(t, FinishPairStage(c, start.Directory))
	return start.Directory
}

func TestPairStageReceiptsRejectDriftAndPreserveInterruptedAttempts(t *testing.T) {
	c := pairedFixture(t)
	old := PairProcess{PID: 1, BootID: "previous-boot", StartTick: "1"}
	start, err := BeginPairStage(c, "candidate", "import", old)
	require.NoError(t, err)
	require.True(t, start.Fresh)
	original, err := os.ReadFile(filepath.Join(start.Directory, "intent.json"))
	require.NoError(t, err)
	process, err := PairProcessIdentity(os.Getpid())
	require.NoError(t, err)
	resumed, err := BeginPairStage(c, "candidate", "import", process)
	require.NoError(t, err)
	require.NotEqual(t, start.Directory, resumed.Directory)
	require.FileExists(t, filepath.Join(start.Directory, "interrupted.json"))
	after, err := os.ReadFile(filepath.Join(start.Directory, "intent.json"))
	require.NoError(t, err)
	require.Equal(t, original, after)
	_, err = BeginPairStage(c, "candidate", "import", process)
	require.ErrorContains(t, err, "live process")
	require.NoError(t, PairFailure(resumed.Directory, "provider_unavailable"))
	_, err = BeginPairStage(c, "candidate", "import", old)
	require.ErrorContains(t, err, "retained failure")
	dir := writePairCompletedStage(t, c, "baseline", "validate")
	reused, err := BeginPairStage(c, "baseline", "validate", process)
	require.NoError(t, err)
	require.True(t, reused.Reused)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "native", "summary.json"), []byte("{}"), 0o600))
	require.Error(t, VerifyPairStage(c, dir))
}

func TestPairCaptureReuseChecksProvenanceAndEveryCase(t *testing.T) {
	c := pairedFixture(t)
	writePairCompletedStage(t, c, "baseline", "import")
	dir := writePairCompletedStage(t, c, "baseline", "capture")
	capture := filepath.Join(dir, "native")
	r := c.Baseline
	r.CaptureDir = capture
	var err error
	r.CaptureSHA256, err = PairFileHash(filepath.Join(capture, "pair_capture.json"))
	require.NoError(t, err)
	_, err = VerifyPairCaptureProof(c, r, capture)
	require.NoError(t, err)
	changed := r
	changed.Models.Remember = "different-model"
	_, err = VerifyPairCaptureProof(c, changed, capture)
	require.ErrorContains(t, err, "provenance")
	traces, err := LoadRecallTraces(filepath.Join(capture, "recall_traces.jsonl"))
	require.NoError(t, err)
	require.NoError(t, writeJSONL(filepath.Join(capture, "recall_traces.jsonl"), append(traces, traces[0])))
	require.ErrorContains(t, ValidatePairCapture(c, capture), "duplicate")
	_, err = VerifyPairCaptureProof(c, r, capture)
	require.ErrorContains(t, err, "artifacts")
}

func TestPairCommonReportRetainsIncompleteCoverageAndSourceCaptures(t *testing.T) {
	c := pairedFixture(t)
	recovered := func(o *PairObservation) {
		failed := o.Imports[0]
		failed.AttemptID, failed.Outcome, failed.Live = "failed-attempt", "failed", false
		failed.EvidenceIDs, failed.ContentHashes = nil, nil
		o.Imports = append(o.Imports, failed)
	}
	for _, side := range []string{"baseline", "candidate"} {
		writePairCompletedStage(t, c, side, "import", recovered)
		writePairCompletedStage(t, c, side, "capture", recovered)
		writePairCompletedStage(t, c, side, "score")
	}
	baseline, err := latestPairStage(c, "baseline", "capture")
	require.NoError(t, err)
	before, err := pairArtifactHashes(baseline)
	require.NoError(t, err)
	report, err := WritePairReport(c)
	require.NoError(t, err)
	require.Equal(t, "complete", report.Status)
	require.Zero(t, report.Comparison.RecallDelta)
	require.Equal(t, 2, report.Sides["candidate"].DistinctSources)
	require.Len(t, report.Sides["candidate"].Progress.Imports, 3)
	require.Equal(t, "failed", report.Sides["candidate"].Progress.Imports[2].Outcome)
	require.NotEmpty(t, report.Sides["candidate"].Scores.Slices)
	require.NotEmpty(t, report.Limitations)
	after, err := pairArtifactHashes(baseline)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.FileExists(t, filepath.Join(c.OutDir, "paired_report.md"))
}

func TestPairConfigFailsClosedBeforeRuntimeMutation(t *testing.T) {
	c := pairedFixture(t)
	path := filepath.Join(t.TempDir(), "pair.json")
	require.NoError(t, writePairJSON(path, c))
	loaded, err := LoadPairConfig(path)
	require.NoError(t, err)
	require.Equal(t, c.RunID, loaded.RunID)
	c.Candidate.ImportConcurrency = 8
	require.NoError(t, writePairJSON(path, c))
	_, err = LoadPairConfig(path)
	require.ErrorContains(t, err, "import/admission")
	c.Candidate.ImportConcurrency = 1
	c.Candidate.CaptureDir = "/existing/candidate"
	require.NoError(t, writePairJSON(path, c))
	_, err = LoadPairConfig(path)
	require.Error(t, err)
	c.Candidate.CaptureDir = ""
	payload, err := json.Marshal(c)
	require.NoError(t, err)
	payload = append(payload[:len(payload)-1], []byte(`,"api_key":"must-not-be-in-manifest"}`)...)
	require.NoError(t, os.WriteFile(path, payload, 0o600))
	_, err = LoadPairConfig(path)
	require.ErrorContains(t, err, "unknown field")
}

func TestPairInterruptedStageBoundaries(t *testing.T) {
	for _, stage := range []string{"validate", "capacity", "import", "drain", "capture", "score"} {
		t.Run(stage, func(t *testing.T) {
			c := pairedFixture(t)
			old := PairProcess{PID: 1, BootID: "old", StartTick: "1"}
			start, err := BeginPairStage(c, "candidate", stage, old)
			require.NoError(t, err)
			process, err := PairProcessIdentity(os.Getpid())
			require.NoError(t, err)
			resumed, err := BeginPairStage(c, "candidate", stage, process)
			if stage == "capture" || stage == "score" {
				require.ErrorContains(t, err, "not proven complete")
				require.NoFileExists(t, filepath.Join(start.Directory, "receipt.json"))
			} else {
				require.NoError(t, err)
				require.NotEqual(t, start.Directory, resumed.Directory)
			}
		})
	}
}

func TestPairReportIncompleteCoverage(t *testing.T) {
	c := pairedFixture(t)
	for _, side := range []string{"baseline", "candidate"} {
		writePairCompletedStage(t, c, side, "import")
		process, err := PairProcessIdentity(os.Getpid())
		require.NoError(t, err)
		stage, err := BeginPairStage(c, side, "capture", process)
		require.NoError(t, err)
		o := pairFixtureObservation(t, c, c.Candidate)
		if side == "baseline" {
			o = pairFixtureObservation(t, c, c.Baseline)
		}
		o.Failed = 1
		o.CoverageComplete = false
		writePairNativeCapture(t, c, filepath.Join(stage.Directory, "native"))
		require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "before.json"), o))
		require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "observation.json"), o))
		require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "native-exit.json"), map[string]any{"exit_code": 0, "finished_at": time.Now().UTC()}))
		require.NoError(t, FinishPairStage(c, stage.Directory))
		writePairCompletedStage(t, c, side, "score")
	}
	report, err := WritePairReport(c)
	require.NoError(t, err)
	require.Equal(t, "incomplete", report.Status)
	configPath := filepath.Join(t.TempDir(), "pair.json")
	require.NoError(t, writePairJSON(configPath, c))
	err = RunPairCommand(PairCommand{ConfigPath: configPath, Action: "report"}, io.Discard)
	require.ErrorContains(t, err, "comparison is incomplete")
	require.Contains(t, report.Sides["candidate"].Readiness.IncompleteReasons, "ontology_coverage")
}

func TestPairRuntimeStateCannotOverlapPreservedCapture(t *testing.T) {
	for _, side := range []string{"baseline", "candidate"} {
		for _, nested := range []bool{false, true} {
			c := pairedFixture(t)
			capture := t.TempDir()
			original := filepath.Join(capture, "original.json")
			require.NoError(t, os.WriteFile(original, []byte("preserved"), 0o600))
			c.Baseline.CaptureDir, c.Baseline.CaptureSHA256 = capture, c.Baseline.Runner.SHA256
			state := capture
			if nested {
				state = filepath.Join(capture, "runtime-state")
			}
			if side == "baseline" {
				c.Baseline.Runtime.StateDir = state
			} else {
				c.Candidate.Runtime.StateDir = state
			}
			config := filepath.Join(t.TempDir(), "pair.json")
			require.NoError(t, writePairJSON(config, c))
			err := RunPairCommand(PairCommand{ConfigPath: config, Action: "check-config"}, io.Discard)
			require.ErrorContains(t, err, "must not overlap")
			contents, err := os.ReadFile(original)
			require.NoError(t, err)
			require.Equal(t, "preserved", string(contents))
			entries, err := os.ReadDir(capture)
			require.NoError(t, err)
			require.Len(t, entries, 1)
		}
	}
}

func TestPairRecoveryRejectsAmbiguousArtifactsAndLiveNativeProcess(t *testing.T) {
	c := pairedFixture(t)
	dead := PairProcess{PID: 1, BootID: "previous-host", StartTick: "1"}
	process, err := PairProcessIdentity(os.Getpid())
	require.NoError(t, err)
	start, err := BeginPairStage(c, "candidate", "import", dead)
	require.NoError(t, err)
	nativePath := filepath.Join(start.Directory, "native-process.json")
	require.NoError(t, writePairJSON(nativePath, process))
	_, err = BeginPairStage(c, "candidate", "import", process)
	require.ErrorContains(t, err, "native subprocess is still running")
	require.NoFileExists(t, filepath.Join(start.Directory, "interrupted.json"))
	require.NoError(t, os.WriteFile(nativePath, []byte("{"), 0o600))
	_, err = BeginPairStage(c, "candidate", "import", process)
	require.ErrorContains(t, err, "invalid paired JSON")
	require.NoDirExists(t, filepath.Join(c.OutDir, "candidate", "import", "attempt-0002"))
	for _, name := range []string{"unexpected-file", "attempt-invalid", "attempt-0000"} {
		t.Run(name, func(t *testing.T) {
			c := pairedFixture(t)
			path := filepath.Join(c.OutDir, "candidate", "import", name)
			if name == "unexpected-file" {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte("unclaimed artifact"), 0o600))
			} else {
				require.NoError(t, os.MkdirAll(path, 0o700))
			}
			_, err := BeginPairStage(c, "candidate", "import", process)
			require.Error(t, err)
			require.NoDirExists(t, filepath.Join(c.OutDir, "candidate", "import", "attempt-0001"))
		})
	}
	c = pairedFixture(t)
	dir := writePairCompletedStage(t, c, "candidate", "validate")
	var receipt PairStageReceipt
	require.NoError(t, readPairJSON(filepath.Join(dir, "receipt.json"), &receipt))
	receipt.FinishedAt = receipt.Intent.StartedAt.Add(-time.Second)
	require.NoError(t, writePairJSON(filepath.Join(dir, "receipt.json"), receipt))
	require.ErrorContains(t, VerifyPairStage(c, dir), "identity or time")
	_, err = BeginPairStage(c, "candidate", "validate", process)
	require.Error(t, err)
}

func TestPairCaptureCompletionRequiresMatchingFrozenRuntime(t *testing.T) {
	cases := []struct {
		name string
		edit func(*PairObservation)
	}{
		{"scope", func(o *PairObservation) { o.Scope.OwnerID = uuid.NewString() }},
		{"active_capacity", func(o *PairObservation) { o.Policy.Team = 1 }},
		{"missing_source", func(o *PairObservation) { o.Imports = o.Imports[:1] }},
		{"canonical_drift", func(o *PairObservation) { o.CanonicalHash = pairBytesHash([]byte("changed")) }},
		{"disabled", func(o *PairObservation) { o.Enabled = false }},
		{"unpaused", func(o *PairObservation) { o.Paused = false }},
		{"wrong_evidence", func(o *PairObservation) { o.Imports[0].EvidenceIDs[0] = "different-evidence" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			c := pairedFixture(t)
			writePairCompletedStage(t, c, "candidate", "import")
			process, err := PairProcessIdentity(os.Getpid())
			require.NoError(t, err)
			stage, err := BeginPairStage(c, "candidate", "capture", process)
			require.NoError(t, err)
			o := pairFixtureObservation(t, c, c.Candidate)
			test.edit(&o)
			writePairNativeCapture(t, c, filepath.Join(stage.Directory, "native"))
			require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "before.json"), o))
			require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "observation.json"), o))
			require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "native-exit.json"), map[string]any{"exit_code": 0, "finished_at": time.Now().UTC()}))
			err = FinishPairStage(c, stage.Directory)
			if test.name == "wrong_evidence" {
				require.NoError(t, err)
				_, err = VerifyPairCaptureProof(c, c.Candidate, filepath.Join(stage.Directory, "native"))
				require.ErrorContains(t, err, "live import evidence")
			} else {
				require.Error(t, err)
				require.NoFileExists(t, filepath.Join(stage.Directory, "receipt.json"))
				require.NoFileExists(t, filepath.Join(stage.Directory, "native", "pair_capture.json"))
			}
		})
	}
}

func TestPairImportMappingDeadlineCannotCompleteAnInterruptedStage(t *testing.T) {
	t.Setenv("PAIR_API_KEY", "fixture")
	c := pairedFixture(t)
	stage, err := BeginPairStage(c, "candidate", "import", PairProcess{PID: 1, BootID: "previous-host", StartTick: "1"})
	require.NoError(t, err)
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "complete-import-mapping", Side: "candidate", Directory: t.TempDir()})
	require.ErrorContains(t, err, "belong to this paired run")
	config := filepath.Join(stage.Directory, "native", "run_config.json")
	require.NoError(t, writePairJSON(config, RunConfig{Mode: "baseline", SeedHash: c.SeedHash}))
	require.ErrorContains(t, CompletePairImportMapping(c, "candidate", stage.Directory), "incompatible native import")
	require.NoError(t, writePairJSON(config, RunConfig{Mode: "import", SeedHash: c.SeedHash}))
	mapping := filepath.Join(stage.Directory, "native", "knowledge_mapping.json")
	require.NoError(t, writePairJSON(mapping, newKnowledgeMapping()))
	before, err := os.ReadFile(mapping)
	require.NoError(t, err)
	observation := pairFixtureObservation(t, c, c.Candidate)
	observation.Imports = observation.Imports[:1]
	require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "observation.json"), observation))
	require.ErrorContains(t, CompletePairImportMapping(c, "candidate", stage.Directory), "incomplete")
	observation.Imports[0].Outcome = "failed"
	observation.Imports[0].Live = false
	observation.Imports[0].EvidenceIDs, observation.Imports[0].ContentHashes = nil, nil
	require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "observation.json"), observation))
	require.ErrorContains(t, CompletePairImportMapping(c, "candidate", stage.Directory), "automatic retry is forbidden")
	c.Deadline = time.Now().Add(-time.Second)
	_, err = runFixturePairCommand(t, c, PairCommand{Action: "complete-import-mapping", Side: "candidate", Directory: stage.Directory})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	after, err := os.ReadFile(mapping)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoFileExists(t, filepath.Join(stage.Directory, "receipt.json"))
}

func TestPairInterruptedCaptureAndScoreRequireEveryCompletionArtifact(t *testing.T) {
	for _, stage := range []string{"capture", "score"} {
		files := []string{"native-exit.json", "native/run_config.json", "native/knowledge_mapping.json", "native/summary.json", "native/recall_traces.jsonl", "native/retrieval_scores.jsonl"}
		if stage == "capture" {
			files = append(files, "before.json", "observation.json")
		} else {
			files = append(files, "capture-proof.json", "scorer.json")
		}
		for _, missing := range files {
			t.Run(stage+"/"+missing, func(t *testing.T) {
				c := pairedFixture(t)
				writePairCompletedStage(t, c, "candidate", "import")
				dir := writePairCompletedStage(t, c, "candidate", "capture")
				if stage == "score" {
					dir = writePairCompletedStage(t, c, "candidate", "score")
				}
				require.NoError(t, os.Remove(filepath.Join(dir, "receipt.json")))
				if stage == "capture" {
					require.NoError(t, os.Remove(filepath.Join(dir, "native", "pair_capture.json")))
				}
				var intent PairStageIntent
				require.NoError(t, readPairJSON(filepath.Join(dir, "intent.json"), &intent))
				intent.Process = PairProcess{PID: 1, BootID: "previous-host", StartTick: "1"}
				require.NoError(t, writePairJSON(filepath.Join(dir, "intent.json"), intent))
				require.NoError(t, os.Remove(filepath.Join(dir, missing)))
				before, err := pairArtifactHashes(dir)
				require.NoError(t, err)
				process, err := PairProcessIdentity(os.Getpid())
				require.NoError(t, err)
				_, err = BeginPairStage(c, "candidate", stage, process)
				require.ErrorContains(t, err, "not proven complete")
				after, err := pairArtifactHashes(dir)
				require.NoError(t, err)
				require.Equal(t, before, after, "ambiguous completion must preserve all retained artifacts")
				require.NoDirExists(t, filepath.Join(filepath.Dir(dir), "attempt-0002"))
			})
		}
	}
}

func TestPairCaptureProofRejectsIncompleteAndUnfrozenHistory(t *testing.T) {
	cases := []struct {
		name string
		edit func(*PairCaptureProof)
	}{
		{"missing_import", func(p *PairCaptureProof) { p.After.Imports = p.After.Imports[:1] }},
		{"foreign_receipt", func(p *PairCaptureProof) { p.After.Imports[0].Scope.OwnerID = uuid.NewString() }},
		{"before_scope", func(p *PairCaptureProof) { p.Before.Scope.Generation++ }},
		{"missing_observed_time", func(p *PairCaptureProof) { p.Before.ObservedAt = time.Time{} }},
		{"not_paused", func(p *PairCaptureProof) { p.Before.Paused = false }},
		{"window_changed", func(p *PairCaptureProof) { p.Before.WindowID = "previous-window" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			c := pairedFixture(t)
			writePairCompletedStage(t, c, "baseline", "import")
			dir := filepath.Join(writePairCompletedStage(t, c, "baseline", "capture"), "native")
			path := filepath.Join(dir, "pair_capture.json")
			var proof PairCaptureProof
			require.NoError(t, readPairJSON(path, &proof))
			test.edit(&proof)
			require.NoError(t, writePairJSON(path, proof))
			_, err := VerifyPairCaptureProof(c, c.Baseline, dir)
			require.Error(t, err)
		})
	}
	c := pairedFixture(t)
	writePairCompletedStage(t, c, "baseline", "import")
	dir := filepath.Join(writePairCompletedStage(t, c, "baseline", "capture"), "native")
	r := c.Baseline
	r.CaptureSHA256 = pairBytesHash([]byte("different proof"))
	_, err := VerifyPairCaptureProof(c, r, dir)
	require.ErrorContains(t, err, "proof hash mismatch")
	require.NoError(t, os.Symlink(filepath.Join(dir, "summary.json"), filepath.Join(dir, "linked-artifact")))
	_, err = VerifyPairCaptureProof(c, c.Baseline, dir)
	require.ErrorContains(t, err, "regular files")
}

func TestPairScoreCompletionRejectsDifferentScorerProofAndTrace(t *testing.T) {
	for _, artifact := range []string{"scorer", "proof", "trace"} {
		t.Run(artifact, func(t *testing.T) {
			c := pairedFixture(t)
			writePairCompletedStage(t, c, "candidate", "import")
			writePairCompletedStage(t, c, "candidate", "capture")
			dir := writePairCompletedStage(t, c, "candidate", "score")
			require.NoError(t, os.Remove(filepath.Join(dir, "receipt.json")))
			switch artifact {
			case "scorer":
				changed := c.Scorer
				changed.SHA256 = pairBytesHash([]byte("different scorer"))
				require.NoError(t, writePairJSON(filepath.Join(dir, "scorer.json"), changed))
			case "proof":
				var proof PairCaptureProof
				require.NoError(t, readPairJSON(filepath.Join(dir, "capture-proof.json"), &proof))
				proof.Identity.ImageID = pairBytesHash([]byte("different capture"))
				require.NoError(t, writePairJSON(filepath.Join(dir, "capture-proof.json"), proof))
			case "trace":
				path := filepath.Join(dir, "native", "recall_traces.jsonl")
				traces, err := LoadRecallTraces(path)
				require.NoError(t, err)
				traces[0].RankedRefs[0].Rank++
				require.NoError(t, writeJSONL(path, traces))
			}
			require.Error(t, FinishPairStage(c, dir))
			require.NoFileExists(t, filepath.Join(dir, "receipt.json"))
		})
	}
}

func TestPairStageRecoveryRejectsMissingOrDifferentIntent(t *testing.T) {
	c := pairedFixture(t)
	process, err := PairProcessIdentity(os.Getpid())
	require.NoError(t, err)
	root := filepath.Join(c.OutDir, "candidate", "import")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "attempt-0001"), 0o700))
	_, err = BeginPairStage(c, "candidate", "import", process)
	require.Error(t, err)
	require.NoDirExists(t, filepath.Join(root, "attempt-0002"))
	c = pairedFixture(t)
	stage, err := BeginPairStage(c, "candidate", "import", PairProcess{PID: 1, BootID: "previous-host", StartTick: "1"})
	require.NoError(t, err)
	original, err := os.ReadFile(filepath.Join(stage.Directory, "intent.json"))
	require.NoError(t, err)
	c.RunID = "another-run"
	_, err = BeginPairStage(c, "candidate", "import", process)
	require.ErrorContains(t, err, "stage identity changed")
	require.ErrorContains(t, FinishPairStage(c, stage.Directory), "intent is incompatible")
	after, err := os.ReadFile(filepath.Join(stage.Directory, "intent.json"))
	require.NoError(t, err)
	require.Equal(t, original, after)
}
