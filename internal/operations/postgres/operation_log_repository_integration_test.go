//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestOperationLogRepositoryInvocationLookupUsesIdentityIndex(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID, otherTeamID, profileID, otherProfileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	invocationID := uuid.NewString()
	otherInvocationID := uuid.NewString()
	const retainedRows = 1_000_000
	now := time.Now().UTC()

	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO operation_logs (
				timestamp, severity, severity_rank, message, source, team_id, profile_id, attrs
			)
			SELECT
				now() - (series * interval '1 second'), 'INFO', 20, 'retained operation log', 'test', $1::uuid, $2::uuid,
				CASE WHEN series <= 3
				     THEN jsonb_build_object('invocation_id', $3::text)
				     ELSE '{}'::jsonb
				END
			FROM generate_series(1, $4::int) AS series
		`, teamID, profileID, invocationID, retainedRows).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO operation_logs (
				timestamp, severity, severity_rank, message, source, team_id, profile_id, attrs
			) VALUES
				(now(), 'INFO', 20, 'other team invocation', 'test', $1::uuid, $2::uuid, jsonb_build_object('invocation_id', $4::text)),
				(now(), 'INFO', 20, 'same team other profile', 'test', $3::uuid, $5::uuid, jsonb_build_object('invocation_id', $4::text)),
				(now(), 'INFO', 20, 'same team other invocation', 'test', $3::uuid, $2::uuid, jsonb_build_object('invocation_id', $6::text))
		`, otherTeamID, profileID, teamID, invocationID, otherProfileID, otherInvocationID).Error
	}))

	repo := NewOperationLogRepository(appDB, rls)
	page, err := repo.List(ctx, domain.OperationLogFilter{
		TeamID:       &teamID,
		InvocationID: invocationID,
		Limit:        2,
		Offset:       1,
	})
	require.NoError(t, err)
	require.EqualValues(t, 4, page.Total)
	require.Len(t, page.Items, 2)
	for _, item := range page.Items {
		require.NotNil(t, item.TeamID)
		require.Equal(t, teamID, *item.TeamID)
		require.Equal(t, invocationID, item.Attrs["invocation_id"])
	}

	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	require.NoError(t, dropOperationLogInvocationIndex(ctx, adminSQL))
	for _, countQuery := range []bool{false, true} {
		for _, planCacheMode := range []string{"force_custom_plan", "force_generic_plan"} {
			for _, scale := range []int{100_000, retainedRows} {
				plan := explainOperationLogInvocationLookup(t, ctx, adminSQL, teamID, invocationID, now.Add(-time.Duration(scale)*time.Second), planCacheMode, countQuery)
				require.NotContains(t, plan.indexNames, "operation_logs_team_invocation_timestamp_idx", countQuery, planCacheMode, scale)
				require.Greater(t, plan.rowsRemoved, float64(scale/2), countQuery, planCacheMode, scale)
			}
		}
	}

	require.NoError(t, createOperationLogInvocationIndex(ctx, adminSQL))
	for _, countQuery := range []bool{false, true} {
		for _, planCacheMode := range []string{"force_custom_plan", "force_generic_plan"} {
			for _, scale := range []int{100_000, retainedRows} {
				plan := explainOperationLogInvocationLookup(t, ctx, adminSQL, teamID, invocationID, now.Add(-time.Duration(scale)*time.Second), planCacheMode, countQuery)
				require.Contains(t, plan.indexNames, "operation_logs_team_invocation_timestamp_idx", countQuery, planCacheMode, scale)
				require.Less(t, plan.rowsRemoved, float64(10), countQuery, planCacheMode, scale)
			}
		}
	}
}

type operationLogExplainPlan struct {
	indexNames  []string
	rowsRemoved float64
}

func explainOperationLogInvocationLookup(t *testing.T, ctx context.Context, db *sql.DB, teamID uuid.UUID, invocationID string, from time.Time, planCacheMode string, countQuery bool) operationLogExplainPlan {
	t.Helper()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()

	where := operationLogWhereClause(normalizeOperationLogFilter(domain.OperationLogFilter{
		TeamID:       &teamID,
		InvocationID: invocationID,
		From:         &from,
		Limit:        100,
	}))
	statementName := "operation_log_invocation_lookup"
	parameterTypes := "text, text, uuid, text, text, text, text, text, text, text, text, timestamptz, timestamptz"
	executeArgs := fmt.Sprintf("'', '', '%s'::uuid, '', '%s', '', '', '', '', '', '', '%s'::timestamptz, NULL", teamID.String(), invocationID, from.UTC().Format(time.RFC3339Nano))
	query := `SELECT id::text, timestamp, attrs FROM operation_logs WHERE ` + where + ` ORDER BY timestamp DESC, id DESC LIMIT $14 OFFSET $15`
	if countQuery {
		statementName = "operation_log_invocation_count"
		query = `SELECT count(*) FROM operation_logs WHERE ` + where
	}
	prepare := `PREPARE ` + statementName + ` (` + parameterTypes
	if !countQuery {
		prepare += ", int, int"
	}
	prepare += `) AS ` + query
	_, err = conn.ExecContext(ctx, `SELECT set_config('app.tx_mode', 'system', false)`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `SET plan_cache_mode = `+planCacheMode)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, prepare)
	require.NoError(t, err)
	defer func() { _, _ = conn.ExecContext(context.Background(), `DEALLOCATE `+statementName) }()
	if !countQuery {
		executeArgs += ", 100, 0"
	}
	explain := fmt.Sprintf(`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE %s(%s)`, statementName, executeArgs)
	var raw []byte
	require.NoError(t, conn.QueryRowContext(ctx, explain).Scan(&raw))
	var documents []map[string]any
	require.NoError(t, json.Unmarshal(raw, &documents))
	result := operationLogExplainPlan{}
	var visit func(map[string]any)
	visit = func(node map[string]any) {
		if name, ok := node["Index Name"].(string); ok {
			result.indexNames = append(result.indexNames, name)
		}
		if removed, ok := node["Rows Removed by Filter"].(float64); ok {
			result.rowsRemoved += removed
		}
		if plans, ok := node["Plans"].([]any); ok {
			for _, child := range plans {
				if childNode, ok := child.(map[string]any); ok {
					visit(childNode)
				}
			}
		}
	}
	visit(documents[0]["Plan"].(map[string]any))
	return result
}

func dropOperationLogInvocationIndex(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx`)
	return err
}

func createOperationLogInvocationIndex(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE INDEX CONCURRENTLY operation_logs_team_invocation_timestamp_idx
		    ON operation_logs(team_id, (attrs ->> 'invocation_id'), timestamp DESC, id DESC)
		    WHERE (attrs ->> 'invocation_id') IS NOT NULL
	`)
	return err
}
