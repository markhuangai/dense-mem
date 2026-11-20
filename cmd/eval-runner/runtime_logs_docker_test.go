package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/stretchr/testify/require"
)

func TestRuntimeLogDockerOwnershipAndCompleteCollection(t *testing.T) {
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("explicit disposable Docker regression execution required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const image = "busybox:1.37.0-musl"
	docker := func(args ...string) []byte {
		t.Helper()
		data, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		require.NoError(t, err, string(data))
		return data
	}
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", image).Run(); err != nil {
		docker("image", "pull", image)
	}
	owner := "logs-" + uuid.NewString()
	create := func(run, label, text string, live bool) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "provider.log")
		require.NoError(t, os.WriteFile(file, []byte(text), 0o600))
		name := "densemem-eval-" + run + "-server"
		command := []string{"cat", "/provider.log"}
		if live {
			command = []string{"sh", "-ec", "trap 'exit 0' TERM; cat /provider.log; touch /ready; while true; do sleep 1; done"}
		}
		args := []string{"container", "create", "--name", name, "--label", "io.dense-mem.eval.run=" + label,
			"--mount", "type=bind,source=" + file + ",target=/provider.log,readonly", image}
		id := strings.TrimSpace(string(docker(append(args, command...)...)))
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			actual, err := exec.CommandContext(cleanup, "docker", "inspect", "--format", `{{index .Config.Labels "io.dense-mem.eval.run"}}`, id).Output()
			require.NoError(t, err)
			require.Equal(t, label, strings.TrimSpace(string(actual)))
			require.NoError(t, exec.CommandContext(cleanup, "docker", "container", "rm", "--force", id).Run())
		})
		if live {
			docker("container", "start", id)
			docker("container", "exec", id, "sh", "-ec", "until test -f /ready; do sleep 1; done")
		} else {
			docker("container", "start", "--attach", id)
		}
		return name
	}
	var output bytes.Buffer
	capture := &evalharness.Capture{RunID: owner, Logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: evalharness.NewRunnerCredentialProtector(runtimeLogKeyCanary, runtimeLogURLCanary)}
	t.Setenv("EVAL_SERVER_CONTAINER", create(owner, owner, runtimeProviderLogFixture(t), true))
	usage := map[string]evalharness.UsageTotals{}
	require.ErrorContains(t, runtimeLogs(ctx, owner, capture, usage, true, true), "still running")
	require.NoError(t, stopRuntime(ctx, owner, false))
	require.NoError(t, stopRuntime(ctx, owner, false))
	require.NoError(t, runtimeLogs(ctx, owner, capture, usage, true, true))
	require.Len(t, usage, 5)
	require.EqualValues(t, 1, usage["ontology"].Unavailable)
	protected := runtimeCapturePayloads(t, output.Bytes())
	require.NotContains(t, protected, runtimeLogKeyCanary)
	require.NotContains(t, protected, runtimeLogURLCanary)
	require.ErrorContains(t, runtimeLogs(ctx, "another-run", capture, usage, true, true), "identity mismatch")
	foreign := "logs-" + uuid.NewString()
	foreignContainer := create(foreign, owner, "foreign provider exchange\n", true)
	t.Setenv("EVAL_SERVER_CONTAINER", foreignContainer)
	require.ErrorContains(t, stopRuntime(ctx, foreign, false), "ownership cannot be verified")
	require.Equal(t, "true", strings.TrimSpace(string(docker("container", "inspect", "--format", "{{.State.Running}}", foreignContainer))))
	before := output.String()
	require.ErrorContains(t, runtimeLogs(ctx, foreign, capture, usage, true, true), "ownership cannot be verified")
	require.Equal(t, before, output.String(), "foreign logs are rejected before capture")
	other := "logs-" + uuid.NewString()
	t.Setenv("EVAL_SERVER_CONTAINER", create(other, other, "no exchanges\n", false))
	empty := &evalharness.Capture{RunID: other, Logger: capture.Logger, Protector: capture.Protector}
	require.ErrorContains(t, runtimeLogs(ctx, other, empty, map[string]evalharness.UsageTotals{}, true, true), "required runtime provider exchanges are missing")
	require.NoError(t, runtimeLogs(ctx, other, empty, map[string]evalharness.UsageTotals{}, false, true))
}
