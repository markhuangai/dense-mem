package evalretire

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type retirementDockerFixture struct {
	command                        RuntimeCommand
	receipt                        runtimeReceipt
	protected                      runtimeContainer
	data, shared, external, volume string
	sharedVolume, evidence         string
}

func newRetirementDockerFixture(t *testing.T, stubborn bool) retirementDockerFixture {
	t.Helper()
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("explicit disposable Docker regression execution required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const image = "busybox:1.37.0-musl"
	if _, err := runtimeDocker(ctx, "image", "inspect", image); err != nil {
		_, err = runtimeDocker(ctx, "image", "pull", image)
		require.NoError(t, err)
	}
	root, project := t.TempDir(), "eval763-"+uuid.NewString()
	protectedProject := "eval763-" + uuid.NewString()
	containerIDs := []string{}
	t.Cleanup(func() {
		directory, err := runtimeLockDirectory()
		require.NoError(t, err)
		for _, id := range append([]string{"project-" + project, "project-" + protectedProject}, containerIDs...) {
			path := filepath.Join(directory, id+".lock")
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				continue
			}
			lock, err := runtimeLock(path)
			require.NoError(t, err)
			require.NoError(t, os.Remove(path))
			require.NoError(t, lock.Close())
		}
	})
	f := retirementDockerFixture{command: RuntimeCommand{Action: "record", Project: project, Directory: filepath.Join(root, "runtime"), StopTimeout: 2 * time.Second}}
	f.data, f.shared = filepath.Join(f.command.Directory, "data"), filepath.Join(f.command.Directory, "shared")
	for _, path := range []string{f.data, f.shared} {
		require.NoError(t, os.MkdirAll(path, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(path, "evidence"), []byte("exact retained fixture evidence"), 0o600))
	}
	f.evidence = filepath.Join(root, "frozen.json")
	require.NoError(t, os.WriteFile(f.evidence, []byte("exact frozen fixture judgments"), 0o600))
	f.command.RetentionPlan = evalRuntimeIncompletePlan(t, root)
	var plan runtimeRetentionPlan
	require.NoError(t, readRuntimeJSON(f.command.RetentionPlan, &plan))
	hash, err := runtimeFileHash(f.evidence)
	require.NoError(t, err)
	plan.Evidence[0] = runtimeEvidence{Kind: "frozen_inputs", Files: []runtimeArtifact{{Path: f.evidence, SHA256: hash}}}
	plan.ArchiveDir = filepath.Join(root, "archive")
	createVolume := func(owned bool) string {
		name := "eval763-" + uuid.NewString()
		args := []string{"volume", "create", "--label", "io.dense-mem.eval.fixture=" + project}
		if owned {
			args = append(args, "--label", "com.docker.compose.project="+project)
		}
		_, err := runtimeDocker(ctx, append(args, name)...)
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			present, err := runtimePresent(cleanup, "volume", name)
			require.NoError(t, err)
			if !present {
				return
			}
			label, err := runtimeDocker(cleanup, "volume", "inspect", "--format", `{{index .Labels "io.dense-mem.eval.fixture"}}`, name)
			require.NoError(t, err)
			require.Equal(t, project, strings.TrimSpace(string(label)))
			_, err = runtimeDocker(cleanup, "volume", "rm", name)
			require.NoError(t, err)
		})
		return name
	}
	f.volume, f.sharedVolume, f.external = createVolume(true), createVolume(true), createVolume(false)
	networkBytes, err := runtimeDocker(ctx, "network", "create", "--label", "com.docker.compose.project="+project, project)
	require.NoError(t, err)
	network := strings.TrimSpace(string(networkBytes))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		present, err := runtimePresent(cleanup, "network", network)
		require.NoError(t, err)
		if present {
			actual, err := inspectRuntimeNetwork(cleanup, network, project)
			require.NoError(t, err)
			require.Equal(t, project, actual.Project)
			_, err = runtimeDocker(cleanup, "network", "rm", network)
			require.NoError(t, err)
		}
	})
	createContainer := func(owner string, args ...string) runtimeContainer {
		args = append([]string{"container", "create", "--label", "com.docker.compose.project=" + owner, "--label", "com.docker.compose.service=server"}, args...)
		script := "trap 'exit 0' TERM INT; while :; do sleep 1 & wait $!; done"
		if stubborn && owner == project {
			script = "trap '' TERM; trap 'exit 0' USR1; while :; do sleep 1 & wait $!; done"
		}
		args = append(args, image, "sh", "-c", script)
		created, err := runtimeDocker(ctx, args...)
		require.NoError(t, err)
		id := strings.TrimSpace(string(created))
		containerIDs = append(containerIDs, id)
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			present, err := runtimePresent(cleanup, "container", id)
			require.NoError(t, err)
			if present {
				actual, err := inspectRuntimeContainer(cleanup, id)
				require.NoError(t, err)
				require.Equal(t, owner, actual.Project)
				_, err = runtimeDocker(cleanup, "container", "rm", "--force", "--volumes", id)
				require.NoError(t, err)
			}
		})
		_, err = runtimeDocker(ctx, "container", "start", id)
		require.NoError(t, err)
		actual, err := inspectRuntimeContainer(ctx, id)
		require.NoError(t, err)
		return actual
	}
	createContainer(project, "--network", network,
		"--mount", "type=bind,source="+f.data+",target=/data",
		"--mount", "type=bind,source="+f.shared+",target=/shared",
		"--mount", "type=volume,source="+f.volume+",target=/owned",
		"--mount", "type=volume,source="+f.sharedVolume+",target=/shared-volume",
		"--mount", "type=volume,source="+f.external+",target=/external",
		"--mount", "type=volume,target=/anonymous")
	f.protected = createContainer(protectedProject, "--network", "none", "--mount", "type=bind,source="+f.shared+",target=/shared,readonly", "--mount", "type=volume,source="+f.sharedVolume+",target=/shared-volume")
	protectedDir := filepath.Join(root, "protected")
	require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "record", Project: protectedProject, Directory: protectedDir}, io.Discard))
	plan.ProtectedRuntimes = []string{protectedDir}
	require.NoError(t, writeRuntimeJSON(f.command.RetentionPlan, plan))
	require.NoError(t, RunRuntimeCommand(ctx, f.command, io.Discard))
	f.receipt, err = readRuntimeReceipt(f.command.Directory)
	require.NoError(t, err)
	t.Cleanup(func() {
		receipt, err := readRuntimeReceipt(f.command.Directory)
		require.NoError(t, err)
		if receipt.Retirement == nil {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		for _, helper := range receipt.Retirement.Helpers {
			if helper.ID == "" {
				continue
			}
			present, err := runtimePresent(cleanup, "container", helper.ID)
			require.NoError(t, err)
			if present {
				actual, err := inspectRuntimeContainer(cleanup, helper.ID)
				require.NoError(t, err)
				require.Equal(t, receipt.ID, actual.Owner)
				require.Equal(t, helper.Name, actual.Name)
				_, err = runtimeDocker(cleanup, "container", "rm", "--force", helper.ID)
				require.NoError(t, err)
			}
		}
	})
	f.command.Action = "retire"
	return f
}

