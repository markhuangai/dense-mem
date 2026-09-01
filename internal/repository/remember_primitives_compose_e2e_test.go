//go:build compose_e2e

package repository

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func TestComposeRememberPrimitives(t *testing.T) {
	baseDSN := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if baseDSN == "" {
		t.Fatal("DATABASE_URL is required for the Compose primitive driver")
	}
	parsed, err := url.Parse(baseDSN)
	require.NoError(t, err)
	databaseName := "dense_mem_compose_primitives_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	adminDB, err := sql.Open("pgx", baseDSN)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, adminDB.PingContext(ctx))
	quotedDatabase := `"` + strings.ReplaceAll(databaseName, `"`, `""`) + `"`
	_, err = adminDB.ExecContext(ctx, "CREATE DATABASE "+quotedDatabase+" TEMPLATE template0")
	require.NoError(t, err)
	defer func() {
		_, _ = adminDB.ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, databaseName)
		_, _ = adminDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quotedDatabase)
		_ = adminDB.Close()
	}()

	parsed.Path = "/" + databaseName
	query := parsed.Query()
	query.Set("sslmode", "disable")
	parsed.RawQuery = query.Encode()
	t.Setenv("DATABASE_URL", parsed.String())
	t.Setenv("DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS", "1")
	runRememberEvidenceOnlyAtomicScenario(t)
}
