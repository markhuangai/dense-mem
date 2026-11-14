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
	valid := Manifest{Version: 1, RunID: "42-hotpotqa", WorkflowRunID: "42", SourceRevision: strings.Repeat("a", 40), Image: "ghcr.io/markhuangai/dense-mem@sha256:" + strings.Repeat("b", 64), RegistryHash: strings.Repeat("c", 64), Dataset: "hotpotqa", Mode: "live", Model: "dense-mem-testing", EmbeddingModel: "@cf/baai/bge-m3", EmbeddingDimensions: 1024, ContextWindowTokens: 500000, ModelDeadline: time.Now().UTC().Add(time.Hour).Truncate(time.Second), DataDir: "data", OutputDir: "public"}
	file := filepath.Join(t.TempDir(), "manifest.json")
	for _, mode := range []string{"live", "fixtures", "replay"} {
		t.Run(mode, func(t *testing.T) {
			manifest := valid
			manifest.Mode = mode
			if mode == "replay" {
				manifest.RunID = "previous-run-hotpotqa"
				manifest.ModelDeadline = time.Time{}
			}
			require.NoError(t, writeJSON(file, manifest))
			actual, err := ReadManifest(file)
			require.NoError(t, err)
			require.Equal(t, manifest, actual)
		})
	}
	for _, tc := range []struct {
		name, message string
		change        func(*Manifest)
	}{
		{"version", "identity", func(m *Manifest) { m.Version = 2 }},
		{"run identity", "identity", func(m *Manifest) { m.RunID = "" }},
		{"workflow run", "identity", func(m *Manifest) { m.WorkflowRunID = "43" }},
		{"source revision", "identity", func(m *Manifest) { m.SourceRevision = strings.Repeat("z", 40) }},
		{"image digest", "identity", func(m *Manifest) { m.Image = "ghcr.io/markhuangai/dense-mem:latest" }},
		{"mode", "mode", func(m *Manifest) { m.Mode = "local" }},
		{"dataset ownership", "dataset job", func(m *Manifest) { m.Dataset = "musique" }},
		{"model", "model and context", func(m *Manifest) { m.Model = "" }},
		{"provider URL as model", "model and context", func(m *Manifest) { m.Model = "https://provider.example/v1" }},
		{"embedding model", "model and context", func(m *Manifest) { m.EmbeddingModel = "another-model" }},
		{"embedding dimensions", "model and context", func(m *Manifest) { m.EmbeddingDimensions = 512 }},
		{"context", "model and context", func(m *Manifest) { m.ContextWindowTokens = 16383 }},
		{"data directory", "data and output", func(m *Manifest) { m.DataDir = "" }},
		{"output directory", "data and output", func(m *Manifest) { m.OutputDir = "" }},
		{"registry identity", "data and output", func(m *Manifest) { m.RegistryHash = "short" }},
		{"missing deadline", "deadline", func(m *Manifest) { m.ModelDeadline = time.Time{} }},
		{"extended deadline", "deadline", func(m *Manifest) { m.ModelDeadline = time.Now().Add(331 * time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := valid
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
			_, err := ReadManifest(file)
			require.ErrorContains(t, err, "fresh GitHub-hosted")
		})
	}
}
