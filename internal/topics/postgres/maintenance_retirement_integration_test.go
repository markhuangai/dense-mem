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
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
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
	seedHistoricalRecords(t, f, "first-generation", testTopic("original"))
	seedHistoricalRecords(t, other, "peer-generation", testTopic("peer"))
	staleActor := f.actor(0, "member")
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
	seedHistoricalRecords(t, f, "second-generation", testTopic("replacement"))
	setHistoricalMaintenanceEnabled(t, f, false)
	windowID, runID := uuid.NewString(), uuid.NewString()
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO ontology_maintenance_windows(window_id,starts_at,ends_at,policy) VALUES(?,now()-interval '2 days',now()-interval '1 day','{"model":"fixture-model","context_window_tokens":300000}')`, windowID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_runs(run_id,window_id,operation_key,request_hash,command,kind,status,max_batches,created_at,updated_at)
			VALUES(?,?,'populated-reset','historical','{}','scheduled','completed',0,now()-interval '2 days',now()-interval '1 day')`, runID, windowID).Error; err != nil {
			return err
		}
		for _, source := range shared {
			body, err := json.Marshal([]ontology.SourceHandle{source})
			if err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO ontology_maintenance_batches(batch_id,run_id,window_id,team_id,shared_space_id,space_generation,lease_token,lease_until,sources,revisions,status,created_at,completed_at)
				VALUES(?,?,?,?,?,?,gen_random_uuid(),now()-interval '1 day',?::jsonb,'{}','completed',now()-interval '2 days',now()-interval '1 day')`,
				uuid.NewString(), runID, windowID, f.team, f.space, f.generation, string(body)).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_teams(team_id,shared_space_id,space_generation,discovery_kind,discovery_cursor) VALUES(?,?,?,1,'old-cursor')
			ON CONFLICT(team_id,shared_space_id,space_generation) DO UPDATE SET discovery_cursor=EXCLUDED.discovery_cursor`, f.team, f.space, f.generation).Error; err != nil {
			return err
		}
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
	history := func(prefix string) map[string]string {
		result := map[string]string{}
		for _, name := range []string{"state", "windows", "runs", "attempts"} {
			var body string
			require.NoError(t, f.admin.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY to_jsonb(row_value)::text)::text,'[]') FROM `+prefix+name+` row_value`).Row().Scan(&body))
			result[name] = body
		}
		var settings string
		require.NoError(t, f.admin.Raw(`SELECT COALESCE(jsonb_object_agg(replace(key,'TOPIC_MAINTENANCE_','ONTOLOGY_MAINTENANCE_'),value)::text,'{}') FROM app_config WHERE key LIKE 'ONTOLOGY_MAINTENANCE_%' OR key LIKE 'TOPIC_MAINTENANCE_%'`).Row().Scan(&settings))
		result["settings"] = settings
		var batches string
		require.NoError(t, f.admin.Raw(`SELECT jsonb_agg(to_jsonb(row_value)-ARRAY['sources','revisions','status','failure_code','completed_at','community_topic_id'] ORDER BY batch_id)::text FROM `+prefix+`batches row_value`).Row().Scan(&batches))
		result["batch_identity"] = batches
		return result
	}
	beforeHistory := history("ontology_maintenance_")
	var oldRecords int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_record_revisions`).Row().Scan(&oldRecords))
	require.Positive(t, oldRecords)
	migrator, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(ctx))
	require.Equal(t, beforeHistory, history("topic_maintenance_"))
	var retired, completed, communityBatches int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FILTER(WHERE batch_id=?::uuid AND status='lost' AND failure_code='retired_contract' AND completed_at IS NOT NULL),
 count(*) FILTER(WHERE batch_id<>?::uuid AND status='completed' AND completed_at IS NOT NULL),
 count(*) FILTER(WHERE community_topic_id IS NOT NULL)
 FROM topic_maintenance_batches`, unfinished, unfinished).Row().Scan(&retired, &completed, &communityBatches))
	require.Equal(t, 1, retired)
	require.Zero(t, communityBatches)
	var batchIdentities []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(beforeHistory["batch_identity"]), &batchIdentities))
	require.Greater(t, len(batchIdentities), 1)
	require.Equal(t, len(batchIdentities)-1, completed)
	require.Equal(t, before, f.canonicalSnapshot(t))
	require.Equal(t, otherBefore, other.canonicalSnapshot(t))
	for _, table := range []string{"topic_catalog_heads", "topic_publications", "topic_record_heads", "topic_record_revisions", "topic_source_dependencies", "topic_revision_dependencies", "historical_organization_assessments", "topic_draft_heads", "topic_draft_revisions", "topic_maintenance_sources", "topic_maintenance_markers"} {
		var count int
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM `+table).Row().Scan(&count))
		require.Zero(t, count, table)
	}
	var staleCursors int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_maintenance_teams WHERE discovery_kind<>0 OR discovery_cursor<>'' OR lease_token IS NOT NULL OR lease_until IS NOT NULL`).Row().Scan(&staleCursors))
	require.Zero(t, staleCursors)
	config := maintenanceSettings(t, f)
	_, err = config.UpdateTopicMaintenanceSettings(ctx, map[string]string{domain.AppConfigTopicMaintenanceEnabled: "false"}, "control", "", "")
	require.NoError(t, err)
	service, calls := maintenanceServiceFixture(t, f, config, nil)
	window := maintenanceWindow(t, f, config)
	_, err = f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.ErrorIs(t, err, ontology.ErrMaintenancePaused)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE topic_maintenance_state SET paused=false`).Error
	}))
	_, err = f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.ErrorIs(t, err, ontology.ErrMaintenanceDisabled)
	_, err = config.UpdateTopicMaintenanceSettings(ctx, map[string]string{domain.AppConfigTopicMaintenanceEnabled: "true"}, "control", "", "")
	require.NoError(t, err)
	drainMaintenance(t, service)
	require.Positive(t, calls())
	_, err = f.store.ListRecords(staleActor, f.team, ontology.Topic, "", 20)
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	credentials := access.NewCredentialRepository(f.app, f.rls, nil)
	for index, original := range f.credentials {
		refreshed, err := credentials.GetActiveByID(ctx, original.ID)
		require.NoError(t, err)
		require.NotNil(t, refreshed)
		require.Equal(t, original.ID, refreshed.ID)
		require.Equal(t, original.OwnerID, refreshed.OwnerID)
		require.Equal(t, f.generation, refreshed.TeamSharedSpaceGeneration)
		f.credentials[index] = refreshed
	}
	for _, item := range []struct {
		fixture *ontologyFixture
		source  ontology.SourceHandle
		owner   int
	}{{f, shared[0], 0}, {f, shared[1], 1}, {other, peer, 0}} {
		var count int
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_id=? AND eligible AND status='organized'`, item.fixture.team, item.fixture.space, item.fixture.generation, item.source.ID).Row().Scan(&count))
		require.Equal(t, 1, count)
		view, err := item.fixture.store.ListRecords(item.fixture.actor(item.owner, "member"), item.fixture.team, ontology.Topic, "", 20)
		require.NoError(t, err)
		require.NotEmpty(t, view.Records)
	}
	var excluded, leaked int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_maintenance_sources WHERE source_id IN (?,?)`, old.ID, private.Evidence[0].FragmentID).Row().Scan(&excluded))
	require.Zero(t, excluded)
	require.NoError(t, f.rls.WithTeamTx(other.actor(0, "member"), f.app, other.team, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM topic_record_heads WHERE team_id=?::uuid`, f.team).Row().Scan(&leaked)
	}))
	require.Zero(t, leaked)
	var rebuilt int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_record_revisions`).Row().Scan(&rebuilt))
	require.Positive(t, rebuilt)
	require.NoError(t, migrator.RunUp(ctx))
	var afterRestart int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_record_revisions`).Row().Scan(&afterRestart))
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
		dependency := f.source(t, source)
		require.NoError(t, f.store.withScope(context.Background(), f.team, false, func(tx *gorm.DB, fence scope) error {
			return tx.Exec(`INSERT INTO ontology_maintenance_sources(team_id,shared_space_id,space_generation,source_kind,source_id,source_version,fingerprint,eligible,status,last_run_id)
				VALUES(?,?,?,?,?,?,?,true,?,?)`, fence.TeamID, fence.SpaceID, fence.Generation, source.Kind, source.ID, source.Version, dependency.Fingerprint, []string{"budget_deferred", "failed", "ambiguous"}[i], x.run).Error
		}))
	}
	require.NoError(t, f.admin.Raw(`SELECT pending_at FROM ontology_maintenance_sources WHERE source_id=?`, x.deferred.ID).Row().Scan(&x.pendingAt))
	return x
}

func legacyMaintenanceHistory(t *testing.T, x legacyMaintenance) map[string]string {
	t.Helper()
	return maintenanceHistoryWithPrefix(t, x, "ontology_maintenance_")
}

func maintenanceHistoryWithPrefix(t *testing.T, x legacyMaintenance, prefix string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for name, query := range map[string]string{
		"window":  `SELECT to_jsonb(row)::text FROM ` + prefix + `windows AS row WHERE window_id=?::uuid`,
		"attempt": `SELECT jsonb_agg(to_jsonb(row)-'estimated_input_tokens'-'max_output_tokens' ORDER BY attempt)::text FROM ` + prefix + `attempts AS row WHERE batch_id=?::uuid`,
		"grant":   `SELECT to_jsonb(row)::text FROM ` + prefix + `runs AS row WHERE run_id=?::uuid`,
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
	_, err = x.provider.ApplyVersion(ctx, budgetRetirementVersion, true)
	require.ErrorIs(t, err, goose.ErrAlreadyApplied)
	_, err = x.provider.ApplyVersion(ctx, budgetRetirementVersion, false)
	require.ErrorContains(t, err, "recover by rolling forward")
	current, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.NoError(t, current.RunUp(ctx))
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
	require.Equal(t, before, maintenanceHistoryWithPrefix(t, x, "topic_maintenance_"))

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
	var historical string
	require.NoError(t, f.admin.Raw(`SELECT to_jsonb(row)::text FROM ontology_maintenance_windows AS row WHERE window_id=?::uuid`, legacyID).Row().Scan(&historical))
	_, err = migrator.ApplyVersion(ctx, budgetRetirementVersion, true)
	require.NoError(t, err)
	current, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.NoError(t, current.RunUp(ctx))
	config := maintenanceSettings(t, f)
	anchor := legacyEnd.Add(-6 * time.Hour)
	_, err = config.UpdateTopicMaintenanceSettings(ctx, map[string]string{
		domain.AppConfigTopicMaintenanceCadenceHours: "24", domain.AppConfigTopicMaintenanceStartTime: anchor.Format("15:04"),
	}, "control", "", "")
	require.NoError(t, err)
	f.store = NewStore(f.app, f.rls)
	_, err = f.store.MaintenanceCommand(ctx, domain.TopicMaintenanceCommand{Action: "resume", OperationKey: uuid.NewString()}, time.Now(), nil)
	require.NoError(t, err)
	policy, err := config.TopicMaintenanceRuntimeConfig(ctx)
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
	require.NoError(t, f.admin.Raw(`SELECT to_jsonb(row)::text FROM topic_maintenance_windows AS row WHERE window_id=?::uuid`, legacyID).Row().Scan(&after))
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
	_, err = f.store.MaintenanceCommand(ctx, domain.TopicMaintenanceCommand{Action: "allowance", OperationKey: uuid.NewString()}, time.Now(), nil)
	require.ErrorIs(t, err, ontology.ErrInvalid)
}
