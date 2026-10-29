package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func holdEvalRuntimeMonitor(t *testing.T, monitor string, receipt runtimeReceipt) func() {
	t.Helper()
	lockDir, err := runtimeLockDirectory()
	require.NoError(t, err)
	args := []string{"-c", `set -eu
for path do
  exec {lock}>"$path"
  flock -n "$lock"
done
printf 'locked\n'
IFS= read -r release`, "runtime-monitor", filepath.Join(monitor, "paired.lock")}
	for _, container := range receipt.Containers {
		args = append(args, filepath.Join(lockDir, container.ID+".lock"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, "bash", args...)
	command.Stderr = os.Stderr
	input, err := command.StdinPipe()
	require.NoError(t, err)
	output, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_, writeErr := io.WriteString(input, "\n")
		closeErr := input.Close()
		waitErr := command.Wait()
		cancel()
		require.NoError(t, errors.Join(writeErr, closeErr, waitErr))
	}
	t.Cleanup(release)
	ready := make([]byte, len("locked\n"))
	_, err = io.ReadFull(output, ready)
	require.NoError(t, err)
	require.Equal(t, "locked\n", string(ready))
	return release
}

func TestEvalRuntimeDockerMissingMonitorRecovery(t *testing.T) {
	for _, variant := range []string{"removed", "renamed"} {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		fixture := newEvalRuntimeFixture(t, ctx, false, "")
		t.Run(variant, func(t *testing.T) {
			receipt, err := readRuntimeReceipt(fixture.command.Directory)
			require.NoError(t, err)
			monitor := filepath.Join(fixture.root, "monitor")
			require.NoError(t, os.Mkdir(monitor, 0o700))
			t.Cleanup(func() { require.NoError(t, os.MkdirAll(monitor, 0o700)) })
			release := holdEvalRuntimeMonitor(t, monitor, receipt)
			pair := PairConfig{OutDir: monitor, Candidate: PairRelease{Runtime: PairRuntime{StateDir: receipt.Directory, Project: receipt.Project}}}
			for _, container := range receipt.Containers {
				switch container.Service {
				case "server":
					pair.Candidate.Runtime.ServerContainer = container.Name
				case "postgres":
					pair.Candidate.Runtime.PostgresContainer = container.Name
				}
			}
			require.NoError(t, CheckPairRuntimeLifecycle(pair, "candidate"))
			receipt, err = readRuntimeReceipt(receipt.Directory)
			require.NoError(t, err)
			require.Equal(t, []string{monitor}, receipt.MonitorDirs)
			protected, err := captureProtectedRuntimes(ctx, []string{receipt.Directory})
			require.NoError(t, err)
			before, err := os.ReadFile(filepath.Join(receipt.Directory, runtimeReceiptName))
			require.NoError(t, err)
			unchanged := func(t *testing.T) {
				t.Helper()
				after, err := os.ReadFile(filepath.Join(receipt.Directory, runtimeReceiptName))
				require.NoError(t, err)
				require.Equal(t, before, after)
				require.NoError(t, verifyProtectedRuntimes(ctx, protected))
				require.NoDirExists(t, monitor)
			}
			if variant == "removed" {
				require.NoError(t, os.Remove(filepath.Join(monitor, "paired.lock")))
				require.NoError(t, os.Remove(monitor))
			} else {
				require.NoError(t, os.Rename(monitor, monitor+"-moved"))
				_, err := runtimeLock(filepath.Join(monitor+"-moved", "paired.lock"))
				require.ErrorContains(t, err, "owned by another process")
			}
			resume := RuntimeCommand{Action: "start", Directory: receipt.Directory}
			retire := RuntimeCommand{Action: "retire", Directory: receipt.Directory, RetentionPlan: fixture.planPath, StopTimeout: 10 * time.Second}
			for _, command := range []RuntimeCommand{resume, retire} {
				require.ErrorContains(t, RunRuntimeCommand(ctx, command, io.Discard), "owned by another process")
				unchanged(t)
			}
			release()
			require.NoError(t, RunRuntimeCommand(ctx, resume, io.Discard))
			unchanged(t)
			archive, err := archiveRuntimeEvidence(fixture.plan)
			require.NoError(t, err)
			for _, failure := range []string{"source_missing", "source_corrupt", "archive_corrupt"} {
				t.Run(failure, func(t *testing.T) {
					path := archive.Files[0].SourcePath
					if failure == "archive_corrupt" {
						path = archive.Files[0].ArchivePath
					}
					original, err := os.ReadFile(path)
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, os.WriteFile(path, original, 0o600)) })
					if failure == "source_missing" {
						require.NoError(t, os.Remove(path))
						require.ErrorIs(t, RunRuntimeCommand(ctx, retire, io.Discard), os.ErrNotExist)
					} else {
						require.NoError(t, os.WriteFile(path, []byte("corrupted"), 0o600))
						require.ErrorContains(t, RunRuntimeCommand(ctx, retire, io.Discard), "hash")
					}
					unchanged(t)
				})
			}
			planHash, err := canonicalJSONHash(fixture.plan)
			require.NoError(t, err)
			// This is the persisted checkpoint immediately before retirement begins resource changes.
			receipt.State = "retiring"
			receipt.Retirement = &runtimeRetirement{PlanHash: planHash, Archive: archive, StartedAt: time.Now().UTC(), Dispositions: map[string]string{}, Helpers: map[string]runtimeContainer{}}
			require.NoError(t, saveRuntimeReceipt(receipt))
			before, err = os.ReadFile(filepath.Join(receipt.Directory, runtimeReceiptName))
			require.NoError(t, err)
			t.Run("archive_missing_after_intent", func(t *testing.T) {
				path := archive.Files[0].ArchivePath
				original, err := os.ReadFile(path)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, os.WriteFile(path, original, 0o600)) })
				require.NoError(t, os.Remove(path))
				require.ErrorIs(t, RunRuntimeCommand(ctx, retire, io.Discard), os.ErrNotExist)
				unchanged(t)
			})
			require.NoError(t, RunRuntimeCommand(ctx, retire, io.Discard))
			require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "verify", Directory: receipt.Directory}, io.Discard))
			retired, err := readRuntimeReceipt(receipt.Directory)
			require.NoError(t, err)
			require.Equal(t, "retired", retired.State)
			require.Equal(t, []string{monitor}, retired.MonitorDirs)
			require.NoDirExists(t, monitor)
			require.NoError(t, verifyRuntimeArchive(retired.Retirement.Archive))
		})
	}
}

