//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const budgetRetirementVersion = 20261023020000

type legacyMaintenance struct {
	f                                     *ontologyFixture
	provider                              *goose.Provider
	window, run, batch, assessment, grant string
	deferred                              ontology.SourceHandle
	endsAt, pendingAt                     time.Time
}

func legacyMaintenanceFixture(t *testing.T) legacyMaintenance {
	t.Helper()
	f := newOrganizationFixture(t)
	provider := ontologyMigrationProvider(t, f.admin)
	_, err := provider.ApplyVersion(context.Background(), budgetRetirementVersion, false)
	require.NoError(t, err)
	x := legacyMaintenance{f: f, provider: provider, window: uuid.NewString(), run: uuid.NewString(), batch: uuid.NewString(), assessment: uuid.NewString(), grant: uuid.NewString(), endsAt: time.Now().UTC().Add(11 * time.Hour).Truncate(time.Microsecond)}
	x.deferred = f.organizationEvidence(t, 0, "Atlas stores PostgreSQL data.", nil)
	failed := f.organizationEvidence(t, 0, "Beacon stores SQLite data.", nil)
	ambiguous := f.organizationEvidence(t, 0, "Cedar storage is uncertain.", nil)
	sources, err := json.Marshal([]ontology.SourceHandle{x.deferred})
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.app, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO ontology_maintenance_windows(window_id,starts_at,ends_at,policy,charged_input,charged_output,reserved_input,reserved_output,overrun,additional_input,additional_output)
		 VALUES(?::uuid,?,?,?::jsonb,70,100,70,100,true,50,25)`, x.window, x.endsAt.Add(-12*time.Hour), x.endsAt,
			`{"enabled":true,"cadence_hours":12,"start_time_local":"03:00","timezone":"UTC","model":"fixture-model","max_concurrency":1,"input_tokens":10,"output_tokens":10,"settings_version":"legacy"}`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_runs(run_id,window_id,operation_key,request_hash,command,kind,status,max_batches,created_at,updated_at)
		 VALUES(?::uuid,?::uuid,'legacy-scheduled','legacy','{}','scheduled','pending',0,clock_timestamp(),clock_timestamp()),
		 (?::uuid,?::uuid,'legacy-grant','legacy','{"input_tokens":50,"output_tokens":25}','allowance','completed',0,clock_timestamp(),clock_timestamp())`, x.run, x.window, x.grant, x.window).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_batches(batch_id,run_id,window_id,team_id,shared_space_id,space_generation,lease_token,lease_until,sources,revisions,status,created_at,completed_at)
		 VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?,?::uuid,clock_timestamp()-interval '1 minute',?::jsonb,'{}','completed',clock_timestamp()-interval '2 minutes',clock_timestamp()-interval '1 minute')`,
			x.batch, x.run, x.window, f.team, f.space, f.generation, uuid.NewString(), string(sources)).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO ontology_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,reported_input,reported_output,reported_total,admitted_at,reconciled_at)
		 VALUES(?::uuid,?::uuid,1,?::uuid,70,100,0,0,0,clock_timestamp()-interval '2 minutes',clock_timestamp()-interval '1 minute')`, x.batch, x.assessment, x.window).Error
	}))
	for i, source := range []ontology.SourceHandle{x.deferred, failed, ambiguous} {
		require.NoError(t, f.store.withScope(context.Background(), f.team, false, func(tx *gorm.DB, fence scope) error {
			if err := refreshMaintenanceSource(tx, fence, source, false); err != nil {
				return err
			}
			return tx.Exec(`UPDATE ontology_maintenance_sources SET status=?,last_run_id=?::uuid WHERE team_id=?::uuid AND source_kind=? AND source_id=?`,
				[]string{"budget_deferred", "failed", "ambiguous"}[i], x.run, f.team, source.Kind, source.ID).Error
		}))
	}
	require.NoError(t, f.admin.Raw(`SELECT pending_at FROM ontology_maintenance_sources WHERE source_id=?`, x.deferred.ID).Row().Scan(&x.pendingAt))
	return x
}

func legacyMaintenanceHistory(t *testing.T, x legacyMaintenance) map[string]string {
	t.Helper()
	result := map[string]string{}
	for name, query := range map[string]string{
		"window":  `SELECT to_jsonb(row)::text FROM ontology_maintenance_windows AS row WHERE window_id=?::uuid`,
		"attempt": `SELECT jsonb_agg(to_jsonb(row)-'estimated_input_tokens'-'max_output_tokens' ORDER BY attempt)::text FROM ontology_maintenance_attempts AS row WHERE batch_id=?::uuid`,
		"grant":   `SELECT to_jsonb(row)::text FROM ontology_maintenance_runs AS row WHERE run_id=?::uuid`,
	} {
		id := x.window
		if name == "attempt" {
			id = x.batch
		}
		if name == "grant" {
			id = x.grant
		}
		var body string
		require.NoError(t, x.f.admin.Raw(query, id).Row().Scan(&body))
		result[name] = body
	}
	return result
}

