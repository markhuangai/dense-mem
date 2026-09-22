//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

func TestDreamDiagnosticsAreTeamScopedAndExpirePayloads(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-diagnostics-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-diagnostics-owner")
	otherOwnerSameTeamID := createLedgerProfile(t, adminDB, rls, teamID, "dream-diagnostics-other-owner-same-team")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "dream-diagnostics-other-team")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, otherTeamID, "dream-diagnostics-other-owner")
	store := newDreamFixtureStore(appDB, rls)

	run, err := store.ClaimDreamCycle(ctx, dreamcontract.DreamCycleClaimInput{
		TeamID: teamID, InitiatedByProfileID: ownerID, RunDate: "2026-09-21",
		WindowKey: "manual:diagnostics", LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	require.True(t, run.Claimed)
	captured := time.Now().UTC()
	require.NoError(t, store.RecordDreamRunDiagnostics(ctx, dreamcontract.DreamDiagnosticCaptureInput{
		TeamID: teamID, RunID: run.RunID, Phase: "run", Outcome: "completed",
		CaptureState: "captured", Payload: []byte(`{"provider_exchanges":[{"response_body":"safe"}]}`), CapturedAt: &captured,
	}))
	var expiryWithinRetention bool
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT expires_at >= created_at
			   AND expires_at <= created_at + INTERVAL '7 days'
			FROM dream_diagnostic_captures
			WHERE team_id = ?::uuid AND run_id = ?::uuid
			ORDER BY created_at ASC
			LIMIT 1
		`, teamID, run.RunID).Scan(&expiryWithinRetention).Error
	}))
	require.True(t, expiryWithinRetention)

	page, err := store.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{TeamID: teamID, RunID: run.RunID, Limit: 25})
	require.NoError(t, err)
	require.NotEmpty(t, page.Items)
	for _, item := range page.Items {
		require.Equal(t, teamID, item.TeamID)
		require.Empty(t, item.Payload, "list projection must not hydrate protected payloads")
	}
	runCapture := page.Items[0]
	detail, err := store.GetDreamDiagnostic(ctx, teamID, run.RunID, runCapture.CaptureID)
	require.NoError(t, err)
	require.NotEmpty(t, detail.Payload)

	otherPage, err := store.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{TeamID: otherTeamID, RunID: run.RunID, Limit: 25})
	require.NoError(t, err)
	require.Empty(t, otherPage.Items)
	var sameTeamVisible, crossTeamVisible int
	sameTeamPage, err := store.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{TeamID: teamID, RunID: run.RunID, Limit: 25})
	require.NoError(t, err)
	require.NotEmpty(t, sameTeamPage.Items, "a second owner in the same team retains team-scoped visibility")
	_ = otherOwnerSameTeamID
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM dream_diagnostic_captures WHERE team_id = ?::uuid`, teamID).Scan(&sameTeamVisible).Error
	}))
	require.Greater(t, sameTeamVisible, 0)
	require.NoError(t, rls.WithTeamTx(ctx, appDB, otherTeamID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM dream_diagnostic_captures WHERE team_id = ?::uuid`, teamID).Scan(&crossTeamVisible).Error
	}))
	require.Zero(t, crossTeamVisible)
	_ = otherOwnerID

	expiredID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO dream_diagnostic_captures (
				team_id, capture_id, run_id, phase, outcome, details, payload,
				capture_state, expires_at, created_at
			) VALUES (?::uuid, ?::uuid, ?::uuid, 'provider', 'completed', '{"target":"expired"}'::jsonb,
			          '{"provider_exchanges":[{"response_body":"expired"}]}'::jsonb, 'captured',
			          clock_timestamp() - INTERVAL '1 hour', clock_timestamp() - INTERVAL '2 hours')
		`, teamID, expiredID, run.RunID).Error
	}))
	expiredBeforePurge, err := store.GetDreamDiagnostic(ctx, teamID, run.RunID, expiredID)
	require.NoError(t, err)
	require.Equal(t, "expired", expiredBeforePurge.CaptureState)
	require.Equal(t, "retention_expired", expiredBeforePurge.CaptureReason)
	require.Empty(t, expiredBeforePurge.Details)
	require.Empty(t, expiredBeforePurge.Payload)
	deleted, err := store.PurgeExpiredDreamDiagnostics(ctx, 25)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	expired, err := store.GetDreamDiagnostic(ctx, teamID, run.RunID, expiredID)
	require.NoError(t, err)
	require.Equal(t, "expired", expired.CaptureState)
	require.Equal(t, "retention_expired", expired.CaptureReason)
	require.Empty(t, expired.Payload)
	require.Empty(t, expired.Details)
	require.Zero(t, func() int {
		purged, purgeErr := store.PurgeExpiredDreamDiagnostics(ctx, 25)
		require.NoError(t, purgeErr)
		return purged
	}(), "a fresh expired tombstone remains available during its bounded window")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE dream_diagnostic_captures
			SET tombstone_expires_at = clock_timestamp() - INTERVAL '1 second'
			WHERE team_id = ?::uuid AND capture_id = ?::uuid
		`, teamID, expiredID).Error
	}))
	deleted, err = store.PurgeExpiredDreamDiagnostics(ctx, 25)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	_, err = store.GetDreamDiagnostic(ctx, teamID, run.RunID, expiredID)
	require.ErrorIs(t, err, dreamcontract.ErrDreamDiagnosticNotFound)
}

func TestDreamDiagnosticsPersistBothLanesWithScopedPagination(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-diagnostics-both-lanes")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-diagnostics-both-lanes-owner")
	store := newDreamFixtureStore(appDB, rls)
	graphRun, err := store.ClaimDreamCycle(ctx, dreamcontract.DreamCycleClaimInput{
		TeamID: teamID, InitiatedByProfileID: ownerID, RunDate: "2026-09-21",
		WindowKey: "manual:graph-diagnostics", LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	evidenceWindow := time.Now().UTC().Truncate(time.Hour)
	evidenceRun, err := store.ClaimScheduledDreamCycle(ctx, dreamcontract.DreamCycleClaimInput{
		TeamID: teamID, RunDate: "2026-09-21", WindowKey: "hour:diagnostics", ScheduledFor: &evidenceWindow,
		LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute), Lane: "evidence_discovery",
	})
	require.NoError(t, err)

	for _, item := range []struct {
		runID string
		lane  string
	}{
		{runID: graphRun.RunID, lane: "graph"},
		{runID: evidenceRun.RunID, lane: "evidence_discovery"},
	} {
		for _, phase := range []string{"target", "provider", "validation", "proposal", "disposition"} {
			require.NoError(t, store.RecordDreamDiagnostic(ctx, dreamcontract.DreamDiagnosticCaptureInput{
				TeamID: teamID, RunID: item.runID, Phase: phase, Outcome: "completed",
				Details: map[string]any{"lane": item.lane, "phase": phase}, CaptureState: "not_captured",
			}))
		}
		page, err := store.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{TeamID: teamID, RunID: item.runID, Limit: 2})
		require.NoError(t, err)
		require.Len(t, page.Items, 2)
		require.NotEmpty(t, page.NextCursor)
		next, err := store.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{TeamID: teamID, RunID: item.runID, Limit: 2, Cursor: page.NextCursor})
		require.NoError(t, err)
		require.NotEmpty(t, next.Items)
	}
}
