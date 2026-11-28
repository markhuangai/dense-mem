//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceConcurrentAccountingAndCompletionAvoidsGlobalLockStarvation(t *testing.T) {
	f := newOrganizationFixture(t)
	other := maintenanceOtherTeam(t, f)
	for i := range 20 {
		f.organizationEvidence(t, i%len(f.owners), fmt.Sprintf("Team A concurrent sentence %d.", i), nil)
		other.organizationEvidence(t, i%len(other.owners), fmt.Sprintf("Team B concurrent sentence %d.", i), nil)
	}
	config := concurrencyConfig(t, f, 4, 2)
	window := maintenanceWindow(t, f, config)
	ctx := t.Context()

	_, claimA := maintenanceClaim(t, f, window)
	_, claimB := maintenanceClaim(t, f, window)

	claims := []*topic.MaintenanceClaim{claimA, claimB}
	assessmentIDs := []string{uuid.NewString(), uuid.NewString()}
	withHeldLocks := func(stateLock string, run func(context.Context) error) error {
		return f.store.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
			if err := tx.Exec(stateLock).Error; err != nil {
				return err
			}
			if err := tx.Exec(`SELECT 1 FROM topic_maintenance_windows WHERE window_id=?::uuid FOR NO KEY UPDATE`, window.ID).Error; err != nil {
				return err
			}
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return run(bounded)
		})
	}

	require.NoError(t, withHeldLocks(`SELECT 1 FROM topic_maintenance_state WHERE singleton FOR SHARE`, func(bounded context.Context) error {
		var group errgroup.Group
		for index, claim := range claims {
			group.Go(func() error {
				for attempt := 1; attempt <= 3; attempt++ {
					if err := f.store.AdmitMaintenanceAttempt(bounded, *claim, assessmentIDs[index], attempt, 1000, 500, time.Now().UTC()); err != nil {
						return fmt.Errorf("admit batch %s attempt %d: %w", claim.ID, attempt, err)
					}
				}
				return nil
			})
		}
		return group.Wait()
	}), "attempt admission must finish while state is shared-locked and the window is write-locked")

	require.NoError(t, withHeldLocks(`SELECT 1 FROM topic_maintenance_state WHERE singleton FOR UPDATE`, func(bounded context.Context) error {
		var group errgroup.Group
		for index, claim := range claims {
			group.Go(func() error {
				for attempt := 1; attempt <= 3; attempt++ {
					if err := f.store.ReconcileMaintenanceAttempt(bounded, *claim, assessmentIDs[index], topic.AssessmentAttempt{
						Number: attempt, ReportedInputTokens: 900, ReportedOutputTokens: 200, ReportedTotalTokens: 1100,
					}); err != nil {
						return fmt.Errorf("reconcile batch %s attempt %d: %w", claim.ID, attempt, err)
					}
				}
				outcomes := make([]topic.OrganizationOutcome, 0, len(claim.Sources))
				for _, source := range claim.Sources {
					outcomes = append(outcomes, topic.OrganizationOutcome{Source: source, Status: "organized"})
				}
				result := topic.OrganizationResult{AssessmentID: assessmentIDs[index], Current: true, Outcomes: outcomes}
				if err := f.store.CompleteMaintenanceBatch(bounded, *claim, result, "", time.Now().UTC()); err != nil {
					return fmt.Errorf("complete batch %s: %w", claim.ID, err)
				}
				return nil
			})
		}
		group.Go(func() error {
			_, err := f.store.MaintenanceStatus(bounded, time.Now().UTC())
			return err
		})
		group.Go(func() error {
			_, err := f.store.previewMaintenanceTurn(bounded, window.ID, time.Now().UTC())
			return err
		})
		return group.Wait()
	}), "reconciliation, completion and reads must finish under an exclusive admission lock")

	status, err := f.store.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Zero(t, status.Usage.UnreconciledAttempts)
	require.EqualValues(t, 6, status.Usage.Attempts)
	require.EqualValues(t, 5400, status.Usage.ReportedInputTokens)
	require.EqualValues(t, 1200, status.Usage.ReportedOutputTokens)
	require.NotNil(t, status.LastSuccessfulProgress, "per-team progress must surface in MaintenanceStatus")
}