type evalRecordedVolumeFixture struct {
	Root, Project, External, ContainerID string
	Mounts                               []runtimeMount
	cleanupFailed                        bool
}

func newEvalRecordedVolumeFixture(t *testing.T, ctx context.Context) *evalRecordedVolumeFixture {
	t.Helper()
	fixture := &evalRecordedVolumeFixture{
		Root: evalRuntimeDockerRoot(t), Project: "eval627-" + uuid.NewString(),
		External: strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", ""),
	}
	t.Cleanup(func() {
		if fixture.cleanupFailed {
			t.Logf("preserving recorded-volume recovery evidence: %+v", fixture)
			return
		}
		receipt := runtimeReceipt{Project: fixture.Project}
		if fixture.ContainerID != "" {
			receipt.Containers = []runtimeContainer{{ID: fixture.ContainerID}}
		}
		removeEvalRuntimeFixture(t, fixture.Root, receipt)
	})
	_, err := runtimeDocker(ctx, "volume", "create", fixture.External)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		present, err := runtimePresent(cleanupCtx, "volume", fixture.External)
		if err == nil && present {
			t.Logf("removing external fixture volume %s", fixture.External)
			_, err = runtimeDocker(cleanupCtx, "volume", "rm", fixture.External)
		}
		if err != nil {
			fixture.cleanupFailed = true
			t.Errorf("external fixture volume %s cleanup: %v", fixture.External, err)
		}
	})
	created, err := runtimeDocker(ctx, "container", "create", "--network", "none", "--label", "com.docker.compose.project="+fixture.Project,
		"--mount", "type=volume,source="+fixture.External+",target=/external", "--volume", fixture.External+":/external-short",
		"--mount", "type=volume,target=/anonymous", "--volume", "/anonymous-short", "busybox:1.37.0-musl", "true")
	require.NoError(t, err)
	fixture.ContainerID = strings.TrimSpace(string(created))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		present, err := runtimePresent(cleanupCtx, "container", fixture.ContainerID)
		if err == nil && present {
			_, err = runtimeDocker(cleanupCtx, "container", "rm", "--volumes", fixture.ContainerID)
		}
		if err != nil {
			fixture.cleanupFailed = true
			t.Errorf("recorded-volume fixture container %s cleanup: %v", fixture.ContainerID, err)
		}
	})
	t.Logf("owned recorded-volume fixture: %+v", fixture)
	return fixture
}

