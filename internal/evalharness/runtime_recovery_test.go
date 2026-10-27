package evalharness

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func evalRuntimeIncompletePlan(t *testing.T, root string) string {
	t.Helper()
	plan := runtimeRetentionPlan{SchemaVersion: runtimeSchema, ArchiveDir: filepath.Join(root, "archive"), Outcome: "incomplete"}
	for _, kind := range runtimeEvidenceKinds {
		plan.Evidence = append(plan.Evidence, runtimeEvidence{Kind: kind, Unavailable: "startup ended before the evaluation began"})
	}
	path := filepath.Join(root, "retention.json")
	require.NoError(t, writePairJSON(path, plan))
	return path
}

func TestEvalRuntimeDockerOwnershipAndStorageGuards(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	active := newEvalRuntimeFixture(t, ctx, false, "")
	protected, err := captureProtectedRuntimes(ctx, []string{active.command.Directory})
	require.NoError(t, err)
	actual, err := readRuntimeReceipt(active.command.Directory)
	require.NoError(t, err)

	t.Run("matching_hostname_does_not_hide_a_foreign_bind", func(t *testing.T) {
		hostname, err := os.Hostname()
		require.NoError(t, err)
		created, err := runtimeDocker(ctx, "container", "create", "--hostname", hostname, "--network", "none", "--label", runtimeOwnerLabel+"="+uuid.NewString(), "--mount", "type=bind,source="+actual.Binds[0].Identity.Path+",target=/shared,readonly", "busybox:1.37.0-musl", "sh", "-ec", "trap 'exit 0' TERM; while :; do sleep 0.1; done")
		require.NoError(t, err)
		id := strings.TrimSpace(string(created))
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			container, err := inspectRuntimeContainer(cleanupCtx, id)
			if err != nil {
				t.Errorf("foreign-bind fixture readback: %v", err)
				return
			}
			if err := stopRuntimeContainer(cleanupCtx, container, 10*time.Second); err != nil {
				t.Errorf("foreign-bind fixture stop: %v", err)
				return
			}
			if _, err := runtimeDocker(cleanupCtx, "container", "rm", id); err != nil {
				t.Errorf("foreign-bind fixture removal: %v", err)
			}
		})
		_, err = runtimeDocker(ctx, "container", "start", id)
		require.NoError(t, err)
		container, err := inspectRuntimeContainer(ctx, id)
		require.NoError(t, err)
		require.False(t, runtimeExecutorContainer(ctx, container))
		referenced, err := runtimeReferenced(ctx, actual, "bind", actual.Binds[0].Identity.Path)
		require.NoError(t, err)
		require.True(t, referenced)
		require.NoError(t, verifyProtectedRuntimes(ctx, protected))
	})

	t.Run("reused_project_cannot_be_recovered_or_retired", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "runtime")
		hash, err := PairFileHash(active.command.ComposeFile)
		require.NoError(t, err)
		intent := runtimeReceipt{SchemaVersion: runtimeSchema, ID: uuid.NewString(), Directory: dir, Project: actual.Project, State: "preparing", ComposeFile: active.command.ComposeFile, ComposeHash: hash, CreatedAt: time.Now().UTC()}
		require.NoError(t, saveRuntimeReceipt(intent))
		plan := evalRuntimeIncompletePlan(t, root)
		for _, action := range []string{"start", "retire"} {
			command := RuntimeCommand{Action: action, Directory: dir, RetentionPlan: plan}
			require.ErrorContains(t, RunRuntimeCommand(ctx, command, io.Discard), "ownership label")
			require.NoError(t, verifyProtectedRuntimes(ctx, protected))
			saved, err := readRuntimeReceipt(dir)
			require.NoError(t, err)
			require.Empty(t, saved.Containers, "a failed recovery cannot enroll foreign resources")
			require.Equal(t, "preparing", saved.State)
		}
	})

	t.Run("named_volume_overlap_blocks_compose", func(t *testing.T) {
		root := t.TempDir()
		var volume string
		for _, item := range actual.Volumes {
			if item.Project == actual.Project {
				volume = item.Name
			}
		}
		require.NotEmpty(t, volume)
		compose := map[string]any{
			"services": map[string]any{"server": map[string]any{"image": "busybox:1.37.0-musl", "command": []string{"sleep", "600"}, "volumes": []string{"shared:/data"}}},
			"volumes":  map[string]any{"shared": map[string]any{"name": volume, "external": true}},
		}
		composePath := filepath.Join(root, "compose.json")
		require.NoError(t, writePairJSON(composePath, compose))
		command := RuntimeCommand{Action: "start", Directory: filepath.Join(root, "runtime"), Project: "eval627-" + uuid.NewString(), ComposeFile: composePath, New: true}
		require.ErrorContains(t, RunRuntimeCommand(ctx, command, io.Discard), "named-volume storage overlaps")
		require.NoError(t, assertUnusedRuntimeProject(ctx, command.Project))
		require.NoError(t, verifyProtectedRuntimes(ctx, protected))
	})

	t.Run("retention_plan_in_disposable_storage_is_rejected", func(t *testing.T) {
		var databaseID string
		for _, container := range actual.Containers {
			if container.Service == "postgres" {
				databaseID = container.ID
			}
		}
		_, err := runtimeDocker(ctx, "container", "cp", active.planPath, databaseID+":/data/retention.json")
		require.NoError(t, err)
		command := RuntimeCommand{Action: "retire", Directory: active.command.Directory, RetentionPlan: filepath.Join(active.command.Directory, "postgres", "retention.json")}
		require.ErrorContains(t, RunRuntimeCommand(ctx, command, io.Discard), "retention plan overlaps")
		_, err = runtimeDocker(ctx, "container", "exec", databaseID, "test", "-f", "/data/retention.json")
		require.NoError(t, err)
		require.NoError(t, verifyProtectedRuntimes(ctx, protected))
	})
}

