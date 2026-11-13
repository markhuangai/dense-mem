//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
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

func TestOntologyEvidenceOnlyResetRebuildsExistingEvidence(t *testing.T) {
	f := newHistoricalOntologyFixture(t)
	other := maintenanceOtherTeam(t, f)
	ctx := context.Background()
	old := f.organizationEvidence(t, 0, "Historical generation uses PostgreSQL.", nil)
	peer := other.organizationEvidence(t, 0, "Actor C uses PostgreSQL.", nil)
	config := maintenanceSettings(t, f)
	service, calls := maintenanceServiceFixture(t, f, config, nil)
	drainMaintenance(t, service)
	require.Positive(t, calls())
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.team, f.space).Error
	}))
	f.generation++
	shared := []ontology.SourceHandle{
		f.organizationEvidence(t, 0, "Actor A uses PostgreSQL.", nil),
		f.organizationEvidence(t, 1, "Actor B uses PostgreSQL.", nil),
	}
	credential := f.credentials[0]
	private, err := f.knowledge.CreateIngestForTest(ctx, knowledge.CreateIngestInput{
		TeamID: f.team, OwnerProfileID: f.owners[0], SpaceID: credential.MemorySpaceID.String(), SpaceGeneration: credential.MemorySpaceGeneration,
		IdempotencyKey: uuid.NewString(), RequestHash: testHash("private reset Evidence"), Evidence: []knowledge.EvidenceInput{{Content: "Private source must never enter the rebuilt shared ontology."}},
	})
	require.NoError(t, err)
	drainMaintenance(t, service)
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_state SET paused=true`).Error
	}))
	unfinished := uuid.NewString()
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		result := tx.Exec(`INSERT INTO ontology_maintenance_batches
 SELECT (jsonb_populate_record(NULL::ontology_maintenance_batches,to_jsonb(batch)||jsonb_build_object(
 'batch_id',?::uuid,'status','running','failure_code','','completed_at',NULL,'lease_token',gen_random_uuid(),
 'lease_until',clock_timestamp()+interval '1 hour'))).*
 FROM ontology_maintenance_batches batch WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND status='completed'
 ORDER BY created_at DESC LIMIT 1`, unfinished, f.team, f.space, f.generation)
		if result.Error != nil {
			return result.Error
		}
		require.EqualValues(t, 1, result.RowsAffected)
		attempt := tx.Exec(`INSERT INTO ontology_maintenance_attempts(batch_id,assessment_id,attempt,window_id,
 reserved_input,reserved_output,estimated_input_tokens,max_output_tokens,admitted_at)
 SELECT batch_id,gen_random_uuid(),1,window_id,0,0,11,17,clock_timestamp()
 FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, unfinished)
		if attempt.Error != nil {
			return attempt.Error
		}
		require.EqualValues(t, 1, attempt.RowsAffected)
		return tx.Exec(`UPDATE ontology_maintenance_attempts SET reported_input=3,reported_output=5,reported_total=8,
 reconciled_at=clock_timestamp() WHERE batch_id=?::uuid`, unfinished).Error
	}))
	before, otherBefore := f.canonicalSnapshot(t), other.canonicalSnapshot(t)
	history := func() map[string]string {
		result := map[string]string{}
		for _, table := range []string{"ontology_maintenance_state", "ontology_maintenance_windows", "ontology_maintenance_runs", "ontology_maintenance_attempts"} {
			var body string
			require.NoError(t, f.admin.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY to_jsonb(row_value)::text)::text,'[]') FROM `+table+` row_value`).Row().Scan(&body))
			result[table] = body
		}
		var settings string
		require.NoError(t, f.admin.Raw(`SELECT jsonb_agg(to_jsonb(row_value) ORDER BY key)::text FROM app_config row_value WHERE key LIKE 'ONTOLOGY_%'`).Row().Scan(&settings))
		result["settings"] = settings
		var batches string
		require.NoError(t, f.admin.Raw(`SELECT jsonb_agg(to_jsonb(row_value)-ARRAY['sources','revisions','status','failure_code','completed_at'] ORDER BY batch_id)::text FROM ontology_maintenance_batches row_value`).Row().Scan(&batches))
		result["batch_identity"] = batches
		return result
	}
	beforeHistory, beforeCalls := history(), calls()
	var oldRecords int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_record_revisions`).Row().Scan(&oldRecords))
	require.Positive(t, oldRecords)
	migrator, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(ctx))
	require.Equal(t, beforeCalls, calls(), "the migration must not call a provider")
	require.Equal(t, beforeHistory, history())
	var retired, completed int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FILTER(WHERE batch_id=?::uuid AND status='lost' AND failure_code='retired_contract' AND completed_at IS NOT NULL),
 count(*) FILTER(WHERE batch_id<>?::uuid AND status='completed' AND completed_at IS NOT NULL)
 FROM ontology_maintenance_batches`, unfinished, unfinished).Row().Scan(&retired, &completed))
	require.Equal(t, 1, retired)
	var batchIdentities []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(beforeHistory["batch_identity"]), &batchIdentities))
	require.Greater(t, len(batchIdentities), 1)
	require.Equal(t, len(batchIdentities)-1, completed)
	require.Equal(t, before, f.canonicalSnapshot(t))
	require.Equal(t, otherBefore, other.canonicalSnapshot(t))
	for _, table := range []string{"ontology_catalog_heads", "ontology_publications", "ontology_record_heads", "ontology_record_revisions", "ontology_source_dependencies", "ontology_revision_dependencies", "ontology_assessments", "ontology_draft_heads", "ontology_draft_revisions", "ontology_maintenance_sources", "ontology_maintenance_markers"} {
		var count int
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM `+table).Row().Scan(&count))
		require.Zero(t, count, table)
	}
	var staleCursors int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_teams WHERE discovery_kind<>0 OR discovery_cursor<>'' OR lease_token IS NOT NULL OR lease_until IS NOT NULL`).Row().Scan(&staleCursors))
	require.Zero(t, staleCursors)
	window := maintenanceWindow(t, f, config)
	_, err = f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.ErrorIs(t, err, ontology.ErrMaintenancePaused)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_state SET paused=false`).Error
	}))
	_, err = f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.ErrorIs(t, err, ontology.ErrMaintenanceDisabled)
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "true"}, "control", "", "")
	require.NoError(t, err)
	drainMaintenance(t, service)
	require.Greater(t, calls(), beforeCalls)
	for _, item := range []struct {
		fixture *ontologyFixture
		source  ontology.SourceHandle
	}{{f, shared[0]}, {f, shared[1]}, {other, peer}} {
		var count int
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_id=? AND eligible AND status='organized'`, item.fixture.team, item.fixture.space, item.fixture.generation, item.source.ID).Row().Scan(&count))
		require.Equal(t, 1, count)
		view, err := item.fixture.store.ListRecords(item.fixture.actor(0, "member"), item.fixture.team, ontology.Topic, "", 20)
		require.NoError(t, err)
		require.NotEmpty(t, view.Records)
	}
	var excluded, leaked int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE source_id IN (?,?)`, old.ID, private.Evidence[0].FragmentID).Row().Scan(&excluded))
	require.Zero(t, excluded)
	require.NoError(t, f.rls.WithTeamTx(other.actor(0, "member"), f.app, other.team, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_record_heads WHERE team_id=?::uuid`, f.team).Row().Scan(&leaked)
	}))
	require.Zero(t, leaked)
	var rebuilt int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_record_revisions`).Row().Scan(&rebuilt))
	require.Positive(t, rebuilt)
	require.NoError(t, migrator.RunUp(ctx))
	var afterRestart int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_record_revisions`).Row().Scan(&afterRestart))
	require.Equal(t, rebuilt, afterRestart)
	require.Equal(t, before, f.canonicalSnapshot(t))
	require.Equal(t, otherBefore, other.canonicalSnapshot(t))
}

func legacyMaintenanceFixture(t *testing.T) legacyMaintenance {
	t.Helper()
	f := newHistoricalOntologyFixture(t)
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
	f := newHistoricalOntologyFixture(t)
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
		if entry.IsDir() || strings.SplitN(entry.Name(), "_", 2)[0] >= "20261023020000" {
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
