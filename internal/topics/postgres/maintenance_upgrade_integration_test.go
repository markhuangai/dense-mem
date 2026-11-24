//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	organization "github.com/markhuangai/dense-mem/internal/topics"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setHistoricalMaintenanceEnabled(t *testing.T, f *ontologyFixture, enabled bool) {
	t.Helper()
	require.NoError(t, f.admin.Exec(`INSERT INTO app_config(key,value) VALUES('ONTOLOGY_MAINTENANCE_ENABLED',?::boolean::text)
		ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, enabled).Error)
}

func historicalCommand(ctx context.Context, f *ontologyFixture, input domain.TopicMaintenanceCommand) (result topic.MaintenanceRun, err error) {
	input, err = topic.PrepareMaintenanceCommand(input)
	if err != nil {
		return result, err
	}
	err = f.store.withMaintenanceUpgradeSchema(ctx, func(tx *gorm.DB) error {
		var err error
		result, err = maintenanceCommandTx(ctx, tx, input, time.Now(), nil)
		return err
	})
	return result, err
}

func seedHistoricalAssessment(t *testing.T, f *ontologyFixture, handle topic.SourceHandle) topic.OrganizationReceipt {
	t.Helper()
	id := uuid.NewString()
	model := assessment.NewProvider(nil, "historical-fixture-model", assessor.DefaultSemanticAssessmentLimits(), 300000).Identity()
	input := topic.OrganizationInput{OperationKey: "equivalence:historical-assessment", Sources: []topic.SourceHandle{handle}}
	inputHash, err := topic.OrganizationInputHash(input, model)
	require.NoError(t, err)
	sources := []topic.SourceDependency{f.source(t, handle)}
	batchHash, err := topic.OrganizationBatchHash(sources, model)
	require.NoError(t, err)
	contextHash, err := topic.OrganizationContextHash(topic.OrganizationContext{})
	require.NoError(t, err)
	receipt := topic.OrganizationReceipt{ID: id, OperationKey: input.OperationKey, InputHash: inputHash, BatchHash: batchHash, ContextHash: contextHash, ProviderIdentity: model, Sources: sources,
		Result: topic.OrganizationResult{AssessmentID: id, Current: true, Outcomes: []topic.OrganizationOutcome{{Source: handle, Status: "unchanged"}}}}
	require.NoError(t, topic.ValidateOrganizationReceipt(receipt))
	body, err := json.Marshal(receipt)
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.app, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO ontology_assessments(team_id,shared_space_id,space_generation,operation_key,assessment_id,input_hash,batch_hash,body) VALUES(?,?,?,?,?,?,?,?::jsonb)`,
			f.team, f.space, f.generation, receipt.OperationKey, id, inputHash, batchHash, string(body)).Error
	}))
	return receipt
}

func upgradeService(f *ontologyFixture) *organization.MaintenanceUpgradeService {
	return organization.NewMaintenanceUpgradeService(f.store, auditapp.New(auditpostgres.NewStore(f.app, f.rls)))
}

