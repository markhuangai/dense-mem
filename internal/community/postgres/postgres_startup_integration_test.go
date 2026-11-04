//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/markhuangai/dense-mem/internal/observability"
	dockercontainer "github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestCommunityTopicPostgresStartupReadiness(t *testing.T) {
	t.Setenv("LOG_LEVEL", "info")
	t.Run("final server accepts TCP SQL", func(t *testing.T) {
		var created []testcontainers.Container
		options, alias := postgresStartupTestOptions(t, &created)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		container, err := runLedgerPostgresContainer(ctx, options...)
		require.NoError(t, err)
		require.Len(t, created, 1)
		dsn, err := postgresContainerDSN(ctx, container, alias)
		require.NoError(t, err)
		conn, err := pgx.Connect(ctx, dsn)
		require.NoError(t, err)
		defer func() { require.NoError(t, conn.Close(context.Background())) }()
		var answer int
		var tcp bool
		require.NoError(t, conn.QueryRow(ctx, "SELECT 1, inet_server_addr() IS NOT NULL").Scan(&answer, &tcp))
		require.Equal(t, 1, answer)
		require.True(t, tcp)
	})
	t.Run("temporary server does not satisfy readiness", func(t *testing.T) {
		var created []testcontainers.Container
		options, _ := postgresStartupTestOptions(t, &created)
		options = append(options, postgresStartupInitScript(t, "echo startup-init-blocked\nsleep 35\n"))
		started := time.Now()
		container, err := runLedgerPostgresContainer(context.Background(), options...)
		require.Nil(t, container)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.GreaterOrEqual(t, time.Since(started), 30*time.Second)
		require.Contains(t, err.Error(), "matched 1 times, expected 2")
		require.Contains(t, err.Error(), "startup-init-blocked")
		require.Contains(t, err.Error(), "running=true")
		require.Contains(t, err.Error(), "logs_truncated=false")
		require.NotContains(t, err.Error(), "PostgreSQL init process complete")
		assertPostgresStartupContainerRemoved(t, created)
	})
	for _, recover := range []bool{true, false} {
		label := "port collision exhausts four attempts"
		if recover {
			label = "port collision recovers after cleanup"
		}
		t.Run(label, func(t *testing.T) {
			var blockerCreated []testcontainers.Container
			blockerOptions, _ := postgresStartupTestOptions(t, &blockerCreated)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			blocker, err := runLedgerPostgresContainer(ctx, blockerOptions...)
			require.NoError(t, err)
			port, err := blocker.MappedPort(ctx, "5432/tcp")
			require.NoError(t, err)
			var created []testcontainers.Container
			options, alias := postgresStartupTestOptions(t, &created)
			options = append(options, testcontainers.WithHostConfigModifier(func(config *dockercontainer.HostConfig) {
				for _, prior := range created {
					assertPostgresStartupContainerRemoved(t, []testcontainers.Container{prior})
				}
				if !recover || len(created) == 0 {
					config.PortBindings = dockernetwork.PortMap{
						dockernetwork.MustParsePort("5432/tcp"): {{HostPort: port.Port()}},
					}
				}
			}))
			var container *tcpostgres.PostgresContainer
			logs := capturePostgresStartupLogs(t, func() { container, err = runLedgerPostgresContainer(ctx, options...) })
			if !recover {
				require.Error(t, err)
				require.Nil(t, container)
				require.Len(t, created, 4)
				require.Equal(t, 3, strings.Count(logs, `"msg":"postgres_fixture_retry"`))
				require.Contains(t, logs, `"msg":"postgres_fixture_failed"`)
				for _, prior := range created {
					assertPostgresStartupContainerRemoved(t, []testcontainers.Container{prior})
				}
				return
			}
			require.NoError(t, err)
			require.Len(t, created, 2)
			require.Equal(t, 1, strings.Count(logs, `"msg":"postgres_fixture_retry"`))
			require.Contains(t, logs, `"msg":"postgres_fixture_removed"`)
			require.Contains(t, logs, `"msg":"postgres_fixture_ready"`)
			require.Contains(t, logs, `"attempt":2`)
			dsn, err := postgresContainerDSN(ctx, container, alias)
			require.NoError(t, err)
			conn, err := pgx.Connect(ctx, dsn)
			require.NoError(t, err)
			defer func() { require.NoError(t, conn.Close(context.Background())) }()
			var answer int
			require.NoError(t, conn.QueryRow(ctx, "SELECT 1").Scan(&answer))
			require.Equal(t, 1, answer)
		})
	}
}

