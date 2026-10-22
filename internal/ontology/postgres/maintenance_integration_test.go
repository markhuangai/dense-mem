//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceGlobalAccounting(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas stores PostgreSQL data.", nil)
	config := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, config)
	turn, claim := maintenanceClaim(t, f, window)
	ctx := context.Background()
	assessment := uuid.NewString()
	second := NewStore(f.app, f.rls)
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	for _, store := range []*Store{f.store, second} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errs <- store.ReserveMaintenanceAttempt(ctx, *claim, assessment, 1, 100, 200, time.Now().UTC())
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	status, err := second.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 100, status.Window.ChargedInput)
	require.EqualValues(t, 200, status.Window.ChargedOutput)
	usage := ontology.AssessmentAttempt{Number: 1, ReportedInputTokens: 80, ReportedOutputTokens: 40, ReportedTotalTokens: 120}
	require.NoError(t, second.ReconcileMaintenanceAttempt(ctx, *claim, assessment, usage))
	require.NoError(t, f.store.ReconcileMaintenanceAttempt(ctx, *claim, assessment, usage))
	usage.ReportedInputTokens++
	require.ErrorIs(t, f.store.ReconcileMaintenanceAttempt(ctx, *claim, assessment, usage), ontology.ErrConflict)
	require.NoError(t, f.store.ReserveMaintenanceAttempt(ctx, *claim, assessment, 2, 110, 200, time.Now().UTC()))
	require.NoError(t, second.ReconcileMaintenanceAttempt(ctx, *claim, assessment, ontology.AssessmentAttempt{Number: 2, ReportedInputTokens: 90}))
	status, err = second.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 170, status.Window.ChargedInput)
	require.EqualValues(t, 240, status.Window.ChargedOutput)
	require.EqualValues(t, 200, status.Window.ReservedOutput)
	require.NoError(t, second.ReserveMaintenanceAttempt(ctx, *claim, assessment, 3, 100, 100, time.Now().UTC()))
	require.NoError(t, second.ReconcileMaintenanceAttempt(ctx, *claim, assessment, ontology.AssessmentAttempt{Number: 3, ReportedInputTokens: 120, ReportedOutputTokens: 150, ReportedTotalTokens: 270}))
	status, err = second.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, status.Window.Overrun)
	require.ErrorIs(t, second.ReserveMaintenanceAttempt(ctx, *claim, uuid.NewString(), 1, 1, 1, time.Now().UTC()), ontology.ErrBudgetDeferred)
	require.NoError(t, second.ReleaseMaintenanceTurn(ctx, *turn))
}