func upgradeInterruptedBatch(t *testing.T, x legacyMaintenance, lease time.Duration) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, x.f.admin.Exec(`INSERT INTO ontology_maintenance_batches
	 (batch_id,run_id,window_id,team_id,shared_space_id,space_generation,lease_token,lease_until,sources,revisions,status,created_at)
	 SELECT ?::uuid,run_id,window_id,team_id,shared_space_id,space_generation,?::uuid,clock_timestamp()+? * interval '1 second',sources,revisions,'running',clock_timestamp()
	 FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, id, uuid.NewString(), lease.Seconds(), x.batch).Error)
	return id
}

func upgradeSnapshot(t *testing.T, x legacyMaintenance) map[string]string {
	t.Helper()
	result := legacyMaintenanceHistory(t, x)
	for name, query := range map[string]string{
		"completed_batch": `SELECT to_jsonb(row)::text FROM ontology_maintenance_batches AS row WHERE batch_id=?::uuid`,
		"assessments":     `SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY operation_key)::text,'[]') FROM ontology_assessments AS row WHERE team_id=(SELECT team_id FROM ontology_maintenance_batches WHERE batch_id=?::uuid)`,
	} {
		var body string
		require.NoError(t, x.f.admin.Raw(query, x.batch).Row().Scan(&body))
		result[name] = body
	}
	return result
}

func TestOntologyMaintenanceUpgradePreservesHistoryAndAdmission(t *testing.T) {
	for _, mode := range []string{"enabled", "paused", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			x := legacyMaintenanceFixture(t)
			f, ctx := x.f, context.Background()
			setHistoricalMaintenanceEnabled(t, f, true)
			seedHistoricalAssessment(t, f, x.deferred)
			var err error
			if mode == "paused" {
				_, err = historicalCommand(ctx, f, domain.TopicMaintenanceCommand{Action: "pause", OperationKey: "operator-pause"})
				require.NoError(t, err)
			} else if mode == "disabled" {
				require.NoError(t, f.admin.Exec(`UPDATE app_config SET value='false' WHERE key='ONTOLOGY_MAINTENANCE_ENABLED'`).Error)
			}
			batch := upgradeInterruptedBatch(t, x, -time.Minute)
			before, canonical := upgradeSnapshot(t, x), f.canonicalSnapshot(t)
			service := upgradeService(f)
			var report topic.MaintenanceUpgradeState
			require.NoError(t, service.Prepare(ctx, func(_ string, state topic.MaintenanceUpgradeState) { report = state }))
			require.EqualValues(t, 1, report.RecoveredBatches)
			require.EqualValues(t, 1, report.ClosedRuns)
			require.Equal(t, mode == "enabled", report.OwnsPause)
			var status, sourceStatus string
			require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, batch).Row().Scan(&status))
			require.Equal(t, "lost", status)
			require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_sources WHERE source_id=?`, x.deferred.ID).Row().Scan(&sourceStatus))
			require.Equal(t, "budget_deferred", sourceStatus)
			var closed, retired int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_runs WHERE run_id=?::uuid AND status='incomplete' AND failure_code='window_closed'`, x.run).Row().Scan(&closed))
			require.Equal(t, 1, closed)
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM audit_log WHERE operation=? AND entity_id=?`, topic.OntologyBudgetRetirementAuditOperation, x.run).Row().Scan(&retired))
			require.Equal(t, 1, retired)
			require.Equal(t, before, upgradeSnapshot(t, x))
			require.NoError(t, upgradeService(f).Prepare(ctx, nil))
			_, err = service.Complete(ctx)
			require.ErrorIs(t, err, topic.ErrMaintenanceUpgradeBlocked)
			_, err = x.provider.ApplyVersion(ctx, budgetRetirementVersion, true)
			require.NoError(t, err)
			require.NoError(t, upgradeService(f).Prepare(ctx, nil))
			_, err = upgradeService(f).Complete(ctx)
			require.NoError(t, err)
			var paused, enabled bool
			require.NoError(t, f.admin.Raw(`SELECT paused,(SELECT value='true' FROM app_config WHERE key='ONTOLOGY_MAINTENANCE_ENABLED') FROM ontology_maintenance_state`).Row().Scan(&paused, &enabled))
			require.Equal(t, mode == "paused", paused)
			require.Equal(t, mode != "disabled", enabled)
			require.Equal(t, before, upgradeSnapshot(t, x))
			require.Equal(t, canonical, f.canonicalSnapshot(t))
			var audits int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM audit_log WHERE operation=?`, topic.OntologyUpgradeAuditOperation).Row().Scan(&audits))
			if mode == "enabled" {
				require.Equal(t, 2, audits)
			} else {
				require.Zero(t, audits)
			}
			_, err = historicalCommand(ctx, f, domain.TopicMaintenanceCommand{Action: "pause", OperationKey: "later-pause"})
			require.NoError(t, err)
			require.NoError(t, service.Prepare(ctx, nil))
			_, err = service.Complete(ctx)
			require.NoError(t, err)
			require.NoError(t, f.admin.Raw(`SELECT paused FROM ontology_maintenance_state`).Row().Scan(&paused))
			require.True(t, paused)
		})
	}
}

func TestOntologyMaintenanceUpgradeWaitCancellationAndUnreconciledRefusal(t *testing.T) {
	for _, mode := range []string{"batch", "preparation", "unreconciled"} {
		t.Run(mode, func(t *testing.T) {
			x := legacyMaintenanceFixture(t)
			f := x.f
			setHistoricalMaintenanceEnabled(t, f, true)
			batch := upgradeInterruptedBatch(t, x, time.Minute)
			if mode == "preparation" {
				require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, batch).Error)
				require.NoError(t, f.admin.Exec(`INSERT INTO ontology_maintenance_teams(team_id,shared_space_id,space_generation,lease_token,lease_until)
				 VALUES(?::uuid,?::uuid,?,?::uuid,clock_timestamp()+interval '1 minute')`, f.team, f.space, f.generation, uuid.NewString()).Error)
			} else if mode == "unreconciled" {
				require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, batch).Error)
				require.NoError(t, f.admin.Exec(`INSERT INTO ontology_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,admitted_at)
				 VALUES(?::uuid,?::uuid,1,?::uuid,20,30,clock_timestamp())`, batch, uuid.NewString(), x.window).Error)
			}
			before, canonical := upgradeSnapshot(t, x), f.canonicalSnapshot(t)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			err := upgradeService(f).Prepare(ctx, nil)
			require.ErrorIs(t, err, topic.ErrMaintenanceUpgradeBlocked)
			if mode != "unreconciled" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			var status string
			require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, batch).Row().Scan(&status))
			require.Equal(t, "running", status)
			var pending, retired int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_runs WHERE run_id=?::uuid AND status='pending'`, x.run).Row().Scan(&pending))
			require.Equal(t, 1, pending)
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM audit_log WHERE operation=?`, topic.OntologyBudgetRetirementAuditOperation).Row().Scan(&retired))
			require.Zero(t, retired)
			require.Equal(t, before, upgradeSnapshot(t, x))
			require.Equal(t, canonical, f.canonicalSnapshot(t))
			if mode == "unreconciled" {
				var attempts int
				require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_attempts WHERE reconciled_at IS NULL`).Row().Scan(&attempts))
				require.Equal(t, 1, attempts)
				return
			}
			require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, batch).Error)
			require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_teams SET lease_until=clock_timestamp()-interval '1 second'`).Error)
			require.NoError(t, upgradeService(f).Prepare(context.Background(), nil))
		})
	}
}

func TestOntologyMaintenanceUpgradeAuditRollbackAndConcurrentReplay(t *testing.T) {
	x := legacyMaintenanceFixture(t)
	f, ctx := x.f, context.Background()
	setHistoricalMaintenanceEnabled(t, f, true)
	upgradeInterruptedBatch(t, x, -time.Minute)
	require.NoError(t, f.admin.Exec(`CREATE FUNCTION reject_upgrade_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF NEW.operation='ONTOLOGY_UPGRADE_COMMAND' THEN RAISE EXCEPTION 'upgrade audit unavailable'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER reject_upgrade_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_upgrade_audit()`).Error)
	require.ErrorIs(t, upgradeService(f).Prepare(ctx, nil), topic.ErrAuditUnavailable)
	var paused bool
	var commands int
	require.NoError(t, f.admin.Raw(`SELECT paused FROM ontology_maintenance_state`).Row().Scan(&paused))
	require.False(t, paused)
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_runs WHERE operation_key=?`, topic.OntologyUpgradePauseKey).Row().Scan(&commands))
	require.Zero(t, commands)
	require.NoError(t, f.admin.Exec(`DROP TRIGGER reject_upgrade_audit ON audit_log; DROP FUNCTION reject_upgrade_audit()`).Error)
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() { errs <- upgradeService(f).Prepare(ctx, nil) })
	}
	workers.Wait()
	for range 2 {
		require.NoError(t, <-errs)
	}
	_, err := x.provider.ApplyVersion(ctx, budgetRetirementVersion, true)
	require.NoError(t, err)
	for range 2 {
		workers.Go(func() { _, err := upgradeService(f).Complete(ctx); errs <- err })
	}
	workers.Wait()
	for range 2 {
		require.NoError(t, <-errs)
	}
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM audit_log WHERE operation=?`, topic.OntologyUpgradeAuditOperation).Row().Scan(&commands))
	require.Equal(t, 2, commands)
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM audit_log WHERE operation=? AND entity_id=?`, topic.OntologyBudgetRetirementAuditOperation, x.run).Row().Scan(&commands))
	require.Equal(t, 1, commands)
}

