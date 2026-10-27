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

type evalRuntimeFixture struct {
	command       RuntimeCommand
	plan          runtimeRetentionPlan
	planPath      string
	root          string
	cleanupFailed bool
	dependents    []*evalRuntimeFixture
}

func evalRuntimeDockerRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", t.Name()+"-")
	require.NoError(t, err)
	t.Logf("owned Docker fixture directory: %s", root)
	return root
}

func removeEvalRuntimeFixture(t *testing.T, root string, receipt runtimeReceipt) {
	t.Helper()
	lockDir, err := runtimeLockDirectory()
	if err != nil {
		t.Errorf("fixture lock cleanup: %v", err)
		return
	}
	paths := []string{filepath.Join(lockDir, "project-"+receipt.Project+".lock")}
	for _, container := range receipt.Containers {
		paths = append(paths, filepath.Join(lockDir, container.ID+".lock"))
	}
	for _, path := range paths {
		lock, err := runtimeLock(path)
		if err != nil {
			t.Errorf("fixture lock remains owned: %v", err)
			return
		}
		err = os.Remove(path)
		lock.Close()
		if err != nil {
			t.Errorf("fixture lock removal: %v", err)
			return
		}
	}
	if err := os.RemoveAll(root); err != nil {
		t.Errorf("retired fixture directory cleanup: %v", err)
	}
}

