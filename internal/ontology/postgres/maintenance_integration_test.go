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
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
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
	paused, err := f.store.MaintenanceCommand(ctx, command, now)
	require.NoError(t, err)
	restarted := NewStore(f.app, f.rls)
	replay, err := restarted.MaintenanceCommand(ctx, command, now)
	require.NoError(t, err)
	require.Equal(t, paused, replay)
	command.Action = "resume"
	_, err = restarted.MaintenanceCommand(ctx, command, now)
	require.ErrorIs(t, err, ontology.ErrConflict)
	require.ErrorIs(t, restarted.ReserveMaintenanceAttempt(ctx, *claim, assessment, 2, 100, 100, now), ontology.ErrMaintenancePaused)
	require.NoError(t, restarted.ReconcileMaintenanceAttempt(ctx, *claim, assessment, ontology.AssessmentAttempt{Number: 1, ReportedInputTokens: 50, ReportedOutputTokens: 50}))
	_, err = restarted.SeedDefinitions(ontology.WithMaintenanceClaim(ctx, *claim), f.team, ontology.SeedInput{OperationKey: "drained-seed", Limit: 20})
	require.NoError(t, err, "already admitted work may publish while paused")
	_, err = restarted.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "resume", OperationKey: "durable-resume"}, now)
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
	window := maintenanceWindow(t, f, config)
	turn, claim := maintenanceClaim(t, f, window)
	ctx := context.Background()
	require.NoError(t, f.rls.WithSystemTx(ctx, f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, claim.ID).Error
	}))
	_, err := f.store.SeedDefinitions(ontology.WithMaintenanceClaim(ctx, *claim), f.team, ontology.SeedInput{OperationKey: "expired-seed", Limit: 20})
	require.ErrorIs(t, err, ontology.ErrLeaseLost)
	require.ErrorIs(t, f.store.ReserveMaintenanceAttempt(ctx, *claim, uuid.NewString(), 1, 100, 100, time.Now().UTC()), ontology.ErrLeaseLost)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	turn, claim = maintenanceClaim(t, f, window)
	require.NoError(t, f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE entity_records SET identity_context=identity_context||'{"revision":"changed"}'::jsonb,version=version+1 WHERE team_id=?::uuid AND entity_id=?::uuid`, f.team, source.ID).Error
	}))
	require.NoError(t, f.store.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize))
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
	migrator, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunDown(context.Background()))
	require.ErrorContains(t, migrator.RunDown(context.Background()), "cannot roll back populated ontology maintenance")
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
