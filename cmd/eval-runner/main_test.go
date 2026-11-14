package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestCLIRejectsRetiredInterfacesAndLocalCampaigns(t *testing.T) {
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	protector := observability.NewCredentialProtector()
	for _, mode := range []string{"paired", "import", "baseline", "candidate", "resume", "token-diagnostics", "runtime"} {
		require.ErrorContains(t, execute(context.Background(), []string{mode, "manifest"}, logger, protector), "workflow-internal")
	}
	t.Setenv("GITHUB_ACTIONS", "")
	require.ErrorContains(t, execute(context.Background(), []string{"run", "manifest"}, logger, protector), "GitHub-hosted")
}

func TestRuntimeLogCollectorProtectsOutputAndExposesIncompleteCapture(t *testing.T) {
	var output bytes.Buffer
	protector := observability.NewCredentialProtector("secret-canary", "https://provider-canary.example")
	capture := &evalharness.Capture{Logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: protector, RunID: "test"}
	usage := map[string]evalharness.UsageTotals{}
	require.NoError(t, logReader(strings.NewReader("provider failed secret-canary at https://provider-canary.example\n"), capture, usage))
	require.NotContains(t, output.String(), "secret-canary")
	require.NotContains(t, output.String(), "https://provider-canary.example")
	broken := `{"msg":"ai_exchange_payload","exchange_id":"exchange","direction":"request","payload_chunk":{"state":"captured","count":2,"index":0,"body":"partial","sha256":"hash"}}` + "\n"
	require.ErrorContains(t, logReader(strings.NewReader(broken), capture, usage), "incomplete")
}