func TestEvalRuntimeDockerBuildBeforeContainerFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := evalRuntimeDockerRoot(t)
	project := "eval627-" + uuid.NewString()
	base := "busybox:1.37.0-musl"
	if _, err := runtimeDocker(ctx, "image", "inspect", base); err != nil {
		_, err = runtimeDocker(ctx, "image", "pull", base)
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM "+base+"\nLABEL io.dense-mem.eval.fixture="+project+"\n"), 0o600))
	compose := map[string]any{"services": map[string]any{"server": map[string]any{
		"image": project + ":build-only", "build": map[string]any{"context": root},
		"command": []string{"sleep", "600"},
	}}, "networks": map[string]any{"default": map[string]string{"driver": "eval627-missing-driver-" + uuid.NewString()}}}
	composePath := filepath.Join(root, "compose.json")
	require.NoError(t, writePairJSON(composePath, compose))
	command := RuntimeCommand{Action: "start", Directory: filepath.Join(root, "runtime"), Project: project, ComposeFile: composePath, New: true}
	plan := evalRuntimeIncompletePlan(t, root)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := readRuntimeReceipt(command.Directory); os.IsNotExist(err) {
			return
		}
		if err := RunRuntimeCommand(cleanupCtx, RuntimeCommand{Action: "retire", Directory: command.Directory, RetentionPlan: plan}, io.Discard); err != nil {
			t.Errorf("build-only fixture cleanup: %v", err)
			return
		}
		receipt, err := readRuntimeReceipt(command.Directory)
		if err != nil {
			t.Errorf("build-only cleanup receipt: %v", err)
			return
		}
		removeEvalRuntimeFixture(t, root, receipt)
	})
	startErr := RunRuntimeCommand(ctx, command, io.Discard)
	var dockerExit *exec.ExitError
	if errors.As(startErr, &dockerExit) {
		t.Logf("controlled build-failure fixture diagnostics: %s", dockerExit.Stderr)
	}
	require.Error(t, startErr)
	receipt, err := readRuntimeReceipt(command.Directory)
	require.NoError(t, err)
	require.Equal(t, "preparing", receipt.State)
	require.Empty(t, receipt.Containers, "network creation failure must precede container creation")
	require.Len(t, receipt.Images, 1, "completed build must remain accounted for without a container")
	require.True(t, receipt.Images[0].Owned)
	require.Equal(t, receipt.ID, receipt.Images[0].Owner)
	require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "retire", Directory: command.Directory, RetentionPlan: plan}, io.Discard))
	present, err := runtimePresent(ctx, "image", receipt.Images[0].ID)
	require.NoError(t, err)
	require.False(t, present)
}

func TestEvalRuntimeDockerResumeBeforeFirstStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := evalRuntimeDockerRoot(t)
	dir := filepath.Join(root, "runtime")
	require.NoError(t, os.Mkdir(dir, 0o700))
	compose := filepath.Join(root, "compose.json")
	require.NoError(t, writePairJSON(compose, map[string]any{"services": map[string]any{"server": map[string]any{
		"image": "busybox:1.37.0-musl", "command": []string{"sh", "-ec", "trap 'exit 0' TERM; while :; do sleep 0.1; done"},
	}}}))
	hash, err := PairFileHash(compose)
	require.NoError(t, err)
	receipt := runtimeReceipt{SchemaVersion: runtimeSchema, ID: uuid.NewString(), Directory: dir, Project: "eval627-" + uuid.NewString(), State: "preparing", ComposeFile: compose, ComposeHash: hash, CreatedAt: time.Now().UTC()}
	plan := evalRuntimeIncompletePlan(t, root)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := RunRuntimeCommand(cleanupCtx, RuntimeCommand{Action: "retire", Directory: dir, RetentionPlan: plan}, io.Discard); err != nil {
			t.Errorf("interrupted-start fixture cleanup: %v", err)
			return
		}
		saved, err := readRuntimeReceipt(dir)
		if err != nil {
			t.Errorf("interrupted-start cleanup receipt: %v", err)
			return
		}
		removeEvalRuntimeFixture(t, root, saved)
	})
	require.NoError(t, prepareRuntimeCompose(ctx, &receipt, true))
	require.NoError(t, saveRuntimeReceipt(receipt))
	_, err = runRuntimeCompose(ctx, receipt, true, "create")
	require.NoError(t, err)
	require.NoError(t, captureRuntimeResources(ctx, &receipt, nil, false))
	require.NoError(t, saveRuntimeReceipt(receipt))
	require.Len(t, receipt.Containers, 1)
	require.Equal(t, "created", receipt.Containers[0].Status)
	require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "start", Directory: dir}, io.Discard))
	saved, err := readRuntimeReceipt(dir)
	require.NoError(t, err)
	require.Equal(t, "active", saved.State)
	require.Equal(t, receipt.Containers[0].ID, saved.Containers[0].ID)
	require.True(t, saved.Containers[0].Running)
}