func TestOntologyMaintenanceAllowancePreservesAccountingAndWindowFences(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas stores PostgreSQL data.", nil)
	ctx := context.Background()
	config := maintenanceSettings(t, f)
	_, err := config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyInputTokens: "100", domain.AppConfigOntologyOutputTokens: "100"}, "control", "", "")
	require.NoError(t, err)
	window := maintenanceWindow(t, f, config)
	turn, claim := maintenanceClaim(t, f, window)
	assessment := uuid.NewString()
	require.NoError(t, f.store.ReserveMaintenanceAttempt(ctx, *claim, assessment, 1, 80, 80, time.Now().UTC()))
	require.ErrorIs(t, f.store.ReserveMaintenanceAttempt(ctx, *claim, uuid.NewString(), 1, 30, 30, time.Now().UTC()), ontology.ErrBudgetDeferred)
	command := domain.OntologyMaintenanceCommand{Action: "allowance", OperationKey: "same-grant", WindowID: window.ID, InputTokens: 50, OutputTokens: 50}
	errs := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := NewStore(f.app, f.rls).MaintenanceCommand(ctx, command, time.Now().UTC(), nil)
			errs <- err
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	status, err := f.store.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, window.Policy, status.Window.Policy)
	require.True(t, window.EndsAt.Equal(status.Window.EndsAt))
	require.EqualValues(t, 50, status.Window.AdditionalInput)
	require.EqualValues(t, 50, status.Window.AdditionalOutput)
	require.EqualValues(t, 80, status.Window.ChargedInput)
	require.EqualValues(t, 80, status.Window.ReservedOutput)
	grant, err := f.store.MaintenanceCommand(ctx, command, time.Now().UTC(), nil)
	require.NoError(t, err)
	require.Equal(t, "completed", grant.Status)
	require.EqualValues(t, 50, grant.OutputTokens)
	changed := command
	changed.OutputTokens++
	_, err = f.store.MaintenanceCommand(ctx, changed, time.Now().UTC(), nil)
	require.ErrorIs(t, err, ontology.ErrConflict)
	changed = command
	changed.OperationKey, changed.InputTokens, changed.OutputTokens = "aggregate-bound", ontology.MaxAdditionalTokens, 0
	_, err = f.store.MaintenanceCommand(ctx, changed, time.Now().UTC(), nil)
	require.ErrorIs(t, err, ontology.ErrInvalid)
	changed = command
	changed.OperationKey, changed.WindowID = "stale-window", uuid.NewString()
	_, err = f.store.MaintenanceCommand(ctx, changed, time.Now().UTC(), nil)
	require.ErrorIs(t, err, ontology.ErrConflict)
	_, err = f.store.MaintenanceCommand(f.actor(0, "manager"), command, time.Now().UTC(), nil)
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	errs = make(chan error, 2)
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errs <- NewStore(f.app, f.rls).ReserveMaintenanceAttempt(ctx, *claim, uuid.NewString(), 1, 40, 40, time.Now().UTC())
		}()
	}
	workers.Wait()
	close(errs)
	accepted, deferred := 0, 0
	for err := range errs {
		if err == nil {
			accepted++
		} else {
			require.ErrorIs(t, err, ontology.ErrBudgetDeferred)
			deferred++
		}
	}
	require.Equal(t, 1, accepted)
	require.Equal(t, 1, deferred)
	for _, query := range []string{`UPDATE ontology_maintenance_runs SET max_batches=1 WHERE run_id=?::uuid`, `DELETE FROM ontology_maintenance_runs WHERE run_id=?::uuid`} {
		err := f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error { return tx.Exec(query, grant.ID).Error })
		require.ErrorContains(t, err, "ontology allowance history is immutable")
	}
	require.NoError(t, f.store.ReconcileMaintenanceAttempt(ctx, *claim, assessment, ontology.AssessmentAttempt{Number: 1, ReportedInputTokens: 81, ReportedOutputTokens: 80}))
	changed = command
	changed.OperationKey = "cannot-clear-overrun"
	_, err = f.store.MaintenanceCommand(ctx, changed, time.Now().UTC(), nil)
	require.ErrorIs(t, err, ontology.ErrBudgetDeferred)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	policy, err := config.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	next, err := f.store.EnsureMaintenanceWindow(ctx, policy, window.EndsAt)
	require.NoError(t, err)
	require.Zero(t, next.AdditionalInput)
	require.Zero(t, next.AdditionalOutput)
	_, err = f.store.MaintenanceCommand(ctx, changed, window.EndsAt, nil)
	require.ErrorIs(t, err, ontology.ErrConflict)
	replayed, err := f.store.MaintenanceCommand(ctx, command, window.EndsAt, nil)
	require.NoError(t, err)
	require.Equal(t, grant, replayed)
}

func TestOntologyMaintenanceAllowanceMigrationRetainsGrantHistory(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	const filename = "20261016020100_ontology_maintenance_allowances.sql"
	contents, err := os.ReadFile(filepath.Join("../../../migrations/postgres/v2_6", filename))
	require.NoError(t, err)
	db, err := f.admin.DB()
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fstest.MapFS{filename: &fstest.MapFile{Data: contents}}, goose.WithDisableGlobalRegistry(true), goose.WithAllowOutofOrder(true))
	require.NoError(t, err)
	_, err = provider.ApplyVersion(ctx, 20261016020100, false)
	require.NoError(t, err)
	require.NoError(t, f.admin.Exec(`GRANT CREATE ON SCHEMA public TO ontology_app;
	 ALTER TABLE ontology_maintenance_windows OWNER TO ontology_app;
	 ALTER TABLE ontology_maintenance_runs OWNER TO ontology_app`).Error)
	db, err = f.app.DB()
	require.NoError(t, err)
	provider, err = goose.NewProvider(goose.DialectPostgres, db, fstest.MapFS{filename: &fstest.MapFile{Data: contents}}, goose.WithDisableGlobalRegistry(true), goose.WithAllowOutofOrder(true))
	require.NoError(t, err)
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	_, err = f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "allowance", OperationKey: "retained-grant", WindowID: window.ID, OutputTokens: 100}, time.Now().UTC(), nil)
	require.NoError(t, err)
	_, err = provider.ApplyVersion(ctx, 20261016020100, false)
	require.ErrorContains(t, err, "ontology allowance history is populated")
	status, err := f.store.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 100, status.Window.AdditionalOutput)
}

