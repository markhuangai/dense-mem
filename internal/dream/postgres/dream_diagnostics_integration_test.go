//go:build integration

package postgres

import (
	"context"
	"fmt"
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
		Details:      map[string]any{"status": "completed", "phase_trace_expected": []map[string]any{}},
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
	require.Equal(t, false, runCapture.Details["phase_trace_truncated"])
	detail, err := store.GetDreamDiagnostic(ctx, teamID, run.RunID, runCapture.CaptureID)
	require.NoError(t, err)
	require.NotEmpty(t, detail.Payload)
	require.Equal(t, false, detail.Details["phase_trace_truncated"])

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

func TestDreamDiagnosticDeadlineMarkerPersistsWithPartialPhaseTrace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-diagnostics-deadline")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-diagnostics-deadline-owner")
	store := newDreamFixtureStore(appDB, rls)
	run, err := store.ClaimDreamCycle(ctx, dreamcontract.DreamCycleClaimInput{
		TeamID: teamID, InitiatedByProfileID: ownerID, RunDate: "2026-09-21",
		WindowKey: "manual:diagnostic-deadline", LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	require.True(t, run.Claimed)

	require.NoError(t, adminDB.Exec(fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION dense_mem_test_reject_primary_run_diagnostic()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.team_id = '%s'::uuid AND NEW.run_id = '%s'::uuid
				AND NEW.phase = 'run' AND NEW.capture_reason <> 'diagnostic_capture_failed' THEN
				RAISE EXCEPTION 'planned primary diagnostic failure';
			END IF;
			RETURN NEW;
		END;
		$$
	`, teamID, run.RunID)).Error)
	require.NoError(t, adminDB.Exec(`
		CREATE TRIGGER dense_mem_test_reject_primary_run_diagnostic
		BEFORE INSERT ON dream_diagnostic_captures
		FOR EACH ROW EXECUTE FUNCTION dense_mem_test_reject_primary_run_diagnostic()
	`).Error)
	defer func() {
		require.NoError(t, adminDB.Exec(`DROP TRIGGER dense_mem_test_reject_primary_run_diagnostic ON dream_diagnostic_captures`).Error)
		require.NoError(t, adminDB.Exec(`DROP FUNCTION dense_mem_test_reject_primary_run_diagnostic()`).Error)
	}()

	expected := []map[string]any{
		{"phase": "target", "hypothesis_id": "", "count": 1},
		{"phase": "provider", "hypothesis_id": "", "count": 1},
	}
	deadlineAt := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	primary := dreamcontract.DreamDiagnosticCaptureInput{
		TeamID: teamID, RunID: run.RunID, Phase: "run", Outcome: "completed",
		Details:      map[string]any{"status": "completed", "phase_trace_expected": expected, "phase_trace_deadline_at": deadlineAt},
		CaptureState: "not_captured", CaptureReason: "provider_payload_not_retained",
	}
	require.Error(t, store.RecordDreamRunDiagnostics(ctx, primary), "the scoped trigger forces the primary run capture to use its fallback")
	fallback := primary
	fallback.Details = map[string]any{"status": "completed", "capture_failed": true, "phase_trace_expected": expected, "phase_trace_deadline_at": deadlineAt}
	fallback.CaptureState = "unavailable"
	fallback.CaptureReason = "diagnostic_capture_failed"
	require.NoError(t, store.RecordDreamRunDiagnostics(ctx, fallback))
	require.NoError(t, store.RecordDreamRunDiagnostics(ctx, fallback), "retrying a run summary must not add another run capture")

	require.NoError(t, store.RecordDreamDiagnostic(ctx, dreamcontract.DreamDiagnosticCaptureInput{
		TeamID: teamID, RunID: run.RunID, Phase: "target", Outcome: "completed",
		CaptureState: "not_captured", CaptureReason: "phase_metadata_only",
	}))

	page, err := store.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{TeamID: teamID, RunID: run.RunID, Limit: 25})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	runCaptures := 0
	for _, capture := range page.Items {
		if capture.Phase != "run" {
			continue
		}
		runCaptures++
		require.Equal(t, false, capture.Details["phase_trace_truncated"])
		require.Equal(t, true, capture.Details["phase_trace_pending"])
		require.NotContains(t, capture.Details, "phase_trace_expected")
		require.NotContains(t, capture.Details, "phase_trace_deadline_at")
		require.Equal(t, "unavailable", capture.CaptureState)
		detail, detailErr := store.GetDreamDiagnostic(ctx, teamID, run.RunID, capture.CaptureID)
		require.NoError(t, detailErr)
		require.Equal(t, false, detail.Details["phase_trace_truncated"])
		require.Equal(t, true, detail.Details["phase_trace_pending"])
	}
	require.Equal(t, 1, runCaptures)

	deadlineAt = time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE dream_diagnostic_captures
			SET details = jsonb_set(details, '{phase_trace_deadline_at}', to_jsonb(?::text), false)
			WHERE team_id = ?::uuid AND run_id = ?::uuid AND phase = 'run' AND hypothesis_id IS NULL
		`, deadlineAt, teamID, run.RunID).Error
	}))
	page, err = store.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{TeamID: teamID, RunID: run.RunID, Limit: 25})
	require.NoError(t, err)
	for _, capture := range page.Items {
		if capture.Phase != "run" {
			continue
		}
		require.Equal(t, true, capture.Details["phase_trace_truncated"])
		require.Equal(t, false, capture.Details["phase_trace_pending"])
		detail, detailErr := store.GetDreamDiagnostic(ctx, teamID, run.RunID, capture.CaptureID)
		require.NoError(t, detailErr)
		require.Equal(t, true, detail.Details["phase_trace_truncated"])
		require.Equal(t, false, detail.Details["phase_trace_pending"])
	}

	require.NoError(t, store.RecordDreamDiagnostic(ctx, dreamcontract.DreamDiagnosticCaptureInput{
		TeamID: teamID, RunID: run.RunID, Phase: "provider", Outcome: "completed",
		CaptureState: "not_captured", CaptureReason: "phase_metadata_only",
	}))
	page, err = store.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{TeamID: teamID, RunID: run.RunID, Limit: 25})
	require.NoError(t, err)
	runCaptures = 0
	for _, capture := range page.Items {
		if capture.Phase != "run" {
			continue
		}
		runCaptures++
		require.Equal(t, false, capture.Details["phase_trace_truncated"])
		require.Equal(t, false, capture.Details["phase_trace_pending"])
	}
	require.Equal(t, 1, runCaptures)
	var persistedRunCaptures int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*) FROM dream_diagnostic_captures
			WHERE team_id = ?::uuid AND run_id = ?::uuid AND phase = 'run' AND hypothesis_id IS NULL
		`, teamID, run.RunID).Scan(&persistedRunCaptures).Error
	}))
	require.Equal(t, 1, persistedRunCaptures)
}

func TestDreamDiagnosticRunSummaryRetriesSerializeConcurrently(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-diagnostics-concurrent-summary")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-diagnostics-concurrent-owner")
	store := newDreamFixtureStore(appDB, rls)
	run, err := store.ClaimDreamCycle(ctx, dreamcontract.DreamCycleClaimInput{
		TeamID: teamID, InitiatedByProfileID: ownerID, RunDate: "2026-09-21",
		WindowKey: "manual:concurrent-diagnostic-summary", LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	require.True(t, run.Claimed)

	lockKey := dreamDiagnosticRunSummaryLockNamespace + teamID + ":" + run.RunID
	lockTx := adminDB.WithContext(ctx).Begin()
	require.NoError(t, lockTx.Error)
	require.NoError(t, lockTx.Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended(?, ?))",
		lockKey, dreamDiagnosticRunSummaryLockHashSeed,
	).Error)
	defer func() { _ = lockTx.Rollback().Error }()

	input := dreamcontract.DreamDiagnosticCaptureInput{
		TeamID: teamID, RunID: run.RunID, Phase: "run", Outcome: "completed",
		Details:      map[string]any{"status": "completed", "phase_trace_expected": []map[string]any{}},
		CaptureState: "not_captured", CaptureReason: "provider_payload_not_retained",
	}
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			ready <- struct{}{}
			<-start
			results <- store.RecordDreamRunDiagnostics(ctx, input)
		}()
	}
	<-ready
	<-ready
	close(start)

	waitingDeadline := time.Now().Add(2 * time.Second)
	waiting := 0
	for {
		err = adminDB.WithContext(ctx).Raw(`
			SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND NOT granted
			  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		`).Scan(&waiting).Error
		if err != nil || waiting == 2 || time.Now().After(waitingDeadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || waiting != 2 {
		_ = lockTx.Rollback().Error
		for range 2 {
			select {
			case <-results:
			case <-ctx.Done():
				t.Fatalf("diagnostic retry did not finish after releasing the test lock: %v", ctx.Err())
			}
		}
		require.NoError(t, err)
		require.Equal(t, 2, waiting, "both PostgreSQL retries must contend on the run-summary advisory lock")
	}
	require.NoError(t, lockTx.Commit().Error)
	for range 2 {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatalf("concurrent diagnostic retries did not finish: %v", ctx.Err())
		}
	}

	var persistedRunCaptures int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*) FROM dream_diagnostic_captures
			WHERE team_id = ?::uuid AND run_id = ?::uuid AND phase = 'run' AND hypothesis_id IS NULL
		`, teamID, run.RunID).Scan(&persistedRunCaptures).Error
	}))
	require.Equal(t, 1, persistedRunCaptures)
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
