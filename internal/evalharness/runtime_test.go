package evalharness

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvalRuntimePathsAndLocks(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "data")
	require.NoError(t, os.Mkdir(child, 0o700))
	identity, err := runtimePathIdentity(child)
	require.NoError(t, err)
	require.NoError(t, verifyRuntimePath(identity))
	require.NoError(t, os.Rename(child, child+"-original"))
	require.NoError(t, os.Mkdir(child, 0o700))
	require.ErrorContains(t, verifyRuntimePath(identity), "identity changed")
	require.NoError(t, os.Symlink(child, filepath.Join(root, "alias")))
	_, err = runtimeCleanPath(filepath.Join(root, "alias", "missing"))
	require.ErrorContains(t, err, "symlink")
	_, err = runtimeCleanPath("/")
	require.Error(t, err)
	lock, err := runtimeLock(filepath.Join(root, "runtime.lock"))
	require.NoError(t, err)
	defer lock.Close()
	_, err = runtimeLock(filepath.Join(root, "runtime.lock"))
	require.ErrorContains(t, err, "owned by another process")
}

func TestEvalRuntimeRequiresExplicitIntent(t *testing.T) {
	for _, action := range []string{"start", "record", "retire", "verify", "unknown"} {
		t.Run(action, func(t *testing.T) {
			err := RunRuntimeCommand(context.Background(), RuntimeCommand{Action: action}, io.Discard)
			require.Error(t, err)
		})
	}
	err := RunRuntimeCommand(context.Background(), RuntimeCommand{Action: "start", Directory: filepath.Join(t.TempDir(), "run"), Project: "fixture"}, io.Discard)
	require.ErrorContains(t, err, "--new-runtime or --supersedes")
}

func TestEvalRuntimePairGuardPreservesLegacyIdentity(t *testing.T) {
	c := pairedFixture(t)
	before, err := pairConfigHash(c)
	require.NoError(t, err)
	require.NoError(t, CheckPairRuntimeLifecycle(c, "candidate"), "unregistered legacy runtimes remain resumable")
	dir := c.Candidate.Runtime.StateDir
	require.NoError(t, os.MkdirAll(dir, 0o700))
	receipt := runtimeReceipt{SchemaVersion: runtimeSchema, ID: "retiring-fixture", Directory: dir, Project: c.Candidate.Runtime.Project, State: "retiring"}
	require.NoError(t, saveRuntimeReceipt(receipt))
	require.ErrorContains(t, CheckPairRuntimeLifecycle(c, "candidate"), "not active")
	after, err := pairConfigHash(c)
	require.NoError(t, err)
	require.Equal(t, before, after, "lifecycle state never changes the paired configuration hash")
}

func TestEvalRuntimeArchive(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "runtime", "postgres")
	require.NoError(t, os.MkdirAll(data, 0o700))
	input := filepath.Join(root, "input.json")
	require.NoError(t, os.WriteFile(input, []byte("private audit input\n"), 0o600))
	hash, err := PairFileHash(input)
	require.NoError(t, err)
	plan := runtimeRetentionPlan{SchemaVersion: runtimeSchema, ArchiveDir: filepath.Join(root, "archive"), Outcome: "incomplete"}
	for _, kind := range runtimeEvidenceKinds {
		plan.Evidence = append(plan.Evidence, runtimeEvidence{Kind: kind, Unavailable: "run ended before this stage"})
	}
	plan.Evidence[0] = runtimeEvidence{Kind: "frozen_inputs", Files: []runtimeArtifact{{Path: input, SHA256: hash}}}
	plan.Evidence[5] = runtimeEvidence{Kind: "private_audit", Files: []runtimeArtifact{{Path: input, SHA256: hash}}}
	receipt := runtimeReceipt{SchemaVersion: runtimeSchema, Directory: filepath.Dir(data), ID: "fixture"}
	bind, err := runtimePathIdentity(data)
	require.NoError(t, err)
	receipt.Binds = []runtimeBind{{Identity: bind}}
	require.NoError(t, validateRuntimeRetention(&plan, receipt))
	archive, err := archiveRuntimeEvidence(plan)
	require.NoError(t, err)
	require.NoError(t, verifyRuntimeArchive(archive))
	for _, file := range archive.Files {
		info, statErr := os.Stat(file.ArchivePath)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	_, err = archiveRuntimeEvidence(plan)
	require.NoError(t, err, "a completed copy is reusable after interruption")
	require.NoError(t, os.WriteFile(archive.Files[0].ArchivePath, []byte("changed"), 0o600))
	require.ErrorContains(t, verifyRuntimeArchive(archive), "hash")
	_, err = archiveRuntimeEvidence(plan)
	require.Error(t, err, "never overwrite corrupted retained evidence")
	plan.ArchiveDir = filepath.Join(data, "archive")
	require.ErrorContains(t, validateRuntimeRetention(&plan, receipt), "overlap")
	plan.ArchiveDir = filepath.Join(root, "archive")
	plan.PendingRuntimeDependencies = []string{"credential audit still needs PostgreSQL"}
	require.ErrorContains(t, validateRuntimeRetention(&plan, receipt), "dependencies")
	plan.PendingRuntimeDependencies = nil
	plan.Outcome = "complete"
	require.ErrorContains(t, validateRuntimeRetention(&plan, receipt), "capture")
}
