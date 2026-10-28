//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceGlobalAccounting(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas stores PostgreSQL data.", nil)
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
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
			errs <- store.AdmitMaintenanceAttempt(ctx, *claim, assessment, 1, 100, 200, time.Now())
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	status, err := second.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Usage.Attempts)
	require.EqualValues(t, 1, status.Usage.UnreconciledAttempts)
	require.False(t, status.CoverageComplete)
	require.ErrorIs(t, f.store.AdmitMaintenanceAttempt(ctx, *claim, assessment, 1, 101, 200, time.Now()), ontology.ErrConflict)
	usage := ontology.AssessmentAttempt{Number: 1, ReportedInputTokens: 80, ReportedOutputTokens: 40, ReportedTotalTokens: 120}
	require.NoError(t, second.ReconcileMaintenanceAttempt(ctx, *claim, assessment, usage))
	require.NoError(t, f.store.ReconcileMaintenanceAttempt(ctx, *claim, assessment, usage))
	usage.ReportedInputTokens++
	require.ErrorIs(t, f.store.ReconcileMaintenanceAttempt(ctx, *claim, assessment, usage), ontology.ErrConflict)
	require.NoError(t, f.store.AdmitMaintenanceAttempt(ctx, *claim, assessment, 2, 110, 200, time.Now()))
	require.NoError(t, second.ReconcileMaintenanceAttempt(ctx, *claim, assessment, ontology.AssessmentAttempt{Number: 2, ReportedInputTokens: 90}))
	require.NoError(t, second.AdmitMaintenanceAttempt(ctx, *claim, assessment, 3, 100, 100, time.Now()))
	require.NoError(t, second.ReconcileMaintenanceAttempt(ctx, *claim, assessment, ontology.AssessmentAttempt{Number: 3}))
	status, err = second.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 170, status.Window.Usage.ReportedInputTokens)
	require.EqualValues(t, 40, status.Window.Usage.ReportedOutputTokens)
	require.EqualValues(t, 120, status.Window.Usage.ReportedTotalTokens)
	require.EqualValues(t, 1, status.Usage.PartialUsageAttempts)
	require.EqualValues(t, 1, status.Usage.UnavailableUsageAttempts)
	require.Zero(t, status.Usage.UnreconciledAttempts)
	require.NoError(t, second.AdmitMaintenanceAttempt(ctx, *claim, uuid.NewString(), 1, 100000, 65536, time.Now()))
	require.ErrorIs(t, second.AdmitMaintenanceAttempt(ctx, *claim, uuid.NewString(), 1, 300000, 1, time.Now()), ontology.ErrConfiguration)
	var reserved int64
	require.NoError(t, f.admin.Raw(`SELECT sum(reserved_input+reserved_output) FROM ontology_maintenance_attempts`).Row().Scan(&reserved))
	require.Zero(t, reserved)
	require.NoError(t, second.ReleaseMaintenanceTurn(ctx, *turn))
}

func TestOntologyMaintenanceLeaseInterruptionReturnsPending(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas stores PostgreSQL data.", nil)
	ctx := context.Background()
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	turn, claim := maintenanceClaim(t, f, window)
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, "window_closed", time.Now()))
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	status, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Pending)
	_, claim = maintenanceClaim(t, f, status.Window)
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, "lease_lost", time.Now()))
	status, err = f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Pending)
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
			require.NoError(t, f.store.discoverMaintenanceError(ctx, *turn, ontology.MaintenancePageSize))
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
	require.NoError(t, f.store.AdmitMaintenanceAttempt(ctx, *claim, assessment, 1, 100, 100, now))
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
	require.ErrorIs(t, restarted.AdmitMaintenanceAttempt(ctx, *claim, assessment, 2, 100, 100, now), ontology.ErrMaintenancePaused)
	require.NoError(t, restarted.ReconcileMaintenanceAttempt(ctx, *claim, assessment, ontology.AssessmentAttempt{Number: 1, ReportedInputTokens: 50, ReportedOutputTokens: 50}))
	_, err = restarted.SeedDefinitions(ontology.WithMaintenanceClaim(ctx, *claim), f.team, ontology.SeedInput{OperationKey: "drained-seed", Limit: 20})
	require.NoError(t, err, "already admitted work may publish while paused")
	_, err = restarted.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "resume", OperationKey: "durable-resume"}, now, nil)
	require.NoError(t, err)
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyContextWindow: "400000", domain.AppConfigOntologyModel: "next-model", domain.AppConfigOntologyConcurrency: "2"}, "control", "", "")
	require.NoError(t, err)
	policy, err := config.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	unchanged, err := restarted.EnsureMaintenanceWindow(ctx, policy, now)
	require.NoError(t, err)
	require.Equal(t, window.Policy, unchanged.Policy)
	next, err := restarted.EnsureMaintenanceWindow(ctx, policy, window.EndsAt)
	require.NoError(t, err)
	require.NotEqual(t, window.ID, next.ID)
	require.EqualValues(t, 400000, next.Policy.ContextWindowTokens)
	require.Equal(t, "next-model", next.Policy.Model)
	require.Zero(t, next.Usage.ReportedInputTokens)
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
	require.NoError(t, err)
	require.ErrorIs(t, restarted.AdmitMaintenanceAttempt(ctx, *claim, assessment, 2, 100, 100, now), ontology.ErrMaintenanceDisabled)
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
	require.ErrorIs(t, f.store.AdmitMaintenanceAttempt(ctx, *claim, uuid.NewString(), 1, 100, 100, time.Now().UTC()), ontology.ErrLeaseLost)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	turn, claim = maintenanceClaim(t, f, window)
	require.NoError(t, f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE entity_records SET identity_context=identity_context||'{"revision":"changed"}'::jsonb,version=version+1 WHERE team_id=?::uuid AND entity_id=?::uuid`, f.team, source.ID).Error
	}))
	discovery, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, discovery)
	require.NoError(t, f.store.discoverMaintenanceError(ctx, *discovery, ontology.MaintenancePageSize))
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