func TestEvaluationRetirementPreservesSharedResourcesAndArchives(t *testing.T) {
	f := newRetirementDockerFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	require.NoError(t, verifyActiveRuntime(ctx, f.receipt))
	require.NoError(t, RunRuntimeCommand(ctx, f.command, io.Discard))
	require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "verify", Directory: f.command.Directory}, io.Discard))
	require.NoError(t, RunRuntimeCommand(ctx, f.command, io.Discard), "verified retirement is idempotent")
	retired, err := readRuntimeReceipt(f.command.Directory)
	require.NoError(t, err)
	require.Equal(t, "retired", retired.State)
	require.NotNil(t, retired.Retirement.VerifiedAt)
	require.NoError(t, verifyRuntimeArchive(retired.Retirement.Archive))
	require.NoDirExists(t, f.data)
	data, err := os.ReadFile(filepath.Join(f.shared, "evidence"))
	require.NoError(t, err)
	require.Equal(t, "exact retained fixture evidence", string(data))
	for name, expected := range map[string]string{f.external: "retained_external", f.sharedVolume: "retained_shared", f.volume: "removed"} {
		require.Equal(t, expected, retired.Retirement.Dispositions["volume:"+name])
		present, err := runtimePresent(ctx, "volume", name)
		require.NoError(t, err)
		require.Equal(t, expected != "removed", present)
	}
	require.Equal(t, "retained_shared", retired.Retirement.Dispositions["bind:"+f.shared])
	actual, err := inspectRuntimeContainer(ctx, f.protected.ID)
	require.NoError(t, err)
	require.Equal(t, f.protected, actual)
	for _, file := range retired.Retirement.Archive.Files {
		data, err := os.ReadFile(file.ArchivePath)
		require.NoError(t, err)
		require.Equal(t, "exact frozen fixture judgments", string(data))
	}
}