func TestOntologyMaintenanceBatchDefersContendedTeamLease(t *testing.T) {
	fixture := newOrganizationFixture(t)
	maintenanceEvidence(t, fixture, "Pending assignment during team-row contention.")
	config := maintenanceSettings(t, fixture)
	window := maintenanceWindow(t, fixture, config)
	ctx := t.Context()
	now := time.Now().UTC()
	turn, err := fixture.store.ClaimMaintenanceTurn(ctx, window.ID, now, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	for range 6 {
		require.NoError(t, fixture.store.discoverMaintenanceError(ctx, *turn, topic.MaintenancePageSize))
	}
	fence := scope{TeamID: turn.TeamID, SpaceID: turn.SpaceID, Generation: turn.Generation}
	require.NoError(t, fixture.store.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if err := lockMaintenanceProgressTeam(tx, fence); err != nil {
			return err
		}
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		claim, err := fixture.store.ClaimMaintenanceBatch(bounded, *turn, window.ID, now)
		require.NoError(t, err)
		require.Nil(t, claim)
		return nil
	}))
	claim, err := fixture.store.ClaimMaintenanceBatch(ctx, *turn, window.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, claim)
	_, err = fixture.store.admitMaintenanceBatch(ctx, *turn, claim, nil, time.Now().UTC())
	require.ErrorIs(t, err, topic.ErrLeaseLost)
}

func TestOntologyMaintenanceEmptyPreviewUsesCurrentSources(t *testing.T) {
	fixture := newOrganizationFixture(t)
	source := maintenanceEvidence(t, fixture, "Assignment failure before eligibility changes.")
	config := maintenanceSettings(t, fixture)
	window := maintenanceWindow(t, fixture, config)
	ctx := t.Context()
	now := time.Now().UTC()
	turn, err := fixture.store.ClaimMaintenanceTurn(ctx, window.ID, now, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	for range 6 {
		require.NoError(t, fixture.store.discoverMaintenanceError(ctx, *turn, topic.MaintenancePageSize))
	}
	require.NoError(t, fixture.store.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		result := tx.Exec(`UPDATE topic_maintenance_sources SET status='failed',last_run_id=?::uuid
			WHERE team_id=?::uuid AND source_id=?`, turn.RunID, turn.TeamID, source.ID)
		require.EqualValues(t, 1, result.RowsAffected)
		return result.Error
	}))
	require.NoError(t, fixture.store.ReleaseMaintenanceTurn(ctx, *turn))
	preview, err := fixture.store.previewMaintenanceTurn(ctx, window.ID, now)
	require.NoError(t, err)
	require.NotNil(t, preview)
	require.Empty(t, preview.candidates)
	require.Zero(t, preview.active)
	require.Equal(t, turn.RunID, preview.runID)
	require.NoError(t, fixture.store.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		unresolved, err := maintenanceRunHasUnresolvedSources(tx, preview.runID, "", true)
		require.NoError(t, err)
		require.True(t, unresolved)
		return tx.Exec(`UPDATE topic_maintenance_sources SET eligible=false,status='unavailable'
			WHERE team_id=?::uuid AND source_id=?`, turn.TeamID, source.ID).Error
	}))
	require.NoError(t, fixture.store.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return fixture.store.completeEmptyMaintenanceRun(bounded, *preview, window.ID, now)
	}))
	var status, code string
	require.NoError(t, fixture.admin.Raw(`SELECT status,failure_code FROM topic_maintenance_runs WHERE run_id=?::uuid`, turn.RunID).Row().Scan(&status, &code))
	require.Equal(t, "completed", status)
	require.Empty(t, code)
}
