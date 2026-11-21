package evalharness

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadManifestValidatesExecutionPins(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	t.Setenv("GITHUB_RUN_ID", "42")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	valid := Manifest{Version: 1, RunID: "42-hotpotqa", WorkflowRunID: "42", SourceRevision: strings.Repeat("a", 40), Image: "ghcr.io/markhuangai/dense-mem@sha256:" + strings.Repeat("b", 64), RegistryHash: strings.Repeat("c", 64), Dataset: "hotpotqa", Mode: "fixtures", Operation: "prepare", SeedDir: "seed", BaseRelease: "v2.8.1-rc.5", Model: "dense-mem-testing", EmbeddingModel: "text-embedding-3-small", EmbeddingDimensions: 1536, ContextWindowTokens: 500000, ImportWorkers: 20, ModelDeadline: time.Now().UTC().Add(time.Hour).Truncate(time.Second), DataDir: "data", OutputDir: "public"}
	file := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, writeJSON(file, valid))
	actual, err := ReadManifest(file)
	require.NoError(t, err)
	require.Equal(t, valid, actual)
	for _, tc := range []struct {
		name          string
		workers, want int
	}{
		{"omitted", 0, 4}, {"minimum", 4, 4}, {"maximum", 10, 10}, {"too few", 3, 0}, {"too many", 11, 0},
	} {
		t.Run("answer workers "+tc.name, func(t *testing.T) {
			manifest := valid
			manifest.Operation, manifest.ImportWorkers, manifest.AnswerWorkers = "run", 0, tc.workers
			require.NoError(t, writeJSON(file, manifest))
			read, err := ReadManifest(file)
			if tc.want == 0 {
				require.ErrorContains(t, err, "answer_workers must be 4..10")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, read.AnswerWorkers)
		})
	}
	for _, tc := range []struct {
		name, message string
		change        func(*Manifest)
	}{
		{"unknown operation", "workflow-internal operation", func(m *Manifest) { m.Operation = "retired" }},
		{"version", "identity", func(m *Manifest) { m.Version = 2 }},
		{"run identity", "identity", func(m *Manifest) { m.RunID = "" }},
		{"workflow run", "identity", func(m *Manifest) { m.WorkflowRunID = "43" }},
		{"source revision", "identity", func(m *Manifest) { m.SourceRevision = strings.Repeat("z", 40) }},
		{"image digest", "identity", func(m *Manifest) { m.Image = "ghcr.io/markhuangai/dense-mem:latest" }},
		{"public hosted preparation", "operation", func(m *Manifest) { m.Mode = "live" }},
		{"hosted AI review skip", "skipping AI security review", func(m *Manifest) {
			m.SkipAISecurityReview = true
			m.SeedImage = "ghcr.io/markhuangai/dense-mem-eval-postgres@sha256:" + strings.Repeat("d", 64)
		}},
		{"mode", "mode", func(m *Manifest) { m.Mode = "local" }},
		{"retired replay mode", "mode", func(m *Manifest) { m.Mode = "replay" }},
		{"dataset ownership", "dataset job", func(m *Manifest) { m.Dataset = "musique" }},
		{"model", "model and context", func(m *Manifest) { m.Model = "" }},
		{"provider URL as model", "model and context", func(m *Manifest) { m.Model = "https://provider.example/v1" }},
		{"embedding model", "model and context", func(m *Manifest) { m.EmbeddingModel = "another-model" }},
		{"retired embedding model", "model and context", func(m *Manifest) { m.EmbeddingModel = "@cf/baai/bge-m3" }},
		{"embedding dimensions", "model and context", func(m *Manifest) { m.EmbeddingDimensions = 512 }},
		{"retired embedding dimensions", "model and context", func(m *Manifest) { m.EmbeddingDimensions = 1024 }},
		{"context", "model and context", func(m *Manifest) { m.ContextWindowTokens = 16383 }},
		{"answer workers during preparation", "answer_workers applies only to scored evaluation", func(m *Manifest) { m.AnswerWorkers = 4 }},
		{"missing import workers", "import workers", func(m *Manifest) { m.ImportWorkers = 0 }},
		{"negative import workers", "import workers", func(m *Manifest) { m.ImportWorkers = -1 }},
		{"excessive import workers", "import workers", func(m *Manifest) { m.ImportWorkers = 101 }},
		{"data directory", "data and output", func(m *Manifest) { m.DataDir = "" }},
		{"output directory", "data and output", func(m *Manifest) { m.OutputDir = "" }},
		{"registry identity", "data and output", func(m *Manifest) { m.RegistryHash = "short" }},
		{"missing deadline", "deadline", func(m *Manifest) { m.ModelDeadline = time.Time{} }},
		{"extended deadline", "deadline", func(m *Manifest) { m.ModelDeadline = time.Now().Add(331 * time.Minute) }},
		{"extended hosted ontology deadline", "deadline", func(m *Manifest) {
			m.Operation, m.ImportWorkers = "bake-ontology", 0
			m.SeedImage = "sha256:" + strings.Repeat("d", 64)
			m.BakeStateDir = t.TempDir()
			m.ModelDeadline = time.Now().Add(331 * time.Minute)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := valid
			tc.change(&manifest)
			require.NoError(t, writeJSON(file, manifest))
			_, err := ReadManifest(file)
			require.ErrorContains(t, err, tc.message)
		})
	}
	frozen := valid
	frozen.Mode, frozen.Operation, frozen.ImportWorkers = "live", "run", 0
	frozen.RunID, frozen.Condition = "42-hotpotqa-on", "on"
	frozen.SeedImage = "ghcr.io/markhuangai/dense-mem-eval-postgres@sha256:" + strings.Repeat("d", 64)
	frozen.ParentSeedImage = "ghcr.io/markhuangai/dense-mem-eval-postgres@sha256:" + strings.Repeat("e", 64)
	require.NoError(t, writeJSON(file, frozen))
	actual, err = ReadManifest(file)
	require.NoError(t, err)
	require.Equal(t, frozen.ParentSeedImage, actual.ParentSeedImage)
	require.Equal(t, frozen.SeedImage, actual.PostgresRuntimeImage())
	for _, tc := range []struct {
		name, message string
		change        func(*Manifest)
	}{
		{"scoring without a condition", "requires a frozen condition", func(m *Manifest) { m.RunID, m.Condition = "42-hotpotqa", "" }},
		{"unknown condition", "invalid evaluation condition", func(m *Manifest) { m.Condition = "both" }},
		{"unpinned parent", "distinct pinned parent", func(m *Manifest) { m.ParentSeedImage = "" }},
		{"self parent", "distinct pinned parent", func(m *Manifest) { m.ParentSeedImage = m.SeedImage }},
		{"off with a parent", "parent pin applies only", func(m *Manifest) { m.RunID, m.Condition = "42-hotpotqa-off", "off" }},
		{"score job with a bake checkpoint", "checkpoint applies only", func(m *Manifest) { m.BakeStateDir = t.TempDir() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := frozen
			tc.change(&manifest)
			require.NoError(t, writeJSON(file, manifest))
			_, err := ReadManifest(file)
			require.ErrorContains(t, err, tc.message)
		})
	}
	for _, tc := range []struct{ key, value string }{
		{"GITHUB_ACTIONS", "false"}, {"RUNNER_ENVIRONMENT", "self-hosted"}, {"GITHUB_RUN_ID", ""}, {"GITHUB_RUN_ATTEMPT", "2"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			require.NoError(t, writeJSON(file, valid))
			_, err := ReadManifest(file)
			require.ErrorContains(t, err, "fresh GitHub-hosted")
		})
	}
}