func recordEvalVolumeFixture(t *testing.T, ctx context.Context, fixture *evalRecordedVolumeFixture) (string, string) {
	t.Helper()
	dir := filepath.Join(fixture.Root, "runtime")
	plan := evalRuntimeIncompletePlan(t, fixture.Root)
	enrolled := false
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, err := readRuntimeReceipt(dir)
		if os.IsNotExist(err) && !enrolled {
			return
		}
		if err != nil {
			fixture.cleanupFailed = true
			t.Errorf("recorded-volume cleanup receipt: %v", err)
			return
		}
		if err := RunRuntimeCommand(cleanupCtx, RuntimeCommand{Action: "retire", Directory: dir, RetentionPlan: plan}, io.Discard); err != nil {
			fixture.cleanupFailed = true
			t.Errorf("recorded-volume fixture retirement: %v", err)
		}
	})
	require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "record", Directory: dir, Project: fixture.Project}, io.Discard), "recorded-volume fixture enrollment")
	enrolled = true
	return dir, plan
}

func TestEvalRuntimeDockerRecordedVolumeOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fixture := newEvalRecordedVolumeFixture(t, ctx)
	container, err := inspectRuntimeContainer(ctx, fixture.ContainerID)
	require.NoError(t, err, "recorded-volume fixture inspection")
	fixture.Mounts = container.Mounts
	t.Logf("recorded-volume fixture mounts: %+v", fixture.Mounts)
	dir, plan := recordEvalVolumeFixture(t, ctx, fixture)
	receipt, err := readRuntimeReceipt(dir)
	require.NoError(t, err)
	require.Len(t, receipt.Containers, 1)
	require.Equal(t, container.ID, receipt.Containers[0].ID)
	require.Len(t, receipt.Volumes, 3)
	for _, volume := range receipt.Volumes {
		require.Equal(t, volume.Name != fixture.External, volume.Owned, "explicit external names must never imply anonymous ownership")
	}
	require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "retire", Directory: dir, RetentionPlan: plan}, io.Discard))
	for _, volume := range receipt.Volumes {
		present, err := runtimePresent(ctx, "volume", volume.Name)
		require.NoError(t, err)
		require.Equal(t, volume.Name == fixture.External, present, "retirement preserves external storage and removes proven anonymous allocations")
	}
	retired, err := readRuntimeReceipt(dir)
	require.NoError(t, err)
	require.Equal(t, "retained_external", retired.Retirement.Dispositions["volume:"+fixture.External])
	require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "verify", Directory: dir}, io.Discard))
}