func TestOntologyMaintenanceAllowanceRacesDeferredCompletion(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas stores PostgreSQL data.", nil)
	ctx := context.Background()
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	turn, claim := maintenanceClaim(t, f, window)
	_, err := f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "allowance", OperationKey: "grant-before-completion", WindowID: window.ID, OutputTokens: 100}, time.Now().UTC(), nil)
	require.NoError(t, err)
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, "budget_deferred", time.Now().UTC()))
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	status, err := f.store.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Pending)
	turn, claim = maintenanceClaim(t, f, status.Window)
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, "budget_deferred", time.Now().UTC()))
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	status, err = f.store.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.BudgetDeferred)
	require.Zero(t, status.Counts.Pending)
}

func TestOntologyMaintenanceEnrollsActiveTeamsDespiteRetiredOptouts(t *testing.T) {
	f := newOrganizationFixture(t)
	maintenanceEntity(t, f, "Existing active team")
	ctx := context.Background()
	config := maintenanceSettings(t, f)
	_, err := config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.admin, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE teams SET config='{"dreaming":{"enabled":false}}' WHERE id=?::uuid`, f.team).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE app_config SET value='true' WHERE key IN ('DREAMING_ENABLED','DREAMING_FORCE_ENABLED','COMMUNITY_DETECTION_ENABLED')`).Error
	}))
	policy, err := config.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	require.False(t, policy.Enabled, "retired enable flags cannot activate ontology")
	window, err := f.store.EnsureMaintenanceWindow(ctx, policy, time.Now().UTC())
	require.NoError(t, err)
	require.Nil(t, window)
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "true"}, "control", "", "")
	require.NoError(t, err)
	window = maintenanceWindow(t, f, config)
	newTeam := maintenanceOtherTeam(t, f)
	maintenanceEntity(t, newTeam, "Newly active team")
	require.NoError(t, f.rls.WithSystemTx(ctx, f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE teams SET config='{"dreaming":{"enabled":false}}' WHERE id=?::uuid`, newTeam.team).Error
	}))
	seen := map[string]bool{}
	for range 12 {
		turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
		require.NoError(t, err)
		if turn == nil {
			break
		}
		for range 6 {
			require.NoError(t, f.store.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize))
		}
		seen[turn.TeamID] = true
		require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
		if seen[f.team] && seen[newTeam.team] {
			break
		}
	}
	require.True(t, seen[f.team])
	require.True(t, seen[newTeam.team])
}

func TestOntologyMaintenancePauseCommandsAndWindows(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	config := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, config)
	ctx, now := context.Background(), time.Now().UTC()
	turn, claim := maintenanceClaim(t, f, window)
	assessment := uuid.NewString()
	require.NoError(t, f.store.ReserveMaintenanceAttempt(ctx, *claim, assessment, 1, 100, 100, now))
	command := domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: "durable-pause"}
	paused, err := f.store.MaintenanceCommand(ctx, command, now, nil)
	require.NoError(t, err)
	restarted := NewStore(f.app, f.rls)
	replay, err := restarted.MaintenanceCommand(ctx, command, now, nil)
	require.NoError(t, err)
	require.Equal(t, paused, replay)
	command.Action = "resume"
	_, err = restarted.MaintenanceCommand(ctx, command, now, nil)
	require.ErrorIs(t, err, ontology.ErrConflict)
	require.ErrorIs(t, restarted.ReserveMaintenanceAttempt(ctx, *claim, assessment, 2, 100, 100, now), ontology.ErrMaintenancePaused)
	require.NoError(t, restarted.ReconcileMaintenanceAttempt(ctx, *claim, assessment, ontology.AssessmentAttempt{Number: 1, ReportedInputTokens: 50, ReportedOutputTokens: 50}))
	_, err = restarted.SeedDefinitions(ontology.WithMaintenanceClaim(ctx, *claim), f.team, ontology.SeedInput{OperationKey: "drained-seed", Limit: 20})
	require.NoError(t, err, "already admitted work may publish while paused")
	_, err = restarted.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "resume", OperationKey: "durable-resume"}, now, nil)
	require.NoError(t, err)
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyInputTokens: "300000", domain.AppConfigOntologyModel: "next-model", domain.AppConfigOntologyConcurrency: "2"}, "control", "", "")
	require.NoError(t, err)
	policy, err := config.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	unchanged, err := restarted.EnsureMaintenanceWindow(ctx, policy, now)
	require.NoError(t, err)
	require.Equal(t, window.Policy, unchanged.Policy)
	next, err := restarted.EnsureMaintenanceWindow(ctx, policy, window.EndsAt)
	require.NoError(t, err)
	require.NotEqual(t, window.ID, next.ID)
	require.EqualValues(t, 300000, next.Policy.InputTokens)
	require.Equal(t, "next-model", next.Policy.Model)
	require.Zero(t, next.ChargedInput)
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
	require.NoError(t, err)
	require.ErrorIs(t, restarted.ReserveMaintenanceAttempt(ctx, *claim, assessment, 2, 100, 100, now), ontology.ErrMaintenanceDisabled)
	require.NoError(t, restarted.ReleaseMaintenanceTurn(ctx, *turn))
}

func TestOntologyMaintenanceExpiredLeaseAndNewInvalidation(t *testing.T) {
	f := newOrganizationFixture(t)
	source := maintenanceEntity(t, f, "Atlas")
	config := maintenanceSettings(t, f)
	_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyConcurrency: "2", domain.AppConfigOntologyTeamConcurrency: "2"}, "control", "", "")
	require.NoError(t, err)
	window := maintenanceWindow(t, f, config)
	turn, claim := maintenanceClaim(t, f, window)
	ctx := context.Background()
	require.NoError(t, f.rls.WithSystemTx(ctx, f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, claim.ID).Error
	}))
	_, err = f.store.SeedDefinitions(ontology.WithMaintenanceClaim(ctx, *claim), f.team, ontology.SeedInput{OperationKey: "expired-seed", Limit: 20})
	require.ErrorIs(t, err, ontology.ErrLeaseLost)
	require.ErrorIs(t, f.store.ReserveMaintenanceAttempt(ctx, *claim, uuid.NewString(), 1, 100, 100, time.Now().UTC()), ontology.ErrLeaseLost)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	turn, claim = maintenanceClaim(t, f, window)
	require.NoError(t, f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE entity_records SET identity_context=identity_context||'{"revision":"changed"}'::jsonb,version=version+1 WHERE team_id=?::uuid AND entity_id=?::uuid`, f.team, source.ID).Error
	}))
	discovery, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, discovery)
	require.NoError(t, f.store.DiscoverMaintenance(ctx, *discovery, ontology.MaintenancePageSize))
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *discovery))
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{Current: true, Outcomes: []ontology.OrganizationOutcome{{Source: source, Status: "organized"}}}, "", time.Now().UTC()))
	status, err := f.store.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Pending, "completion cannot erase a newer source revision")
	require.Zero(t, status.Counts.Organized)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
}