func TestOntologyMaintenanceBudgetRetirementHistoryAndRequeue(t *testing.T) {
	x := legacyMaintenanceFixture(t)
	f, ctx := x.f, context.Background()
	before, canonical := legacyMaintenanceHistory(t, x), f.canonicalSnapshot(t)
	_, err := x.provider.ApplyVersion(ctx, budgetRetirementVersion, true)
	require.NoError(t, err)
	require.Equal(t, before, legacyMaintenanceHistory(t, x))
	require.Equal(t, canonical, f.canonicalSnapshot(t))
	var pendingAt time.Time
	var state string
	require.NoError(t, f.admin.Raw(`SELECT status,pending_at FROM ontology_maintenance_sources WHERE source_id=?`, x.deferred.ID).Row().Scan(&state, &pendingAt))
	require.Equal(t, "pending", state)
	require.True(t, x.pendingAt.Equal(pendingAt))
	var failed, ambiguous, closed, audits int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FILTER(WHERE status='failed'),count(*) FILTER(WHERE status='ambiguous') FROM ontology_maintenance_sources WHERE team_id=?::uuid`, f.team).Row().Scan(&failed, &ambiguous))
	require.Equal(t, 1, failed)
	require.Equal(t, 1, ambiguous)
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_runs WHERE run_id=?::uuid AND status='incomplete' AND failure_code='window_closed'`, x.run).Row().Scan(&closed))
	require.Equal(t, 1, closed)
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM audit_log WHERE operation='ONTOLOGY_BUDGET_RETIREMENT' AND entity_id=?`, x.run).Row().Scan(&audits))
	require.Equal(t, 1, audits)
	restarted := NewStore(f.app, f.rls)
	status, err := restarted.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.Nil(t, status.Window)
	require.EqualValues(t, 1, status.HistoricalUsage.UnavailableUsageAttempts)
	require.Zero(t, status.Usage.UnreconciledAttempts)
	runs, err := restarted.ListMaintenanceRuns(ctx, "", 100)
	require.NoError(t, err)
	var historical ontology.MaintenanceRun
	for _, run := range runs.Runs {
		if run.ID == x.grant {
			historical = run
		}
	}
	require.Equal(t, "allowance", historical.Kind)
	require.EqualValues(t, 50, historical.InputTokens)
	require.EqualValues(t, 25, historical.OutputTokens)
	config := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, config)
	require.NotEqual(t, x.window, window.ID)
	require.True(t, window.EndsAt.Equal(x.endsAt))
	require.WithinDuration(t, time.Now(), window.StartsAt, time.Minute)
	require.Equal(t, before, legacyMaintenanceHistory(t, x))
	_, err = x.provider.ApplyVersion(ctx, budgetRetirementVersion, true)
	require.ErrorIs(t, err, goose.ErrAlreadyApplied)
	_, err = x.provider.ApplyVersion(ctx, budgetRetirementVersion, false)
	require.ErrorContains(t, err, "recover by rolling forward")
}

func TestOntologyMaintenanceBudgetRetirementRejectsUnfinishedWork(t *testing.T) {
	for _, mode := range []string{"enabled", "batch", "preparation", "unreconciled"} {
		t.Run(mode, func(t *testing.T) {
			x := legacyMaintenanceFixture(t)
			f, ctx := x.f, context.Background()
			require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
				switch mode {
				case "enabled":
					return tx.Exec(`INSERT INTO app_config(key,value) VALUES('ONTOLOGY_MAINTENANCE_ENABLED','true') ON CONFLICT(key) DO UPDATE SET value='true'`).Error
				case "batch":
					return tx.Exec(`UPDATE ontology_maintenance_batches SET status='running' WHERE batch_id=?::uuid`, x.batch).Error
				case "preparation":
					return tx.Exec(`INSERT INTO ontology_maintenance_teams(team_id,shared_space_id,space_generation,lease_token,lease_until) VALUES(?::uuid,?::uuid,?,?::uuid,clock_timestamp()+interval '1 minute')`,
						f.team, f.space, f.generation, uuid.NewString()).Error
				default:
					return tx.Exec(`INSERT INTO ontology_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,admitted_at)
					 VALUES(?::uuid,?::uuid,2,?::uuid,10,10,clock_timestamp())`, x.batch, x.assessment, x.window).Error
				}
			}))
			before := legacyMaintenanceHistory(t, x)
			_, err := x.provider.ApplyVersion(ctx, budgetRetirementVersion, true)
			require.Error(t, err)
			require.Equal(t, before, legacyMaintenanceHistory(t, x))
			var state string
			require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_sources WHERE source_id=?`, x.deferred.ID).Row().Scan(&state))
			require.Equal(t, "budget_deferred", state)
		})
	}
}