func TestOntologyMaintenanceUpgradeRetirementAuditRollback(t *testing.T) {
	x := legacyMaintenanceFixture(t)
	f, ctx := x.f, context.Background()
	setHistoricalMaintenanceEnabled(t, f, true)
	batch := upgradeInterruptedBatch(t, x, -time.Minute)
	before, canonical := upgradeSnapshot(t, x), f.canonicalSnapshot(t)
	require.NoError(t, f.admin.Exec(`CREATE FUNCTION reject_retirement_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF NEW.operation='ONTOLOGY_BUDGET_RETIREMENT' THEN RAISE EXCEPTION 'retirement audit unavailable'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER reject_retirement_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_retirement_audit()`).Error)
	require.ErrorIs(t, upgradeService(f).Prepare(ctx, nil), topic.ErrAuditUnavailable)
	var paused bool
	var status string
	var commands int
	require.NoError(t, f.admin.Raw(`SELECT paused FROM ontology_maintenance_state`).Row().Scan(&paused))
	require.False(t, paused)
	require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, batch).Row().Scan(&status))
	require.Equal(t, "running", status)
	require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_runs WHERE run_id=?::uuid`, x.run).Row().Scan(&status))
	require.Equal(t, "pending", status)
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_runs WHERE operation_key=?`, topic.OntologyUpgradePauseKey).Row().Scan(&commands))
	require.Zero(t, commands)
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM audit_log WHERE operation IN (?,?)`, topic.OntologyUpgradeAuditOperation, topic.OntologyBudgetRetirementAuditOperation).Row().Scan(&commands))
	require.Zero(t, commands)
	require.Equal(t, before, upgradeSnapshot(t, x))
	require.Equal(t, canonical, f.canonicalSnapshot(t))
}

func TestOntologyMaintenanceUpgradeRejectsRequestActorsAndReceiptSpoofing(t *testing.T) {
	x := legacyMaintenanceFixture(t)
	f, ctx := x.f, context.Background()
	other := maintenanceOtherTeam(t, f)
	before := f.canonicalSnapshot(t)
	for _, actor := range []context.Context{f.actor(0, "member"), f.actor(1, "manager"), other.actor(0, "manager")} {
		require.ErrorIs(t, upgradeService(f).Prepare(actor, nil), topic.ErrUnauthorized)
		_, err := f.store.PrepareMaintenanceUpgrade(actor, nil)
		require.ErrorIs(t, err, topic.ErrUnauthorized)
	}
	_, err := historicalCommand(ctx, f, domain.TopicMaintenanceCommand{Action: "pause", OperationKey: topic.OntologyUpgradePauseKey})
	require.NoError(t, err)
	require.ErrorIs(t, upgradeService(f).Prepare(ctx, nil), topic.ErrConflict)
	require.Equal(t, before, f.canonicalSnapshot(t))
	require.NoError(t, f.rls.WithTeamTx(other.actor(0, "manager"), f.app, other.team, func(tx *gorm.DB) error {
		var count int
		if err := tx.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE team_id=?::uuid`, f.team).Row().Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("foreign maintenance batches visible")
		}
		return nil
	}))
}

