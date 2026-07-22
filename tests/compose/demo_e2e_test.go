package compose_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDockerComposeDemoE2E_FreshStack(t *testing.T) {
	if os.Getenv("DENSE_MEM_DEMO_E2E") != "1" {
		t.Skip("set DENSE_MEM_DEMO_E2E=1 to run Docker-backed demo E2E")
	}

	root := repoRoot(t)
	project := "dense-mem-demo-e2e"
	image := "dense-mem:demo-e2e"
	port := freeTCPPort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	env := demoE2EEnv(image, port)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	runDocker(t, ctx, root, nil, "version")
	runCompose(t, ctx, root, env, project, "down", "-v", "--remove-orphans")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		runComposeBestEffort(t, cleanupCtx, root, env, project, "down", "-v", "--remove-orphans")
	})

	runDocker(t, ctx, root, nil, "build", "-f", "Dockerfile.demo", "-t", image, ".")
	runCompose(t, ctx, root, env, project, "up", "-d", "--force-recreate")

	waitForDemoHealth(t, ctx, baseURL)
	assertDemoLanding(t, ctx, baseURL)
	session := createDemoSession(t, ctx, baseURL)
	require.NotEmpty(t, session.APIKey)
	require.Equal(t, baseURL+"/mcp", session.MCPURL)
	require.Equal(t, baseURL+"/ui", session.UIURL)
	require.WithinDuration(t, time.Now().UTC().Add(24*time.Hour), session.ExpiresAt, 2*time.Minute)
	assertUserPortal(t, ctx, baseURL)
	assertOnlyDailyIssueCounter(t, ctx, root, env, project)
}

func repoRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	return root
}

func freeTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func demoE2EEnv(image string, port int) []string {
	return []string{
		"DENSE_MEM_DEMO_IMAGE=" + image,
		fmt.Sprintf("DENSE_MEM_DEMO_PORT=%d", port),
		"POSTGRES_PASSWORD=demo-postgres-e2e",
		"AI_API_URL=https://api.openai.com/v1",
		"AI_API_KEY=demo-e2e-placeholder",
		"AI_API_EMBEDDING_MODEL=text-embedding-3-large",
		"AI_API_EMBEDDING_DIMENSIONS=3072",
		"AI_VERIFIER_MODEL=gpt-4o-mini",
	}
}

func runDocker(t *testing.T, ctx context.Context, dir string, env []string, args ...string) string {
	t.Helper()
	return runCommand(t, ctx, dir, env, "docker", args...)
}

func runCompose(t *testing.T, ctx context.Context, dir string, env []string, project string, args ...string) string {
	t.Helper()
	base := []string{"compose", "-p", project, "-f", "examples/docker-compose.demo.yml"}
	return runDocker(t, ctx, dir, env, append(base, args...)...)
}

func runComposeBestEffort(t *testing.T, ctx context.Context, dir string, env []string, project string, args ...string) {
	t.Helper()
	base := []string{"compose", "-p", project, "-f", "examples/docker-compose.demo.yml"}
	_, _ = runCommandBestEffort(ctx, dir, env, "docker", append(base, args...)...)
}

func runCommand(t *testing.T, ctx context.Context, dir string, env []string, name string, args ...string) string {
	t.Helper()

	out, err := runCommandBestEffort(ctx, dir, env, name, args...)
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func runCommandBestEffort(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func waitForDemoHealth(t *testing.T, ctx context.Context, baseURL string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		status, body, err := httpRequest(ctx, http.MethodGet, baseURL+"/health", nil)
		if err == nil && status == http.StatusOK && strings.Contains(body, `"status":"ok"`) {
			return
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("status %d body %s", status, body)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("demo health did not become ready: %v", lastErr)
}

func assertDemoLanding(t *testing.T, ctx context.Context, baseURL string) {
	t.Helper()

	status, body, err := httpRequest(ctx, http.MethodGet, baseURL+"/", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "<title>Dense-Mem Demo</title>")
	require.Contains(t, body, "/demo/api/session")
	require.Contains(t, body, "window.location.origin + '/mcp'")
}

type demoSessionResponse struct {
	APIKey    string    `json:"api_key"`
	MCPURL    string    `json:"mcp_url"`
	UIURL     string    `json:"ui_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func createDemoSession(t *testing.T, ctx context.Context, baseURL string) demoSessionResponse {
	t.Helper()

	status, body, err := httpRequest(ctx, http.MethodPost, baseURL+"/demo/api/session", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)

	var session demoSessionResponse
	require.NoError(t, json.Unmarshal([]byte(body), &session))
	return session
}

func assertUserPortal(t *testing.T, ctx context.Context, baseURL string) {
	t.Helper()

	status, body, err := httpRequest(ctx, http.MethodGet, baseURL+"/ui", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "<!doctype html>")
}

func assertOnlyDailyIssueCounter(t *testing.T, ctx context.Context, root string, env []string, project string) {
	t.Helper()

	out := runCompose(t, ctx, root, env, project, "exec", "-T", "redis", "redis-cli", "--scan", "--pattern", "demo:issue:*")
	lines := nonEmptyLines(out)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], ":day:")
	require.NotContains(t, lines[0], ":hour:")
	require.NotContains(t, lines[0], ":global:")
}

func nonEmptyLines(text string) []string {
	lines := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func httpRequest(ctx context.Context, method string, url string, body io.Reader) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(b), nil
}
