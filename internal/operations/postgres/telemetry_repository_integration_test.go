//go:build integration

package postgres

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestEvidenceOnlyPostgresOperationalTelemetryCountsActiveGenerations(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "telemetry-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "telemetry-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "owner-c")
	privateA, err := ensurePrivateSpaceForOwner(t, appDB, rls, ctx, uuid.MustParse(teamA), uuid.MustParse(ownerA))
	require.NoError(t, err)
	privateB, err := ensurePrivateSpaceForOwner(t, appDB, rls, ctx, uuid.MustParse(teamA), uuid.MustParse(ownerB))
	require.NoError(t, err)
	privateC, err := ensurePrivateSpaceForOwner(t, appDB, rls, ctx, uuid.MustParse(teamC), uuid.MustParse(ownerC))
	require.NoError(t, err)

	now := time.Now().UTC()
	completed := insertTelemetryAttempt(t, adminDB, rls, teamA, ownerA, nil, "completed", "", now)
	insertTelemetryAttempt(t, adminDB, rls, teamA, ownerA, nil, "replayed", completed, now)
	insertTelemetryAttempt(t, adminDB, rls, teamA, ownerA, privateA, "failed", "", now)
	insertTelemetryAttempt(t, adminDB, rls, teamC, ownerC, nil, "rejected", "", now.Add(-2*time.Hour))
	insertTelemetryAttempt(t, adminDB, rls, teamA, ownerA, nil, "failed", "", now.Add(-31*24*time.Hour))
	insertTelemetryAttempt(t, adminDB, rls, teamA, ownerB, privateB, "quarantined", "", now)
	insertTelemetryAttempt(t, adminDB, rls, teamC, ownerC, privateC, "completed", "", now)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE memory_spaces SET generation=generation+1, updated_at=now() WHERE id=?`, privateB.ID).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE memory_spaces SET lifecycle_state='sealed', generation=generation+1, sealed_at=now(), updated_at=now() WHERE id=?`, privateC.ID).Error
	}))

	reader := NewTelemetryLifecycleRepository(appDB, rls)
	snapshot, err := reader.ReadOperationalTelemetry(ctx)
	require.NoError(t, err)
	for _, window := range []string{"15m", "30m", "1h", "12h", "1d", "7d", "30d"} {
		require.Equal(t, 1.0, findWindowedTelemetryCount(snapshot.RememberAttempts, window, "completed"))
		require.Equal(t, 1.0, findWindowedTelemetryCount(snapshot.RememberAttempts, window, "replayed"))
		require.Equal(t, 1.0, findWindowedTelemetryCount(snapshot.RememberAttempts, window, "failed"))
		require.Zero(t, findWindowedTelemetryCount(snapshot.RememberAttempts, window, "quarantined"))
	}
	require.Zero(t, findWindowedTelemetryCount(snapshot.RememberAttempts, "1h", "rejected"))
	require.Equal(t, 1.0, findWindowedTelemetryCount(snapshot.RememberAttempts, "12h", "rejected"))
	assertOperationalTelemetryIndexPlan(t, ctx, appDB, rls)

	for _, recreated := range []*TelemetryLifecycleRepository{reader, NewTelemetryLifecycleRepository(appDB, rls)} {
		metrics := observability.NewPrometheusMetrics()
		require.NoError(t, metrics.RegisterOperationalTelemetryCollector(recreated))
		response := httptest.NewRecorder()
		metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
		body := response.Body.String()
		require.Contains(t, body, `densemem_operational_ledger_collection_success 1`)
		require.Contains(t, body, `densemem_operational_remember_attempts{outcome="completed",window="15m"} 1`)
		for _, absent := range []string{teamA, teamC, ownerA, ownerB, ownerC, completed, "densemem_operational_relationships", "densemem_operational_dream", "densemem_operational_hypothesis"} {
			require.NotContains(t, body, absent)
		}
	}
}

func TestEvidenceOnlyPostgresOperationalTelemetryFailureIsVisible(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec("REVOKE SELECT ON TABLE remember_attempts FROM " + ledgerTestRole).Error
	}))
	defer func() {
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Exec("GRANT SELECT ON TABLE remember_attempts TO " + ledgerTestRole).Error
		}))
	}()
	reader := NewTelemetryLifecycleRepository(appDB, rls)
	_, err := reader.ReadOperationalTelemetry(ctx)
	require.ErrorContains(t, err, "permission denied")
	metrics := observability.NewPrometheusMetrics()
	require.NoError(t, metrics.RegisterOperationalTelemetryCollector(reader))
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	require.Contains(t, response.Body.String(), `densemem_operational_ledger_collection_success 0`)
	require.NotContains(t, response.Body.String(), "densemem_operational_remember_attempts{")
}

func insertTelemetryAttempt(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, teamID, ownerID string, space *domain.MemorySpace, outcome, canonicalID string, createdAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	requestID := id
	if canonicalID != "" {
		requestID = canonicalID
	}
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		var spaceID string
		var generation int64
		if space != nil {
			spaceID = space.ID.String()
			generation = space.Generation
		} else if err := tx.Raw(`SELECT dense_mem_team_shared_space(?::uuid)::text, dense_mem_team_shared_generation(?::uuid)`, teamID, teamID).Row().Scan(&spaceID, &generation); err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO remember_attempts (
            team_id, attempt_id, owner_profile_id, space_id, space_generation, idempotency_key,
            request_hash, contract_version, submission_kind, outcome, canonical_attempt_id, created_at
        ) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, 'remember', ?, NULLIF(?, '')::uuid, ?)`,
			teamID, id, ownerID, spaceID, generation, "telemetry-"+requestID, "hash-"+requestID, domain.ContractVersion, outcome, canonicalID, createdAt).Error
	}))
	return id
}

func assertOperationalTelemetryIndexPlan(t *testing.T, ctx context.Context, db *gorm.DB, rls *storagepostgres.RLS) {
	t.Helper()
	var plan strings.Builder
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		rows, err := tx.Raw(`EXPLAIN (COSTS OFF) WITH ` + operationalTelemetryWindowedCTEs + `, ` + operationalTelemetryRecentRememberAttemptsCTE + `
            SELECT window_bounds.window_key, attempt.outcome, count(*)
            FROM recent_remember_attempts attempt
            JOIN active_semantic_spaces active_space ON active_space.team_id=attempt.team_id
                AND active_space.space_id=attempt.space_id AND active_space.generation=attempt.space_generation
            CROSS JOIN window_bounds WHERE attempt.created_at>=window_bounds.starts_at
            GROUP BY window_bounds.window_key, attempt.outcome`).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		return rows.Err()
	}))
	require.Contains(t, plan.String(), "Index Cond: (created_at >=")
	require.NotContains(t, plan.String(), "dense_mem_active_space_generation")
}

func findWindowedTelemetryCount(values []operationscontract.WindowedTelemetryCount, window, kind string) float64 {
	for _, value := range values {
		if value.Window == window && value.Kind == kind {
			return value.Count
		}
	}
	return 0
}