func TestOntologyMaintenanceUpgradePreservesPrivateAndGenerationFences(t *testing.T) {
	x := legacyMaintenanceFixture(t)
	f, ctx := x.f, context.Background()
	setHistoricalMaintenanceEnabled(t, f, true)
	credential := f.credentials[0]
	privateCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: credential.MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: credential.MemorySpaceGeneration}})
	private, err := f.knowledge.CreateIngestForTest(privateCtx, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: f.owners[0], SpaceID: credential.MemorySpaceID.String(), SpaceGeneration: credential.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "Private upgrade canary."}}})
	require.NoError(t, err)
	other := maintenanceOtherTeam(t, f)
	other.organizationEvidence(t, 0, "Actor C upgrade canary.", nil)
	before, otherBefore := f.canonicalSnapshot(t), other.canonicalSnapshot(t)
	old := upgradeInterruptedBatch(t, x, -time.Minute)
	require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_batches SET space_generation=space_generation+1 WHERE batch_id=?::uuid`, old).Error)
	live := upgradeInterruptedBatch(t, x, time.Minute)
	var token string
	require.NoError(t, f.admin.Raw(`SELECT lease_token::text FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, live).Row().Scan(&token))
	wrong := topic.MaintenanceClaim{ID: live, RunID: x.run, WindowID: x.window, TeamID: f.team, SpaceID: f.space, Generation: f.generation + 1, LeaseToken: token, LeaseUntil: time.Now().Add(time.Minute)}
	require.ErrorIs(t, f.store.withMaintenanceUpgradeSchema(ctx, func(tx *gorm.DB) error {
		phase := ""
		return completeMaintenanceBatch(tx, wrong, topic.OrganizationResult{}, "", time.Now(), &phase)
	}), topic.ErrLeaseLost)
	waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	require.Error(t, upgradeService(f).Prepare(waitCtx, nil))
	var status string
	require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, live).Row().Scan(&status))
	require.Equal(t, "running", status)
	require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, live).Error)
	require.NoError(t, upgradeService(f).Prepare(ctx, nil))
	_, err = x.provider.ApplyVersion(ctx, budgetRetirementVersion, true)
	require.NoError(t, err)
	_, err = upgradeService(f).Complete(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, f.store.withMaintenanceUpgradeSchema(ctx, func(tx *gorm.DB) error {
		phase := ""
		return completeMaintenanceBatch(tx, wrong, topic.OrganizationResult{}, "", time.Now(), &phase)
	}), topic.ErrLeaseLost)
	var privateSources int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE source_id=?`, private.Evidence[0].FragmentID).Row().Scan(&privateSources))
	require.Zero(t, privateSources)
	require.Equal(t, before, f.canonicalSnapshot(t))
	require.Equal(t, otherBefore, other.canonicalSnapshot(t))
}