func TestOntologyMaintenancePopulatedMigrationCannotRollback(t *testing.T) {
	f := newOrganizationFixture(t)
	maintenanceEntity(t, f, "Preserved canonical entity")
	before := f.canonicalSnapshot(t)
	migrator := ontologyMigrationProvider(t, f.admin)
	require.NoError(t, rollbackOntologySchemaTo(t, f.admin, 20261006010001))
	_, err := migrator.ApplyVersion(context.Background(), 20261006010001, false)
	require.ErrorContains(t, err, "cannot roll back populated ontology maintenance")
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyMaintenanceMarkerRollbackAndRLS(t *testing.T) {
	f := newOrganizationFixture(t)
	source := maintenanceEntity(t, f, "Rollback entity")
	config := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, config)
	turn, claim := maintenanceClaim(t, f, window)
	ctx := context.Background()
	var before, after int64
	require.NoError(t, f.admin.Raw(`SELECT COALESCE(max(marker_sequence),0) FROM ontology_maintenance_markers WHERE team_id=?::uuid`, f.team).Row().Scan(&before))
	abort := errors.New("abort authoritative transaction")
	err := f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE entity_records SET identity_context=identity_context||'{"rollback":true}'::jsonb,version=version+1 WHERE team_id=?::uuid AND entity_id=?::uuid`, f.team, source.ID).Error; err != nil {
			return err
		}
		return abort
	})
	require.ErrorIs(t, err, abort)
	require.NoError(t, f.admin.Raw(`SELECT COALESCE(max(marker_sequence),0) FROM ontology_maintenance_markers WHERE team_id=?::uuid`, f.team).Row().Scan(&after))
	require.Equal(t, before, after)
	_, err = f.store.MaintenanceStatus(f.actor(0, "manager"), time.Now().UTC())
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	var count int
	require.NoError(t, f.rls.WithTeamTx(ctx, f.app, uuid.NewString(), func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_kind=? AND source_id=?`, f.team, source.Kind, source.ID).Row().Scan(&count)
	}))
	require.Zero(t, count)
	require.NoError(t, f.rls.WithTeamTx(f.actor(1, "member"), f.app, f.team, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_kind=? AND source_id=?`, f.team, source.Kind, source.ID).Row().Scan(&count)
	}))
	require.Equal(t, 1, count)
	claim.TeamID = uuid.NewString()
	_, err = f.store.SeedDefinitions(ontology.WithMaintenanceClaim(ctx, *claim), f.team, ontology.SeedInput{OperationKey: "cross-team-seed", Limit: 20})
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
}
