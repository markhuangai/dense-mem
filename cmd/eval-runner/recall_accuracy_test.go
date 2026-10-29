package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/stretchr/testify/require"
)

func TestEvalRunnerRecallAccuracyCLI(t *testing.T) {
	out := filepath.Join(t.TempDir(), "cohort")
	stdout, stderr, err := runTokenDiagnosticsCLI(t, nil, "-mode", "generate-recall-accuracy", "-out", out)
	require.NoError(t, err, stderr)
	require.Empty(t, stderr)
	require.Contains(t, stdout, "seed_id=recall_accuracy_v1 cases=24")
	manifest, err := evalharness.LoadSeedManifest(filepath.Join(out, "seed_manifest.json"))
	require.NoError(t, err)
	require.Equal(t, evalharness.SeedSchemaVersionV2, manifest.SchemaVersion)
	before, err := os.ReadFile(filepath.Join(out, "source_lock.json"))
	require.NoError(t, err)
	stdout, stderr, err = runTokenDiagnosticsCLI(t, nil, "-mode", "generate-recall-accuracy", "-out", out)
	require.Error(t, err)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "already exist")
	after, err := os.ReadFile(filepath.Join(out, "source_lock.json"))
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, stderr, err = runTokenDiagnosticsCLI(t, nil, "-mode", "generate-recall-accuracy")
	require.Error(t, err)
	require.Contains(t, stderr, "output directory is required")
}