func TestEvalRuntimeDockerRecordedVolumeOwnershipSetupFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if stage := os.Getenv("DENSE_MEM_RECORDED_VOLUME_FAILURE"); stage != "" {
		fixture := newEvalRecordedVolumeFixture(t, ctx)
		container, err := inspectRuntimeContainer(ctx, fixture.ContainerID)
		require.NoError(t, err)
		fixture.Mounts = container.Mounts
		t.Logf("recorded-volume fixture mounts: %+v", fixture.Mounts)
		require.NoError(t, writePairJSON(os.Getenv("DENSE_MEM_RECORDED_VOLUME_IDENTITIES"), fixture))
		cancel()
		switch stage {
		case "inspection":
			_, err := inspectRuntimeContainer(ctx, fixture.ContainerID)
			require.NoError(t, err, "recorded-volume fixture inspection")
		case "enrollment":
			recordEvalVolumeFixture(t, ctx, fixture)
		default:
			t.Fatalf("unknown recorded-volume failure stage %q", stage)
		}
		t.Fatal("cancelled setup unexpectedly succeeded")
	}
	for _, stage := range []string{"inspection", "enrollment"} {
		t.Run(stage, func(t *testing.T) {
			identities := filepath.Join(t.TempDir(), "fixture.json")
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEvalRuntimeDockerRecordedVolumeOwnershipSetupFailure$", "-test.v")
			command.Env = append(os.Environ(), "DENSE_MEM_RECORDED_VOLUME_FAILURE="+stage, "DENSE_MEM_RECORDED_VOLUME_IDENTITIES="+identities)
			output, err := command.CombinedOutput()
			require.Error(t, err, "child must retain its setup failure: %s", output)
			require.NoError(t, ctx.Err(), "%s", output)
			require.Contains(t, string(output), "context canceled")
			require.Contains(t, string(output), "recorded-volume fixture "+stage)
			require.NotContains(t, string(output), "recorded-volume fixture retirement:")
			require.NotContains(t, string(output), "recorded-volume cleanup receipt:")
			data, err := os.ReadFile(identities)
			require.NoError(t, err, "%s", output)
			var fixture evalRecordedVolumeFixture
			require.NoError(t, json.Unmarshal(data, &fixture))
			present, err := runtimePresent(ctx, "container", fixture.ContainerID)
			require.NoError(t, err)
			require.False(t, present, "%s", output)
			volumes := map[string]bool{}
			for _, mount := range fixture.Mounts {
				require.Equal(t, "volume", mount.Type)
				volumes[mount.Name] = mount.Anonymous
				present, err := runtimePresent(ctx, "volume", mount.Name)
				require.NoError(t, err)
				require.False(t, present, "fixture volume %s remains: %s", mount.Name, output)
			}
			require.Len(t, volumes, 3)
			for name, anonymous := range volumes {
				require.Equal(t, name != fixture.External, anonymous)
			}
			require.Contains(t, string(output), "removing external fixture volume "+fixture.External)
			require.NoDirExists(t, fixture.Root, "%s", output)
		})
	}
}