func TestCommunityTopicPostgresStartupFailureCleanup(t *testing.T) {
	t.Setenv("LOG_LEVEL", "info")
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprintf("truncated=%t", truncated), func(t *testing.T) {
			var created []testcontainers.Container
			options, _ := postgresStartupTestOptions(t, &created)
			secret := "startup-operational-credential-691+/"
			encoded := url.QueryEscape(secret)
			script := fmt.Sprintf("echo 'startup-init-failure: address already in use'\necho '%s %s admitted-password-field'\n", secret, encoded)
			if truncated {
				script += fmt.Sprintf("printf '%%s' '%s'\nhead -c 20000 /dev/zero | tr '\\000' x\n", strings.Repeat(secret, 600))
			}
			script += "echo startup-log-end\nexit 17\n"
			options = append(options, postgresStartupInitScript(t, script))
			var container *tcpostgres.PostgresContainer
			var err error
			logs := capturePostgresStartupLogs(t, func() {
				container, err = runLedgerPostgresContainer(observability.WithAuthenticationSecrets(context.Background(), secret), options...)
			})
			require.NotContains(t, logs, secret)
			require.NotContains(t, logs, encoded)
			require.Contains(t, logs, fmt.Sprintf(`"logs_truncated":%t`, truncated))
			require.Contains(t, logs, `"exit_code":17`)
			require.Contains(t, logs, `"running":false`)
			require.Contains(t, logs, `"oom_killed":false`)
			require.NotContains(t, logs, `"msg":"postgres_fixture_retry"`)
			require.Nil(t, container)
			require.ErrorContains(t, err, "container exited with code 17")
			require.NotContains(t, err.Error(), secret)
			require.NotContains(t, err.Error(), encoded)
			require.Contains(t, err.Error(), "running=false")
			require.Contains(t, err.Error(), "exit_code=17")
			require.Contains(t, err.Error(), "oom_killed=false")
			require.Contains(t, err.Error(), fmt.Sprintf("logs_truncated=%t", truncated))
			var manifest map[string]any
			var chunks []string
			for _, line := range strings.Split(logs, "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				if event["msg"] == "postgres_fixture_diagnostics" {
					manifest = event
				}
				if event["msg"] == "postgres_fixture_log_chunk" {
					require.Equal(t, float64(len(chunks)), event["chunk_index"])
					chunks = append(chunks, event["content"].(string))
				}
			}
			require.NotNil(t, manifest)
			if truncated {
				require.Equal(t, float64(16*1024), manifest["log_bytes"])
				require.Equal(t, "incomplete startup log capture", manifest["logs_unavailable"])
				require.Empty(t, chunks)
				require.NotContains(t, logs, secret[:16])
				require.NotContains(t, err.Error(), secret[:16])
				require.Contains(t, err.Error(), "[omitted: incomplete startup log capture]")
				require.NotContains(t, err.Error(), "startup-log-end")
			} else {
				require.NotContains(t, logs, "diagnostic_unavailable_reason")
				require.Len(t, chunks, int(manifest["chunk_count"].(float64)))
				reconstructed := strings.Join(chunks, "")
				require.Len(t, reconstructed, int(manifest["protected_bytes"].(float64)))
				require.Contains(t, reconstructed, "startup-init-failure: address already in use")
				require.Contains(t, reconstructed, "admitted-password-field")
				require.Contains(t, reconstructed, observability.CredentialProtectionRedacted)
				excerpt := strings.SplitN(err.Error(), "Postgres startup logs (logs_truncated=false):\n", 2)
				require.Len(t, excerpt, 2)
				require.Equal(t, reconstructed, excerpt[1])
			}
			assertPostgresStartupContainerRemoved(t, created)
		})
	}
}

func capturePostgresStartupLogs(t *testing.T, run func()) (captured string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	original := os.Stdout
	os.Stdout = writer
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(&output, reader)
		done <- err
	}()
	defer func() {
		os.Stdout = original
		require.NoError(t, writer.Close())
		require.NoError(t, <-done)
		require.NoError(t, reader.Close())
		captured = output.String()
		_, err := original.Write(output.Bytes())
		require.NoError(t, err)
	}()
	run()
	return
}

func TestCommunityTopicPostgresStartupCancellationCleanup(t *testing.T) {
	var created []testcontainers.Container
	options, _ := postgresStartupTestOptions(t, &created)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options = append(options, testcontainers.WithAdditionalLifecycleHooks(testcontainers.ContainerLifecycleHooks{
		PostCreates: []testcontainers.ContainerHook{func(context.Context, testcontainers.Container) error {
			cancel()
			return nil
		}},
	}))
	container, err := runLedgerPostgresContainer(ctx, options...)
	require.Nil(t, container)
	require.ErrorIs(t, err, context.Canceled)
	require.Contains(t, err.Error(), "status=created")
	require.NotContains(t, err.Error(), "collect Postgres startup")
	assertPostgresStartupContainerRemoved(t, created)
}

func postgresStartupTestOptions(t *testing.T, created *[]testcontainers.Container) ([]testcontainers.ContainerCustomizer, string) {
	t.Helper()
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("set DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 to run disposable PostgreSQL startup tests")
	}
	alias := "postgres-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	options := append(precheckContainerLabels(), precheckNetworkOptions(alias)...)
	options = append(options, testcontainers.WithAdditionalLifecycleHooks(testcontainers.ContainerLifecycleHooks{
		PostCreates: []testcontainers.ContainerHook{func(_ context.Context, container testcontainers.Container) error {
			*created = append(*created, container)
			t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })
			return nil
		}},
	}))
	return options, alias
}

func postgresStartupInitScript(t *testing.T, content string) testcontainers.ContainerCustomizer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "startup.sh")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return tcpostgres.WithInitScripts(path)
}

func assertPostgresStartupContainerRemoved(t *testing.T, created []testcontainers.Container) {
	t.Helper()
	require.Len(t, created, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := created[0].State(ctx)
	require.True(t, errdefs.IsNotFound(err), "container %s must be removed, got %v", created[0].GetContainerID(), err)
}
