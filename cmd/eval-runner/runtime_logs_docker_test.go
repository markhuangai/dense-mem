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
	create := func(run, label, text string) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "provider.log")
		require.NoError(t, os.WriteFile(file, []byte(text), 0o600))
		name := "densemem-eval-" + run + "-server"
		id := strings.TrimSpace(string(docker("container", "create", "--name", name, "--label", "io.dense-mem.eval.run="+label, "--mount", "type=bind,source="+file+",target=/provider.log,readonly", image, "cat", "/provider.log")))
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			actual, err := exec.CommandContext(cleanup, "docker", "inspect", "--format", `{{index .Config.Labels "io.dense-mem.eval.run"}}`, id).Output()
			require.NoError(t, err)
			require.Equal(t, label, strings.TrimSpace(string(actual)))
			require.NoError(t, exec.CommandContext(cleanup, "docker", "container", "rm", "--force", id).Run())
		})
		docker("container", "start", "--attach", id)
		return name
	}
	var output bytes.Buffer
	capture := &evalharness.Capture{RunID: owner, Logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: evalharness.NewRunnerCredentialProtector(runtimeLogKeyCanary, runtimeLogURLCanary)}
	t.Setenv("EVAL_SERVER_CONTAINER", create(owner, owner, runtimeProviderLogFixture(t)))
	usage := map[string]evalharness.UsageTotals{}
	require.NoError(t, runtimeLogs(ctx, owner, capture, usage, true))
	require.Len(t, usage, 5)
	require.EqualValues(t, 2, usage["ontology"].Unavailable)
	protected := runtimeCapturePayloads(t, output.Bytes())
	require.NotContains(t, protected, runtimeLogKeyCanary)
	require.NotContains(t, protected, runtimeLogURLCanary)
	require.ErrorContains(t, runtimeLogs(ctx, "another-run", capture, usage, true), "identity mismatch")
	foreign := "logs-" + uuid.NewString()
	t.Setenv("EVAL_SERVER_CONTAINER", create(foreign, owner, "foreign provider exchange\n"))
	before := output.String()
	require.ErrorContains(t, runtimeLogs(ctx, foreign, capture, usage, true), "ownership cannot be verified")
	require.Equal(t, before, output.String(), "foreign logs are rejected before capture")
	other := "logs-" + uuid.NewString()
	t.Setenv("EVAL_SERVER_CONTAINER", create(other, other, "no exchanges\n"))
	empty := &evalharness.Capture{RunID: other, Logger: capture.Logger, Protector: capture.Protector}
	require.ErrorContains(t, runtimeLogs(ctx, other, empty, map[string]evalharness.UsageTotals{}, true), "required runtime provider exchanges are missing")
	require.NoError(t, runtimeLogs(ctx, other, empty, map[string]evalharness.UsageTotals{}, false))
}