func newEvalRuntimeFixture(t *testing.T, ctx context.Context, stubborn bool, supersedes string) *evalRuntimeFixture {
	t.Helper()
	root := evalRuntimeDockerRoot(t)
	project := "eval627-" + uuid.NewString()
	base := "busybox:1.37.0-musl"
	if _, err := runtimeDocker(ctx, "image", "inspect", base); err != nil {
		_, err := runtimeDocker(ctx, "image", "pull", base)
		require.NoError(t, err, "real Docker lifecycle regression requires its fixture image")
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM "+base+"\nARG FIXTURE_ROLE=server\nLABEL io.dense-mem.eval.fixture="+project+" role=$FIXTURE_ROLE\n"), 0o600))
	loop := "touch /ready; trap 'exit 0' TERM; while :; do sleep 0.1; done"
	database := "mkdir -p /data/restricted; echo durable > /data/restricted/value; chmod 000 /data/restricted; " + loop
	if stubborn {
		database = "mkdir -p /data/restricted; echo durable > /data/restricted/value; chmod 000 /data/restricted; touch /ready; trap 'while ! test -f /allow; do sleep 0.1; done; exit 0' TERM; while :; do sleep 0.1; done"
	}
	health := map[string]any{"test": []string{"CMD", "test", "-f", "/ready"}, "interval": "1s", "timeout": "1s", "retries": 10}
	compose := map[string]any{
		"services": map[string]any{
			"server":   map[string]any{"image": project + ":server", "build": map[string]any{"context": root}, "command": []string{"sh", "-ec", loop}, "volumes": []string{"/scratch"}, "healthcheck": health},
			"postgres": map[string]any{"image": base, "command": []string{"sh", "-ec", database}, "volumes": []string{"${V1_COMPOSE_DATA_DIR}/postgres:/data"}, "healthcheck": health},
			"redis":    map[string]any{"image": base, "command": []string{"sh", "-ec", loop}, "volumes": []string{"state:/cache"}, "healthcheck": health},
			"worker":   map[string]any{"image": project + ":worker", "build": map[string]any{"context": root, "args": map[string]string{"FIXTURE_ROLE": "worker"}}, "command": []string{"sh", "-ec", loop}, "healthcheck": health},
		},
		"volumes": map[string]any{"state": map[string]any{}},
	}
	composePath := filepath.Join(root, "compose.json")
	require.NoError(t, writePairJSON(composePath, compose))
	fixture := &evalRuntimeFixture{root: root, command: RuntimeCommand{Action: "start", Directory: filepath.Join(root, "runtime"), Project: project, ComposeFile: composePath, New: supersedes == "", Supersedes: supersedes, StopTimeout: 5 * time.Second}}
	fixture.plan = runtimeRetentionPlan{SchemaVersion: runtimeSchema, ArchiveDir: filepath.Join(root, "archive"), Outcome: "incomplete"}
	for _, kind := range runtimeEvidenceKinds {
		path := filepath.Join(root, kind+".json")
		require.NoError(t, os.WriteFile(path, []byte("retained fixture "+kind+"\n"), 0o600))
		hash, err := PairFileHash(path)
		require.NoError(t, err)
		fixture.plan.Evidence = append(fixture.plan.Evidence, runtimeEvidence{Kind: kind, Files: []runtimeArtifact{{Path: path, SHA256: hash}}})
	}
	fixture.planPath = filepath.Join(root, "retention-plan.json")
	require.NoError(t, writePairJSON(fixture.planPath, fixture.plan))
	t.Cleanup(func() {
		for _, dependent := range fixture.dependents {
			if dependent.cleanupFailed {
				t.Logf("preserving protected fixture after dependent cleanup failure: %s", root)
				return
			}
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		receipt, err := readRuntimeReceipt(fixture.command.Directory)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Errorf("cannot read fixture cleanup receipt: %v", err)
			fixture.cleanupFailed = true
			return
		}
		for _, container := range receipt.Containers {
			actual, err := inspectRuntimeContainer(cleanupCtx, container.ID)
			if err == nil && actual.Running && actual.Service == "postgres" {
				if _, err := runtimeDocker(cleanupCtx, "container", "exec", container.ID, "touch", "/allow"); err != nil {
					t.Errorf("release fixture shutdown: %v", err)
				}
			}
		}
		command := RuntimeCommand{Action: "retire", Directory: fixture.command.Directory, RetentionPlan: fixture.planPath, StopTimeout: 10 * time.Second}
		if err := RunRuntimeCommand(cleanupCtx, command, io.Discard); err != nil {
			t.Errorf("fixture retirement incomplete: %v", err)
			fixture.cleanupFailed = true
			return
		}
		removeEvalRuntimeFixture(t, root, receipt)
	})
	startErr := RunRuntimeCommand(ctx, fixture.command, io.Discard)
	var dockerExit *exec.ExitError
	if errors.As(startErr, &dockerExit) {
		t.Logf("controlled fixture Compose diagnostics: %s", dockerExit.Stderr)
	}
	require.NoError(t, startErr)
	receipt, err := readRuntimeReceipt(fixture.command.Directory)
	require.NoError(t, err)
	require.Equal(t, "active", receipt.State)
	require.Eventually(t, func() bool {
		for _, container := range receipt.Containers {
			actual, err := inspectRuntimeContainer(ctx, container.ID)
			if err != nil || !actual.Running || actual.Health != "healthy" {
				return false
			}
		}
		return true
	}, 20*time.Second, 100*time.Millisecond)
	return fixture
}

func TestEvalRuntimeDockerLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	_, err := runtimeDocker(ctx, "version", "--format", "{{.Server.Version}}")
	require.NoError(t, err, "this regression requires real Docker; it must not skip")
	active := newEvalRuntimeFixture(t, ctx, false, "")
	old := newEvalRuntimeFixture(t, ctx, true, "")
	active.dependents = append(active.dependents, old)
	old.plan.ProtectedRuntimes = []string{active.command.Directory}
	require.NoError(t, writePairJSON(old.planPath, old.plan))
	protected, err := captureProtectedRuntimes(ctx, old.plan.ProtectedRuntimes)
	require.NoError(t, err)
	var activeServer runtimeContainer
	for _, container := range protected {
		if container.Service == "server" {
			activeServer = container
		}
	}
	require.NotEmpty(t, activeServer.ID)
	require.NoError(t, stopRuntimeContainer(ctx, activeServer, 10*time.Second))
	require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "start", Directory: active.command.Directory}, io.Discard))
	require.Eventually(t, func() bool {
		current, err := inspectRuntimeContainer(ctx, activeServer.ID)
		return err == nil && current.Running && current.Health == "healthy"
	}, 20*time.Second, 100*time.Millisecond)
	protected, err = captureProtectedRuntimes(ctx, old.plan.ProtectedRuntimes)
	require.NoError(t, err)
	t.Run("unhealthy_protected_runtime_blocks_retirement", func(t *testing.T) {
		t.Cleanup(func() {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			_, err := runtimeDocker(cleanupCtx, "container", "exec", activeServer.ID, "touch", "/ready")
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				current, err := inspectRuntimeContainer(cleanupCtx, activeServer.ID)
				return err == nil && current.Health == "healthy"
			}, 15*time.Second, 100*time.Millisecond)
		})
		_, err := runtimeDocker(ctx, "container", "exec", activeServer.ID, "rm", "/ready")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			current, err := inspectRuntimeContainer(ctx, activeServer.ID)
			return err == nil && current.Health == "unhealthy"
		}, 20*time.Second, 100*time.Millisecond)
		require.ErrorContains(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "retire", Directory: old.command.Directory, RetentionPlan: old.planPath, StopTimeout: time.Second}, io.Discard), "protected runtime is not healthy")
		require.ErrorContains(t, verifyProtectedRuntimes(ctx, protected), "health changed")
		current, err := readRuntimeReceipt(old.command.Directory)
		require.NoError(t, err)
		require.Equal(t, "active", current.State)
		require.Nil(t, current.Retirement)
		require.NoError(t, verifyActiveRuntime(ctx, current))
	})
	require.NoError(t, verifyProtectedRuntimes(ctx, protected))
	receipt, err := readRuntimeReceipt(old.command.Directory)
	require.NoError(t, err)
	monitorDir := filepath.Join(old.root, "monitor")
	require.NoError(t, os.Mkdir(monitorDir, 0o700))
	pair := PairConfig{OutDir: monitorDir, Candidate: PairRelease{Runtime: PairRuntime{StateDir: old.command.Directory, Project: receipt.Project}}}
	for _, container := range receipt.Containers {
		if container.Service == "server" {
			pair.Candidate.Runtime.ServerContainer = container.Name
		}
		if container.Service == "postgres" {
			pair.Candidate.Runtime.PostgresContainer = container.Name
		}
	}
	require.NoError(t, CheckPairRuntimeLifecycle(pair, "candidate"))
	require.NoError(t, CheckPairRuntimeLifecycle(pair, "candidate"), "registration is idempotent")
	receipt, err = readRuntimeReceipt(old.command.Directory)
	require.NoError(t, err)
	require.Equal(t, []string{monitorDir}, receipt.MonitorDirs)
	before := append([]runtimeContainer(nil), receipt.Containers...)
	resume := old.command
	resume.New = false
	require.NoError(t, RunRuntimeCommand(ctx, resume, io.Discard))
	for _, container := range before {
		current, err := inspectRuntimeContainer(ctx, container.ID)
		require.NoError(t, err)
		require.True(t, sameRuntimeContainer(container, current))
		require.Equal(t, container.StartedAt, current.StartedAt)
	}
	blocked := RuntimeCommand{Action: "start", Directory: filepath.Join(t.TempDir(), "replacement"), Project: "eval627-" + uuid.NewString(), Supersedes: old.command.Directory}
	require.ErrorContains(t, RunRuntimeCommand(ctx, blocked, io.Discard), "retirement is incomplete")

	fixture, err := runtimeDocker(ctx, "container", "create", "--label", runtimeOwnerLabel+"="+receipt.ID, "--mount", "type=volume,destination=/fixture", "busybox:1.37.0-musl", "sh", "-ec", "echo terminal > /fixture/value")
	require.NoError(t, err)
	fixtureID := strings.TrimSpace(string(fixture))
	_, err = runtimeDocker(ctx, "container", "start", "--attach", fixtureID)
	require.NoError(t, err)
	record := RuntimeCommand{Action: "record", Directory: old.command.Directory, Fixtures: []string{fixtureID}}
	require.NoError(t, RunRuntimeCommand(ctx, record, io.Discard))
	require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "record", Directory: old.command.Directory}, io.Discard), "re-enrollment retains explicit terminal fixtures")
	terminalBefore, err := inspectRuntimeContainer(ctx, fixtureID)
	require.NoError(t, err)
	require.NoError(t, RunRuntimeCommand(ctx, resume, io.Discard))
	terminalAfter, err := inspectRuntimeContainer(ctx, fixtureID)
	require.NoError(t, err)
	require.Equal(t, terminalBefore.StartedAt, terminalAfter.StartedAt, "resumption never restarts a terminal fixture")
	receipt, err = readRuntimeReceipt(old.command.Directory)
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(receipt.Containers, func(c runtimeContainer) bool { return c.ID == fixtureID && c.Fixture }))
	require.GreaterOrEqual(t, len(receipt.Volumes), 3, "named, anonymous server and anonymous fixture storage")
	var sharedVolume, sharedImage string
	for _, volume := range receipt.Volumes {
		if volume.Project == receipt.Project {
			sharedVolume = volume.Name
		}
	}
	for _, container := range receipt.Containers {
		if container.Service == "server" {
			sharedImage = container.ImageID
		}
	}
	shared, err := runtimeDocker(ctx, "container", "create", "--label", runtimeOwnerLabel+"="+uuid.NewString(), "--label", "com.docker.compose.project=eval627-sharing-"+uuid.NewString(), "--mount", "type=volume,source="+sharedVolume+",destination=/shared", sharedImage, "sh", "-ec", "exit 0")
	require.NoError(t, err)
	sharedID := strings.TrimSpace(string(shared))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		present, err := runtimePresent(cleanupCtx, "container", sharedID)
		if err != nil {
			t.Errorf("shared fixture readback: %v", err)
			return
		}
		if present {
			_, err = runtimeDocker(cleanupCtx, "container", "rm", sharedID)
			if err != nil {
				t.Errorf("shared fixture cleanup: %v", err)
			}
		}
	})

	retire := RuntimeCommand{Action: "retire", Directory: old.command.Directory, RetentionPlan: old.planPath, StopTimeout: time.Second}
	monitorLock, err := runtimeLock(filepath.Join(monitorDir, "paired.lock"))
	require.NoError(t, err)
	require.ErrorContains(t, RunRuntimeCommand(ctx, retire, io.Discard), "owned by another process")
	require.NoError(t, monitorLock.Close())
	planWithDependency := old.plan
	planWithDependency.PendingRuntimeDependencies = []string{"report still needs the old database"}
	require.NoError(t, writePairJSON(old.planPath, planWithDependency))
	require.ErrorContains(t, RunRuntimeCommand(ctx, retire, io.Discard), "dependencies")
	require.NoError(t, writePairJSON(old.planPath, old.plan))
	require.NoError(t, verifyActiveRuntime(ctx, receipt))

	archive, err := archiveRuntimeEvidence(old.plan)
	require.NoError(t, err)
	artifact := archive.Files[0]
	original, err := os.ReadFile(artifact.ArchivePath)
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := os.Stat(artifact.ArchivePath); err == nil {
			if err := os.WriteFile(artifact.ArchivePath, original, 0o600); err != nil {
				t.Errorf("restore deliberately corrupted fixture archive: %v", err)
			}
		}
	})
	require.NoError(t, os.WriteFile(artifact.ArchivePath, []byte("corrupted"), 0o600))
	require.ErrorContains(t, RunRuntimeCommand(ctx, retire, io.Discard), "hash")
	require.NoError(t, os.WriteFile(artifact.ArchivePath, original, 0o600))
	require.NoError(t, verifyActiveRuntime(ctx, receipt))

	lockDir, err := runtimeLockDirectory()
	require.NoError(t, err)
	lock, err := runtimeLock(filepath.Join(lockDir, receipt.Containers[0].ID+".lock"))
	require.NoError(t, err)
	require.ErrorContains(t, RunRuntimeCommand(ctx, retire, io.Discard), "owned by another process")
	require.NoError(t, lock.Close())
	modified := receipt
	modified.Containers = slices.Clone(receipt.Containers)
	modified.Containers[0].ImageID = "sha256:" + strings.Repeat("0", 64)
	require.NoError(t, saveRuntimeReceipt(modified))
	require.ErrorContains(t, RunRuntimeCommand(ctx, retire, io.Discard), "identity changed")
	require.NoError(t, saveRuntimeReceipt(receipt))

	err = RunRuntimeCommand(ctx, retire, io.Discard)
	require.ErrorContains(t, err, "graceful container stop incomplete")
	interrupted, err := readRuntimeReceipt(old.command.Directory)
	require.NoError(t, err)
	require.Equal(t, "retiring", interrupted.State)
	require.ErrorContains(t, CheckPairRuntimeLifecycle(pair, "candidate"), "not active")
	require.Nil(t, interrupted.Retirement.VerifiedAt)
	require.NoError(t, verifyRuntimeArchive(interrupted.Retirement.Archive))
	require.ErrorContains(t, RunRuntimeCommand(ctx, blocked, io.Discard), "retirement is incomplete")
	var databaseID string
	for _, container := range receipt.Containers {
		if container.Service == "server" {
			present, err := runtimePresent(ctx, "container", container.ID)
			require.NoError(t, err)
			require.False(t, present, "server is stopped and removed before its database")
		}
		if container.Service == "postgres" {
			databaseID = container.ID
		}
	}
	_, err = runtimeDocker(ctx, "container", "exec", databaseID, "touch", "/allow")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		container, err := inspectRuntimeContainer(ctx, databaseID)
		return err == nil && !container.Running
	}, 10*time.Second, 100*time.Millisecond)
	retire.StopTimeout = 10 * time.Second
	require.NoError(t, RunRuntimeCommand(ctx, retire, io.Discard))
	retired, err := readRuntimeReceipt(old.command.Directory)
	require.NoError(t, err)
	require.Equal(t, "retired", retired.State)
	require.NoError(t, verifyRetiredRuntime(ctx, retired))
	require.NoError(t, verifyProtectedRuntimes(ctx, protected))
	require.NoDirExists(t, filepath.Join(old.command.Directory, "postgres"))
	for _, volume := range retired.Volumes {
		present, err := runtimePresent(ctx, "volume", volume.Name)
		require.NoError(t, err)
		require.Equal(t, volume.Name == sharedVolume, present, "only a volume still referenced by another container survives")
	}
	for _, image := range retired.Images {
		present, err := runtimePresent(ctx, "image", image.ID)
		require.NoError(t, err)
		if image.Owned && image.ID != sharedImage {
			require.False(t, present)
		} else {
			require.True(t, present, "shared fixture image must survive")
		}
	}
	_, err = runtimeDocker(ctx, "container", "rm", sharedID)
	require.NoError(t, err)
	_, err = runtimeDocker(ctx, "volume", "rm", sharedVolume)
	require.NoError(t, err)
	_, err = runtimeDocker(ctx, "image", "rm", sharedImage)
	require.NoError(t, err)
	require.NotEmpty(t, retired.Retirement.Helpers, "permission-restricted bind requires a scoped helper")
	require.NoError(t, RunRuntimeCommand(ctx, retire, io.Discard), "retirement is resumable and idempotent")
	newEvalRuntimeFixture(t, ctx, false, old.command.Directory)

	script, err := filepath.Abs("../../scripts/eval-local.sh")
	require.NoError(t, err)
	process := exec.CommandContext(ctx, "bash", script, "--mode", "runtime", "--runtime-action", "verify", "--runtime-dir", old.command.Directory)
	output, err := process.CombinedOutput()
	require.NoError(t, err, "%s", output)
	var summary map[string]string
	require.NoError(t, json.Unmarshal(output, &summary))
	require.Equal(t, "retired", summary["state"])
}

func TestEvalRuntimeDockerPartialPreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := evalRuntimeDockerRoot(t)
	base := "busybox:1.37.0-musl"
	if _, err := runtimeDocker(ctx, "image", "inspect", base); err != nil {
		_, err = runtimeDocker(ctx, "image", "pull", base)
		require.NoError(t, err)
	}
	compose := map[string]any{"services": map[string]any{
		"server": map[string]any{"image": base, "command": []string{"sh", "-ec", "trap 'exit 0' TERM; while :; do sleep 0.1; done"}, "volumes": []string{"${V1_COMPOSE_DATA_DIR}/data:/data"}},
		"broken": map[string]any{"image": base, "command": []string{"/missing-runtime-command"}},
	}}
	composePath := filepath.Join(root, "compose.json")
	require.NoError(t, writePairJSON(composePath, compose))
	command := RuntimeCommand{Action: "start", New: true, Directory: filepath.Join(root, "runtime"), Project: "eval627-" + uuid.NewString(), ComposeFile: composePath}
	plan := runtimeRetentionPlan{SchemaVersion: runtimeSchema, ArchiveDir: filepath.Join(root, "archive"), Outcome: "incomplete"}
	for _, kind := range runtimeEvidenceKinds {
		plan.Evidence = append(plan.Evidence, runtimeEvidence{Kind: kind, Unavailable: "startup failed before an evaluation began"})
	}
	planPath := filepath.Join(root, "retention.json")
	require.NoError(t, writePairJSON(planPath, plan))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := readRuntimeReceipt(command.Directory); os.IsNotExist(err) {
			return
		}
		if err := RunRuntimeCommand(cleanupCtx, RuntimeCommand{Action: "retire", Directory: command.Directory, RetentionPlan: planPath, StopTimeout: 10 * time.Second}, io.Discard); err != nil {
			t.Errorf("partial-start cleanup: %v", err)
			return
		}
		receipt, err := readRuntimeReceipt(command.Directory)
		if err != nil {
			t.Errorf("partial-start cleanup receipt: %v", err)
			return
		}
		removeEvalRuntimeFixture(t, root, receipt)
	})
	require.Error(t, RunRuntimeCommand(ctx, command, io.Discard))
	receipt, err := readRuntimeReceipt(command.Directory)
	require.NoError(t, err)
	require.Equal(t, "preparing", receipt.State)
	require.NotEmpty(t, receipt.Containers)
	require.NotEmpty(t, receipt.Networks)
	require.ErrorContains(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "verify", Directory: command.Directory}, io.Discard), "incomplete")
	require.NoError(t, RunRuntimeCommand(ctx, RuntimeCommand{Action: "retire", Directory: command.Directory, RetentionPlan: planPath, StopTimeout: 10 * time.Second}, io.Discard))
}
