//go:build integration

package postgres

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestCommunityTopicPostgresStartupReadiness(t *testing.T) {
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
}

func TestCommunityTopicPostgresStartupFailureCleanup(t *testing.T) {
	var created []testcontainers.Container
	options, _ := postgresStartupTestOptions(t, &created)
	options = append(options, postgresStartupInitScript(t, "echo 'startup-init-failure: address already in use'\nhead -c 20000 /dev/zero | tr '\\000' x\necho startup-log-end\nexit 17\n"))
	container, err := runLedgerPostgresContainer(context.Background(), options...)
	require.Nil(t, container)
	require.ErrorContains(t, err, "container exited with code 17")
	require.Contains(t, err.Error(), "running=false")
	require.Contains(t, err.Error(), "exit_code=17")
	require.Contains(t, err.Error(), "oom_killed=false")
	require.Contains(t, err.Error(), "startup-init-failure: address already in use")
	require.Contains(t, err.Error(), "logs_truncated=true")
	require.NotContains(t, err.Error(), "startup-log-end")
	excerpt := strings.SplitN(err.Error(), "Postgres startup logs (logs_truncated=true):\n", 2)
	require.Len(t, excerpt, 2)
	require.Len(t, excerpt[1], 16*1024)
	assertPostgresStartupContainerRemoved(t, created)
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