func TestEvalRuntimeDockerRejectionAndReadback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	fixture := newEvalRuntimeFixture(t, ctx, false, "")
	receipt, err := readRuntimeReceipt(fixture.command.Directory)
	require.NoError(t, err)
	protected, err := captureProtectedRuntimes(ctx, []string{receipt.Directory})
	require.NoError(t, err)
	created, err := runtimeDocker(ctx, "container", "create", "--network", "none", "--label", runtimeOwnerLabel+"="+receipt.ID, "busybox:1.37.0-musl", "true")
	require.NoError(t, err)
	terminalID := strings.TrimSpace(string(created))
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		present, err := runtimePresent(cleanupCtx, "container", terminalID)
		if err == nil && present {
			_, err = runtimeDocker(cleanupCtx, "container", "rm", terminalID)
		}
		require.NoError(t, err)
	})
	_, err = runtimeDocker(ctx, "container", "start", "--attach", terminalID)
	require.NoError(t, err)
	lost := receipt
	require.NoError(t, captureRuntimeResources(ctx, &lost, []string{terminalID}, false))
	terminal, err := inspectRuntimeContainer(ctx, terminalID)
	require.NoError(t, err)
	_, err = runtimeDocker(ctx, "container", "rm", terminalID)
	require.NoError(t, err)
	cases := []struct {
		name, reason string
		change       func(*runtimeReceipt)
	}{
		{"missing_container", "disappeared before retirement intent", func(r *runtimeReceipt) { *r = lost }},
		{"reused_container_name", "name now identifies another", func(r *runtimeReceipt) { *r = lost }},
		{"changed_image", "container identity changed", func(r *runtimeReceipt) { r.Containers[0].ImageID = "sha256:" + strings.Repeat("0", 64) }},
		{"unrecorded_container", "unrecorded container", func(r *runtimeReceipt) { r.Containers = r.Containers[1:] }},
		{"bind_outside_runtime", "outside its runtime directory", func(r *runtimeReceipt) { r.Binds[0].Identity.Path = filepath.Dir(r.Directory) }},
		{"bind_replaced", "storage identity changed", func(r *runtimeReceipt) { r.Binds[0].Identity.Inode++ }},
		{"volume_missing", "volume disappeared before retirement intent", func(r *runtimeReceipt) { r.Volumes[0].Name = "eval627-missing-" + uuid.NewString() }},
		{"volume_replaced", "volume identity changed", func(r *runtimeReceipt) { r.Volumes[0].CreatedAt += "-changed" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "reused_container_name" {
				created, err := runtimeDocker(ctx, "container", "create", "--network", "none", "--name", terminal.Name, "--label", "com.docker.compose.project=eval627-other-"+uuid.NewString(), "busybox:1.37.0-musl", "true")
				require.NoError(t, err)
				t.Cleanup(func() {
					cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
					defer stop()
					_, err := runtimeDocker(cleanupCtx, "container", "rm", strings.TrimSpace(string(created)))
					require.NoError(t, err)
				})
			}
			candidate := receipt
			candidate.Containers = slices.Clone(receipt.Containers)
			candidate.Binds = slices.Clone(receipt.Binds)
			candidate.Volumes = slices.Clone(receipt.Volumes)
			test.change(&candidate)
			require.ErrorContains(t, verifyRetirementOwnership(ctx, candidate), test.reason)
			require.NoError(t, verifyProtectedRuntimes(ctx, protected))
		})
	}
	before, err := os.ReadFile(filepath.Join(receipt.Directory, runtimeReceiptName))
	require.NoError(t, err)
	for _, id := range []string{receipt.Containers[0].ID, strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")} {
		require.Error(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "record", Directory: receipt.Directory, Fixtures: []string{id}}, io.Discard))
	}
	for _, action := range []string{"start", "record", "retire", "verify"} {
		t.Run("cancelled_"+action, func(t *testing.T) {
			cancelled, stop := context.WithCancel(ctx)
			stop()
			err := RunRuntimeCommand(cancelled, RuntimeCommand{Action: action, Directory: receipt.Directory, RetentionPlan: fixture.planPath}, io.Discard)
			require.ErrorIs(t, err, context.Canceled)
			require.NoError(t, verifyProtectedRuntimes(ctx, protected))
		})
	}
	after, err := os.ReadFile(filepath.Join(receipt.Directory, runtimeReceiptName))
	require.NoError(t, err)
	require.Equal(t, before, after, "failed enrollment and cancellation must preserve the ownership receipt")
	missing := "eval627-missing-" + uuid.NewString()
	_, err = inspectRuntimeContainer(ctx, missing)
	require.Error(t, err)
	_, err = inspectRuntimeNetwork(ctx, missing, receipt.Project)
	require.Error(t, err)
	_, err = inspectRuntimeVolume(ctx, missing, receipt.Project)
	require.Error(t, err)
	_, err = inspectRuntimeImage(ctx, missing)
	require.Error(t, err)
	command := fixture.command
	command.Directory = filepath.Join(t.TempDir(), "runtime")
	require.ErrorContains(t, RunRuntimeCommand(ctx, command, io.Discard), "project already owns resources")
	require.NoError(t, verifyProtectedRuntimes(ctx, protected))
	retire := RuntimeCommand{Action: "retire", Directory: receipt.Directory, RetentionPlan: fixture.planPath, StopTimeout: 10 * time.Second}
	require.NoError(t, RunRuntimeCommand(ctx, retire, io.Discard))
	retired, err := readRuntimeReceipt(receipt.Directory)
	require.NoError(t, err)
	cancelled, stop := context.WithCancel(ctx)
	stop()
	require.ErrorIs(t, RunRuntimeCommand(cancelled, RuntimeCommand{Action: "verify", Directory: receipt.Directory}, io.Discard), context.Canceled, "Docker errors cannot prove resource absence")
	copy := retired
	copy.Retirement = nil
	require.ErrorContains(t, verifyRetirementReadback(ctx, copy), "intent is missing")
	progress := *retired.Retirement
	copy.Retirement = &progress
	progress.Dispositions = map[string]string{}
	require.ErrorContains(t, verifyRetirementReadback(ctx, copy), "unfinished resource")
	progress.Dispositions = retired.Retirement.Dispositions
	progress.Helpers = map[string]runtimeContainer{"unfinished": {Name: "unresolved"}}
	require.ErrorContains(t, verifyRetirementReadback(ctx, copy), "has not been reconciled")
	require.ErrorContains(t, removeRuntimeHelper(ctx, &retired, runtimeContainer{}), "identity is unresolved")
	bind := retired.Binds[0]
	require.NoError(t, os.Mkdir(bind.Identity.Path, 0o700))
	require.ErrorContains(t, verifyRetiredRuntime(ctx, retired), "bind storage is not proven absent")
	require.NoError(t, os.Remove(bind.Identity.Path))
	require.NoError(t, retireRuntimeBind(ctx, retire, &retired, bind), "cleanup resumes when the disposable directory is already absent")
	require.NoError(t, verifyRetiredRuntime(ctx, retired))
}