func TestEvaluationRetirementRejectsChangedOwnership(t *testing.T) {
	f := newRetirementDockerFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, variant := range []string{"container", "volume", "bind", "unrecorded", "retention", "protected"} {
		t.Run(variant, func(t *testing.T) {
			receipt, err := readRuntimeReceipt(f.command.Directory)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, saveRuntimeReceipt(f.receipt))
				require.NoError(t, os.WriteFile(f.evidence, []byte("exact frozen fixture judgments"), 0o600))
			})
			reason := "identity changed"
			switch variant {
			case "container":
				receipt.Containers[0].Created = "changed"
			case "volume":
				receipt.Volumes[0].CreatedAt = "changed"
			case "bind":
				receipt.Binds[0].Identity.Inode++
			case "unrecorded":
				created, err := runtimeDocker(ctx, "container", "create", "--label", "com.docker.compose.project="+receipt.Project, "--name", "eval763-unrecorded-"+receipt.ID, receipt.Containers[0].ImageID, "true")
				require.NoError(t, err)
				id := strings.TrimSpace(string(created))
				t.Cleanup(func() {
					cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
					defer stop()
					actual, err := inspectRuntimeContainer(cleanup, id)
					require.NoError(t, err)
					require.Equal(t, receipt.Project, actual.Project)
					_, err = runtimeDocker(cleanup, "container", "rm", id)
					require.NoError(t, err)
				})
				reason = "unrecorded container"
			case "retention":
				require.NoError(t, os.WriteFile(f.evidence, []byte("changed after retention approval"), 0o600))
				reason = "hash"
			case "protected":
				_, err := runtimeDocker(ctx, "container", "stop", f.protected.ID)
				require.NoError(t, err)
				t.Cleanup(func() {
					cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
					defer stop()
					_, err := runtimeDocker(cleanup, "container", "start", f.protected.ID)
					require.NoError(t, err)
				})
				reason = "protected runtime is not healthy"
			}
			require.NoError(t, saveRuntimeReceipt(receipt))
			require.ErrorContains(t, RunRuntimeCommand(ctx, f.command, io.Discard), reason)
			actual, err := inspectRuntimeContainer(ctx, f.receipt.Containers[0].ID)
			require.NoError(t, err)
			require.True(t, actual.Running, "guard rejection must occur before stopping owned work")
			require.FileExists(t, filepath.Join(f.data, "evidence"))
		})
	}
}

func TestEvaluationRetirementPreservesInterruptedStopAndVerifiesReadback(t *testing.T) {
	f := newRetirementDockerFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f.command.StopTimeout = 100 * time.Millisecond
	require.ErrorContains(t, RunRuntimeCommand(ctx, f.command, io.Discard), "graceful container stop incomplete")
	actual, err := inspectRuntimeContainer(ctx, f.receipt.Containers[0].ID)
	require.NoError(t, err)
	require.True(t, actual.Running)
	require.FileExists(t, filepath.Join(f.data, "evidence"))
	_, err = runtimeDocker(ctx, "container", "kill", "--signal=USR1", actual.ID)
	require.NoError(t, err)
	code, err := runtimeDocker(ctx, "container", "wait", actual.ID)
	require.NoError(t, err)
	require.Equal(t, "0", strings.TrimSpace(string(code)))
	f.command.StopTimeout = 2 * time.Second
	require.NoError(t, RunRuntimeCommand(ctx, f.command, io.Discard))
	retired, err := readRuntimeReceipt(f.command.Directory)
	require.NoError(t, err)
	key := "container:" + actual.ID
	retired.Retirement.Dispositions[key] = ""
	require.ErrorContains(t, verifyRetiredRuntime(ctx, retired), "unfinished resource")
	retired.Retirement.Dispositions[key] = "removed"
	require.NoError(t, os.Mkdir(f.data, 0o700))
	require.ErrorContains(t, verifyRetiredRuntime(ctx, retired), "not proven absent")
	require.NoError(t, os.Remove(f.data))
	require.NoError(t, os.WriteFile(retired.Retirement.Archive.Files[0].ArchivePath, []byte("corrupted archive"), 0o600))
	require.ErrorContains(t, verifyRetiredRuntime(ctx, retired), "hash")
}
