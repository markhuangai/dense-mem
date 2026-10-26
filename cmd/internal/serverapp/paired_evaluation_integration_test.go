//go:build integration

package serverapp

import (
	"context"
	"encoding/json"

	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPairedEvaluationRealServiceResumeFreezeAndCommonScore(t *testing.T) {
	root := t.TempDir()
	seed, suite, corpus := pairedSmallSeed(t, root)
	validated, err := evalharness.Run(context.Background(), evalharness.RunOptions{Mode: "validate", SeedManifestPath: seed, SuitePath: suite})
	require.NoError(t, err)
	suiteHash, err := evalharness.PairFileHash(suite)
	require.NoError(t, err)
	baseConfig := evalharness.PairConfig{SchemaVersion: evalharness.PairSchema, RunID: "paired-real-services", SeedManifest: seed, Suite: suite, SeedHash: validated.SeedHash, SuiteSHA256: suiteHash, OutDir: filepath.Join(root, "comparison-run"), Deadline: time.Now().Add(5 * time.Minute), Scorer: evalharness.PairBinary{SHA256: "sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}}
	var baseline evalharness.PairRelease
	var baselineProof []byte
	t.Run("capture_baseline", func(t *testing.T) {
		t.Setenv("POSTGRES_DSN", "")
		t.Setenv("DATABASE_URL", "")
		f := newPairedServiceFixture(t, "baseline")
		c := baseConfig
		c.RunID = "baseline-source"
		c.OutDir = filepath.Join(root, "baseline-source")
		c.Baseline = f.release
		c.Candidate = f.release
		capture := runPairedServiceSide(t, c, f, "baseline", corpus)
		baseline = f.release
		baseline.CaptureDir = capture
		baseline.CaptureSHA256, err = evalharness.PairFileHash(filepath.Join(capture, "pair_capture.json"))
		require.NoError(t, err)
		baselineProof, err = os.ReadFile(filepath.Join(capture, "pair_capture.json"))
		require.NoError(t, err)
	})
	require.NotEmpty(t, baseline.CaptureDir)
	t.Run("resume_candidate_and_reuse_baseline", func(t *testing.T) {
		t.Setenv("POSTGRES_DSN", "")
		t.Setenv("DATABASE_URL", "")
		f := newPairedServiceFixture(t, "candidate")
		c := baseConfig
		c.Baseline = baseline
		c.Candidate = f.release
		candidateCapture := runPairedServiceSide(t, c, f, "candidate", corpus)
		for side, capture := range map[string]string{"baseline": baseline.CaptureDir, "candidate": candidateCapture} {
			process, err := evalharness.PairProcessIdentity(os.Getpid())
			require.NoError(t, err)
			stage, err := evalharness.BeginPairStage(c, side, "score", process)
			require.NoError(t, err)
			_, err = evalharness.Run(context.Background(), evalharness.RunOptions{Mode: side, SeedManifestPath: seed, SuitePath: suite, TracesPath: filepath.Join(capture, "recall_traces.jsonl"), MappingPath: filepath.Join(capture, "knowledge_mapping.json"), OutDir: filepath.Join(stage.Directory, "native"), Gates: evalharness.GateOptions{MaxUnmappedSourceRefs: new(int)}})
			require.NoError(t, err)
			proof, err := evalharness.VerifyPairCaptureProof(c, map[string]evalharness.PairRelease{"baseline": c.Baseline, "candidate": c.Candidate}[side], capture)
			require.NoError(t, err)
			writePairedFixtureJSON(t, filepath.Join(stage.Directory, "capture-proof.json"), proof)
			writePairedFixtureJSON(t, filepath.Join(stage.Directory, "scorer.json"), c.Scorer)
			writePairedFixtureJSON(t, filepath.Join(stage.Directory, "native-exit.json"), map[string]any{"exit_code": 0, "finished_at": time.Now().UTC()})
			require.NoError(t, evalharness.FinishPairStage(c, stage.Directory))
		}
		report, err := evalharness.WritePairReport(c)
		require.NoError(t, err)
		require.Equal(t, "complete", report.Status)
		require.True(t, report.Sides["baseline"].ReusedCapture)
		require.Equal(t, len(corpus), report.Sides["candidate"].DistinctSources)
		require.Positive(t, report.Sides["candidate"].Usage.Unavailable)
		require.Equal(t, 2, report.Sides["candidate"].Scores.ScoredCaseCount)
		require.Zero(t, report.Sides["candidate"].Scores.UnmappedSourceRefs)
		reused, err := os.ReadFile(filepath.Join(baseline.CaptureDir, "pair_capture.json"))
		require.NoError(t, err)
		require.Equal(t, baselineProof, reused)
		changed := baseline
		changed.Models.Remember = "different-model"
		_, err = evalharness.VerifyPairCaptureProof(c, changed, baseline.CaptureDir)
		require.ErrorContains(t, err, "provenance")
		wrong := f.release
		wrong.Scope.OwnerID = uuid.NewString()
		_, err = evalharness.DecodePairObservation(wrong, f.snapshot(t), false)
		require.ErrorContains(t, err, "expected active team")
		wrong = f.release
		wrong.Scope.Generation++
		_, err = evalharness.DecodePairObservation(wrong, f.snapshot(t), false)
		require.Error(t, err)
	})
}

func runPairedServiceSide(t *testing.T, c evalharness.PairConfig, f *pairedServiceFixture, side string, corpus []evalharness.CorpusItem) string {
	t.Helper()
	ctx := context.Background()
	process, err := evalharness.PairProcessIdentity(os.Getpid())
	require.NoError(t, err)
	observe := func() evalharness.PairObservation {
		o, err := evalharness.DecodePairObservation(f.release, f.snapshot(t), false)
		require.NoError(t, err)
		return o
	}
	empty, err := evalharness.DecodePairObservation(f.release, f.snapshot(t), true)
	require.NoError(t, err)
	require.Empty(t, empty.Imports)
	legacyDeclaration := f.release
	legacyDeclaration.Protocol = "none"
	statusCode, wire := f.controlResponse(t, "/ontology/status")
	require.ErrorContains(t, evalharness.VerifyPairProtocol(legacyDeclaration, statusCode, wire), "without the ontology interface")
	require.NoError(t, evalharness.VerifyPairProtocol(f.release, statusCode, wire))
	capacity, err := evalharness.BeginPairStage(c, side, "capacity", process)
	require.NoError(t, err)
	writePairedFixtureJSON(t, filepath.Join(capacity.Directory, "observation.json"), empty)
	require.NoError(t, evalharness.FinishPairStage(c, capacity.Directory))
	interrupted, err := evalharness.BeginPairStage(c, side, "import", evalharness.PairProcess{PID: 1, BootID: "interrupted-host", StartTick: "1"})
	require.NoError(t, err)
	client := &evalharness.HTTPClient{BaseURL: f.mcp.URL, APIKey: "fixture"}
	_, err = client.ImportCorpusWithConcurrency(ctx, corpus[:1], 1)
	require.NoError(t, err)
	first := observe()
	progress, err := evalharness.EvaluatePairImport(f.release.Scope, corpus, first.Imports, false)
	require.NoError(t, err)
	require.Equal(t, []string{corpus[0].SourceDocID}, progress.CompletedSourceIDs)
	require.Equal(t, []string{corpus[1].SourceDocID, corpus[2].SourceDocID}, progress.PendingSourceIDs)
	calls := f.provider.calls
	resumed, err := evalharness.BeginPairStage(c, side, "import", evalharness.PairProcess{PID: 1, BootID: "mapping-export-host", StartTick: "1"})
	require.NoError(t, err)
	require.NotEqual(t, interrupted.Directory, resumed.Directory)
	require.FileExists(t, filepath.Join(interrupted.Directory, "interrupted.json"))
	resume := filepath.Join(resumed.Directory, "completed.txt")
	require.NoError(t, os.WriteFile(resume, []byte(strings.Join(progress.CompletedSourceIDs, "\n")+"\n"), 0o600))
	_, err = evalharness.Run(ctx, evalharness.RunOptions{Mode: "import", SeedManifestPath: c.SeedManifest, SuitePath: c.Suite, BaseURL: f.mcp.URL, APIKey: "fixture", ImportSeed: true, ImportConcurrency: 1, ResumeSourceDocIDsPath: resume, OutDir: filepath.Join(resumed.Directory, "native")})
	require.NoError(t, err)
	expired := c
	expired.Deadline = time.Now().Add(-time.Second)
	require.ErrorIs(t, evalharness.CompletePairImportMapping(expired, side, resumed.Directory), context.DeadlineExceeded)
	fullImport := observe()
	complete, err := evalharness.EvaluatePairImport(f.release.Scope, corpus, fullImport.Imports, false)
	require.NoError(t, err)
	require.True(t, complete.Complete)
	previousImport := resumed.Directory
	resumed, err = evalharness.BeginPairStage(c, side, "import", process)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(previousImport, "interrupted.json"))
	allDone := filepath.Join(resumed.Directory, "completed.txt")
	require.NoError(t, os.WriteFile(allDone, []byte(strings.Join(complete.CompletedSourceIDs, "\n")+"\n"), 0o600))
	_, err = evalharness.Run(ctx, evalharness.RunOptions{Mode: "import", SeedManifestPath: c.SeedManifest, SuitePath: c.Suite, BaseURL: f.mcp.URL, APIKey: "fixture", ImportSeed: true, ImportConcurrency: 1, ResumeSourceDocIDsPath: allDone, OutDir: filepath.Join(resumed.Directory, "native")})
	require.NoError(t, err)
	require.NoError(t, evalharness.CompletePairImportMapping(c, side, resumed.Directory))
	require.Equal(t, calls+len(corpus)-1, f.provider.calls, "resuming must not re-assess a durably completed source")
	calls = f.provider.calls
	_, err = client.ImportCorpusWithConcurrency(ctx, corpus[:1], 1)
	require.NoError(t, err)
	require.Equal(t, calls, f.provider.calls, "same-key replay must reuse native completion")
	imported := observe()
	progress, err = evalharness.EvaluatePairImport(f.release.Scope, corpus, imported.Imports, false)
	require.NoError(t, err)
	require.True(t, progress.Complete)
	writePairedFixtureJSON(t, filepath.Join(resumed.Directory, "observation.json"), imported)
	writePairedFixtureJSON(t, filepath.Join(resumed.Directory, "native-exit.json"), map[string]any{"exit_code": 0, "finished_at": time.Now().UTC()})
	require.NoError(t, evalharness.FinishPairStage(c, resumed.Directory))
	replay, err := evalharness.BeginPairStage(c, side, "import", process)
	require.NoError(t, err)
	require.True(t, replay.Reused)
	drain, err := evalharness.BeginPairStage(c, side, "drain", process)
	require.NoError(t, err)
	_, err = f.maintenance.Command(ctx, domain.OntologyMaintenanceCommand{Action: "resume", OperationKey: uuid.NewString()}, "", "")
	require.NoError(t, err)
	var frozen evalharness.PairObservation
	for turn := 0; turn < 100; turn++ {
		_, err = f.maintenance.RunTurn(ctx)
		require.NoError(t, err)
		_, err = f.projections.RunProjectionTurn(ctx)
		require.NoError(t, err)
		frozen = observe()
		ready, err := evalharness.EvaluatePairReadiness(f.release.Protocol, frozen, false)
		require.NoError(t, err)
		if ready.Ready {
			break
		}
	}

	checkBlocked := func(reason string) {
		o := observe()
		ready, err := evalharness.EvaluatePairReadiness(f.release.Protocol, o, false)
		require.NoError(t, err)
		require.False(t, ready.Ready, "%s: %+v", reason, ready)
		require.Equal(t, []string{reason}, ready.WaitingFor)
	}
	rls := postgres.NewRLS()
	require.NoError(t, rls.WithSystemTx(ctx, f.admin, func(tx *gorm.DB) error {
		drift := tx.Exec(`UPDATE search_documents SET search_state='pending',embedding=NULL WHERE team_id=?`, f.release.Scope.TeamID)
		if drift.Error != nil {
			return drift.Error
		}
		require.Positive(t, drift.RowsAffected)
		return nil
	}))
	checkBlocked("search")
	repaired, err := f.reconciliation.Run(ctx)
	require.NoError(t, err)
	require.Positive(t, repaired.UpdatedCount)
	readyAfterSearch, err := evalharness.EvaluatePairReadiness(f.release.Protocol, observe(), false)
	require.NoError(t, err)
	require.True(t, readyAfterSearch.Ready)
	for _, query := range []string{
		`UPDATE community_topic_work SET status='pending' WHERE team_id=?`,
		`UPDATE community_topic_versions SET version=version+1 WHERE team_id=? AND dependency_key LIKE 'relationship:%'`,
	} {
		require.NoError(t, rls.WithSystemTx(ctx, f.admin, func(tx *gorm.DB) error {
			drift := tx.Exec(query, f.release.Scope.TeamID)
			if drift.Error != nil {
				return drift.Error
			}
			require.Positive(t, drift.RowsAffected)
			return nil
		}))
		checkBlocked("pending_projections")
		for turn := 0; turn < 100; turn++ {
			_, err := f.projections.RunProjectionTurn(ctx)
			require.NoError(t, err)
			ready, err := evalharness.EvaluatePairReadiness(f.release.Protocol, observe(), false)
			require.NoError(t, err)
			if ready.Ready {
				break
			}
		}
		ready, err := evalharness.EvaluatePairReadiness(f.release.Protocol, observe(), false)
		require.NoError(t, err)
		require.True(t, ready.Ready)
	}
	_, err = f.maintenance.Command(ctx, domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: uuid.NewString()}, "", "")
	require.NoError(t, err)
	frozen = observe()
	ready, err := evalharness.EvaluatePairReadiness(f.release.Protocol, frozen, true)
	require.NoError(t, err)
	require.True(t, ready.Ready, "%+v", ready)
	require.Equal(t, imported.CanonicalHash, frozen.CanonicalHash)
	require.Equal(t, imported.SourceHash, frozen.SourceHash)
	writePairedFixtureJSON(t, filepath.Join(drain.Directory, "observation.json"), frozen)
	require.NoError(t, evalharness.FinishPairStage(c, drain.Directory))
	capture, err := evalharness.BeginPairStage(c, side, "capture", process)
	require.NoError(t, err)
	writePairedFixtureJSON(t, filepath.Join(capture.Directory, "before.json"), frozen)
	_, err = evalharness.Run(ctx, evalharness.RunOptions{Mode: side, SeedManifestPath: c.SeedManifest, SuitePath: c.Suite, BaseURL: f.mcp.URL, APIKey: "fixture", MappingPath: filepath.Join(resumed.Directory, "native", "knowledge_mapping.json"), OutDir: filepath.Join(capture.Directory, "native"), Gates: evalharness.GateOptions{MaxUnmappedSourceRefs: new(int)}})
	require.NoError(t, err)
	writePairedFixtureJSON(t, filepath.Join(capture.Directory, "observation.json"), observe())
	writePairedFixtureJSON(t, filepath.Join(capture.Directory, "native-exit.json"), map[string]any{"exit_code": 0, "finished_at": time.Now().UTC()})
	require.NoError(t, evalharness.FinishPairStage(c, capture.Directory))
	require.NoError(t, evalharness.VerifyPairStage(c, capture.Directory))
	return filepath.Join(capture.Directory, "native")
}

func pairedSmallSeed(t *testing.T, root string) (string, string, []evalharness.CorpusItem) {
	t.Helper()
	corpus := []evalharness.CorpusItem{}
	for index, name := range []string{"Alpha", "Beta", "Gamma"} {
		content := "Project " + name + " stores durable memory in PostgreSQL."
		proposal := map[string]any{"ref": "stores", "subject": map[string]any{"name": "Project " + name, "entity_kind": "project"}, "predicate": map[string]any{"proposed_key": "stores_memory_" + strings.ToLower(name)}, "object": map[string]any{"value": map[string]any{"type": "string", "value": "PostgreSQL"}}, "polarity": "+", "evidence_indices": []int{0}}
		corpus = append(corpus, evalharness.CorpusItem{SourceDocID: []string{"one", "two", "unjudged"}[index], Content: content, Relationships: []any{proposal}})
	}
	manifest := evalharness.SeedManifest{SchemaVersion: evalharness.SeedSchemaVersionV2, SeedID: "paired-service-fixture", CorpusFile: "corpus.jsonl", CasesFile: "cases.jsonl", QrelsFile: "qrels.jsonl", RelationshipCount: 3}
	seed, suite := filepath.Join(root, "seed_manifest.json"), filepath.Join(root, "suite.jsonl")
	writePairedFixtureJSON(t, seed, manifest)
	writeRows := func(path string, rows []any) {
		f, err := os.Create(path)
		require.NoError(t, err)
		defer f.Close()
		for _, row := range rows {
			require.NoError(t, json.NewEncoder(f).Encode(row))
		}
	}
	writeRows(filepath.Join(root, "corpus.jsonl"), []any{corpus[0], corpus[1], corpus[2]})
	writeRows(filepath.Join(root, "cases.jsonl"), []any{evalharness.Case{CaseID: "one", Query: corpus[0].Content, Limit: 10, Slices: []string{"storage"}}, evalharness.Case{CaseID: "two", Query: corpus[1].Content, Limit: 10, Slices: []string{"storage"}}})
	writeRows(filepath.Join(root, "qrels.jsonl"), []any{evalharness.QRel{CaseID: "one", RequiredRefs: []evalharness.Ref{{Type: "evidence", SourceDocID: "one", Grade: 1}}}, evalharness.QRel{CaseID: "two", RequiredRefs: []evalharness.Ref{{Type: "evidence", SourceDocID: "two", Grade: 1}}}})
	writeRows(suite, []any{evalharness.SuiteCase{CaseID: "one"}, evalharness.SuiteCase{CaseID: "two"}})
	return seed, suite, corpus
}

func writePairedFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, encoded, 0o600))
}