func TestEvalRuntimeDockerStoragePreflight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, variant := range []string{"nonempty_bind", "outside_bind", "symlink_bind", "file_bind", "invalid_compose", "empty_enrollment"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "runtime")
			project := "eval627-" + uuid.NewString()
			t.Cleanup(func() { removeEvalRuntimeFixture(t, root, runtimeReceipt{Project: project}) })
			require.NoError(t, os.Mkdir(dir, 0o700))
			source := filepath.Join(dir, "data")
			reason := "new runtime storage is not empty"
			if variant == "outside_bind" || variant == "symlink_bind" {
				source, reason = filepath.Join(root, "outside"), "storage must be inside"
			}
			payload := source
			if variant != "file_bind" {
				require.NoError(t, os.Mkdir(source, 0o700))
				payload = filepath.Join(source, "keep")
			} else {
				reason = "not a directory"
			}
			require.NoError(t, os.WriteFile(payload, []byte("preserve pre-existing data\n"), 0o600))
			hash, err := PairFileHash(payload)
			require.NoError(t, err)
			if variant == "symlink_bind" {
				link := filepath.Join(dir, "data")
				require.NoError(t, os.Symlink(source, link))
				source, reason = link, "symlink"
			}
			compose := filepath.Join(root, "compose.json")
			require.NoError(t, writePairJSON(compose, map[string]any{"services": map[string]any{"server": map[string]any{
				"image": "busybox:1.37.0-musl", "command": []string{"true"}, "volumes": []string{source + ":/data"},
			}}}))
			command := RuntimeCommand{Action: "start", Directory: dir, Project: project, ComposeFile: compose, New: true}
			if variant == "invalid_compose" {
				require.NoError(t, os.WriteFile(compose, []byte("{"), 0o600))
				reason = "Compose config failed"
			}
			if variant == "empty_enrollment" {
				command.Action, command.New, reason = "record", false, "no prepared containers"
			}
			require.ErrorContains(t, RunRuntimeCommand(ctx, command, io.Discard), reason)
			require.NoError(t, runtimeVerifyFile(payload, hash))
			require.NoFileExists(t, filepath.Join(dir, runtimeReceiptName))
			require.NoError(t, assertUnusedRuntimeProject(ctx, project), "rejected preflight must not create Docker resources")
		})
	}
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