func TestOntologyMaintenanceBudgetRetirementExpiredWindowActivatesImmediately(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	migrator := ontologyMigrationProvider(t, f.admin)
	_, err := migrator.ApplyVersion(ctx, budgetRetirementVersion, false)
	require.NoError(t, err)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	canonical := f.canonicalSnapshot(t)
	legacyID := uuid.NewString()
	legacyEnd := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO ontology_maintenance_windows(window_id,starts_at,ends_at,policy)
		 VALUES(?::uuid,?,?,?::jsonb)`, legacyID, legacyEnd.Add(-12*time.Hour), legacyEnd,
			`{"enabled":true,"cadence_hours":12,"start_time_local":"03:00","timezone":"UTC","model":"fixture-model","max_concurrency":1,"input_tokens":250000,"output_tokens":100000}`).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE ontology_maintenance_state SET paused=true WHERE singleton`).Error
	}))
	config := maintenanceSettings(t, f)
	anchor := legacyEnd.Add(-6 * time.Hour)
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{
		domain.AppConfigOntologyCadenceHours: "24", domain.AppConfigOntologyStartTime: anchor.Format("15:04"),
	}, "control", "", "")
	require.NoError(t, err)
	var historical string
	require.NoError(t, f.admin.Raw(`SELECT to_jsonb(row)::text FROM ontology_maintenance_windows AS row WHERE window_id=?::uuid`, legacyID).Row().Scan(&historical))
	_, err = migrator.ApplyVersion(ctx, budgetRetirementVersion, true)
	require.NoError(t, err)
	f.store = NewStore(f.app, f.rls)
	_, err = f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "resume", OperationKey: uuid.NewString()}, time.Now(), nil)
	require.NoError(t, err)
	policy, err := config.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	now := time.Now().UTC()
	window, err := f.store.EnsureMaintenanceWindow(ctx, policy, now)
	require.NoError(t, err)
	require.NotNil(t, window)
	require.NotEqual(t, legacyID, window.ID)
	require.Equal(t, now, window.StartsAt)
	require.Equal(t, anchor.Add(24*time.Hour), window.EndsAt)
	require.Equal(t, policy, window.Policy)
	service, calls := maintenanceServiceFixture(t, f, config, nil)
	drainMaintenance(t, service)
	require.Positive(t, calls())
	status, err := service.Status(ctx)
	require.NoError(t, err)
	require.Positive(t, status.Counts.Organized)
	var after string
	require.NoError(t, f.admin.Raw(`SELECT to_jsonb(row)::text FROM ontology_maintenance_windows AS row WHERE window_id=?::uuid`, legacyID).Row().Scan(&after))
	require.Equal(t, historical, after)
	require.Equal(t, canonical, f.canonicalSnapshot(t))
}

func TestOntologyMaintenanceBudgetRetirementRejectsLegacyWriters(t *testing.T) {
	x := legacyMaintenanceFixture(t)
	f, ctx := x.f, context.Background()
	_, err := x.provider.ApplyVersion(ctx, budgetRetirementVersion, true)
	require.NoError(t, err)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE ontology_maintenance_windows SET reserved_input=0 WHERE window_id=?::uuid`, []any{x.window}},
		{`UPDATE ontology_maintenance_windows SET additional_output=1000 WHERE window_id=?::uuid`, []any{x.window}},
		{`UPDATE ontology_maintenance_attempts SET reported_input=1 WHERE batch_id=?::uuid`, []any{x.batch}},
		{`UPDATE ontology_maintenance_runs SET max_batches=1 WHERE run_id=?::uuid`, []any{x.grant}},
		{`INSERT INTO ontology_maintenance_runs(run_id,window_id,operation_key,request_hash,command,kind,status,max_batches,created_at,updated_at)
		 VALUES(gen_random_uuid(),?::uuid,'retired-grant','legacy','{}','allowance','completed',0,clock_timestamp(),clock_timestamp())`, []any{x.window}},
		{`INSERT INTO ontology_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,admitted_at)
		 VALUES(?::uuid,?::uuid,2,?::uuid,10,10,clock_timestamp())`, []any{x.batch, x.assessment, x.window}},
		{`INSERT INTO ontology_maintenance_windows(window_id,starts_at,ends_at,policy)
		 VALUES(gen_random_uuid(),clock_timestamp()+interval '2 days',clock_timestamp()+interval '3 days','{"input_tokens":100,"output_tokens":100}')`, nil},
		{`UPDATE ontology_maintenance_sources SET status='budget_deferred' WHERE source_id=?`, []any{x.deferred.ID}},
	} {
		err := f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error { return tx.Exec(statement.query, statement.args...).Error })
		require.Error(t, err)
	}
	oldRoot := t.TempDir()
	require.NoError(t, filepath.WalkDir("../../../migrations/postgres", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() == "20261023020000_ontology_budget_retirement.sql" {
			return nil
		}
		relative, err := filepath.Rel("../../../migrations/postgres", path)
		if err != nil {
			return err
		}
		target := filepath.Join(oldRoot, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0600)
	}))
	db, err := f.app.DB()
	require.NoError(t, err)
	state, err := storage.ClassifyMigrationState(ctx, db, oldRoot)
	require.NoError(t, err)
	require.Equal(t, storage.MigrationStateInvalid, state.Kind)
	require.Equal(t, "database migration is newer than this binary", state.Reason)
	_, err = f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "allowance", OperationKey: uuid.NewString()}, time.Now(), nil)
	require.ErrorIs(t, err, ontology.ErrInvalid)
}