func TestReadManifestPermitsOnlyPublicSeedOperationsLocally(t *testing.T) {
	buildID := "local-" + strings.Repeat("a", 32)
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("EVAL_LOCAL_SEED_ID", buildID)
	base := Manifest{Version: 1, RunID: buildID + "-hotpotqa", SeedBuildID: buildID, SourceRevision: strings.Repeat("a", 40), Image: "ghcr.io/markhuangai/dense-mem@sha256:" + strings.Repeat("b", 64), RegistryHash: strings.Repeat("c", 64), Dataset: "hotpotqa", Mode: "live", SeedDir: "seed", BaseRelease: "v2.8.1-rc.5", Model: "dense-mem-testing", EmbeddingModel: "text-embedding-3-small", EmbeddingDimensions: 1536, ContextWindowTokens: 500000, ModelDeadline: time.Now().UTC().Add(time.Hour), DataDir: "data", OutputDir: "public"}
	for _, tc := range []struct {
		operation string
		workers   int
	}{
		{"bootstrap", 20}, {"prepare", 40}, {"verify-seed", 0},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			manifest := base
			manifest.Operation, manifest.ImportWorkers = tc.operation, tc.workers
			if tc.operation == "verify-seed" {
				manifest.SeedImage = "sha256:" + strings.Repeat("d", 64)
			}
			file := filepath.Join(t.TempDir(), "manifest.json")
			require.NoError(t, writeJSON(file, manifest))
			actual, err := ReadManifest(file)
			require.NoError(t, err)
			require.Equal(t, manifest, actual)
		})
	}
	t.Run("ontology preparation pins the parent, policy and deadline", func(t *testing.T) {
		valid := base
		valid.Operation = "bake-ontology"
		valid.BakeStateDir = t.TempDir()
		valid.ModelDeadline = time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
		valid.SeedImage = "ghcr.io/markhuangai/dense-mem-eval-postgres@sha256:" + strings.Repeat("d", 64)
		file := filepath.Join(t.TempDir(), "manifest.json")
		require.NoError(t, writeJSON(file, valid))
		parsed, err := ReadManifest(file)
		require.NoError(t, err)
		require.Equal(t, valid.SeedImage, parsed.PostgresRuntimeImage())
		require.Equal(t, valid.BakeStateDir, parsed.BakeStateDir)
		require.Equal(t, valid.ModelDeadline, parsed.ModelDeadline)
		for _, tc := range []struct {
			name, message string
			change        func(*Manifest)
		}{
			{"relative checkpoint", "private checkpoint", func(m *Manifest) { m.BakeStateDir = "relative" }},
			{"floating parent", "pinned seed", func(m *Manifest) { m.SeedImage = "ghcr.io/markhuangai/dense-mem-eval-postgres:hotpotqa-latest" }},
			{"incorrect context", "verified model settings", func(m *Manifest) { m.ContextWindowTokens = 300000 }},
			{"too much time", "exceeds the campaign limit", func(m *Manifest) { m.ModelDeadline = time.Now().Add(49 * time.Hour) }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				manifest := valid
				tc.change(&manifest)
				require.NoError(t, writeJSON(file, manifest))
				_, err := ReadManifest(file)
				require.ErrorContains(t, err, tc.message)
			})
		}
	})

	for _, tc := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"scoring", func(m *Manifest) { m.Operation = "run" }},
		{"fixtures", func(m *Manifest) { m.Mode = "fixtures" }},
		{"hosted identity", func(m *Manifest) { m.WorkflowRunID = "42" }},
		{"different local build", func(m *Manifest) { m.SeedBuildID = "local-" + strings.Repeat("f", 32) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := base
			manifest.Operation, manifest.ImportWorkers = "prepare", 20
			tc.change(&manifest)
			file := filepath.Join(t.TempDir(), "manifest.json")
			require.NoError(t, writeJSON(file, manifest))
			_, err := ReadManifest(file)
			require.ErrorContains(t, err, "local execution permits only public seed preparation and restore verification")
		})
	}

	prepare := base
	prepare.Operation, prepare.ImportWorkers, prepare.SkipAISecurityReview = "prepare", 20, true
	prepare.SeedImage = "ghcr.io/markhuangai/dense-mem-eval-postgres@sha256:" + strings.Repeat("d", 64)
	file := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, writeJSON(file, prepare))
	actual, err := ReadManifest(file)
	require.NoError(t, err)
	require.Equal(t, prepare, actual)
	for _, tc := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"bootstrap", func(m *Manifest) { m.Operation = "bootstrap" }},
		{"restore verification", func(m *Manifest) { m.Operation, m.ImportWorkers = "verify-seed", 0 }},
		{"unpinned bootstrap", func(m *Manifest) { m.SeedImage = "ghcr.io/markhuangai/dense-mem-eval-postgres:bootstrap-latest" }},
	} {
		t.Run("AI review skip during "+tc.name, func(t *testing.T) {
			manifest := prepare
			tc.change(&manifest)
			require.NoError(t, writeJSON(file, manifest))
			_, err := ReadManifest(file)
			require.ErrorContains(t, err, "skipping AI security review requires preparation from a public seed bootstrap")
		})
	}

	prepare.ResumeScope, prepare.SkipAISecurityReview = &Scope{}, false
	require.NoError(t, writeJSON(file, prepare))
	actual, err = ReadManifest(file)
	require.NoError(t, err)
	require.Equal(t, prepare, actual)
	for _, tc := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"bootstrap", func(m *Manifest) { m.Operation = "bootstrap" }},
		{"restore verification", func(m *Manifest) { m.Operation, m.ImportWorkers = "verify-seed", 0 }},
		{"intake proof", func(m *Manifest) { m.IntakeProof, m.Image = true, "sha256:"+strings.Repeat("a", 64) }},
		{"unpinned bootstrap", func(m *Manifest) { m.SeedImage = "" }},
	} {
		t.Run("resume during "+tc.name, func(t *testing.T) {
			m := prepare
			tc.change(&m)
			require.NoError(t, writeJSON(file, m))
			_, err := ReadManifest(file)
			require.ErrorContains(t, err, "resuming requires a local public seed build")
		})
	}
}
