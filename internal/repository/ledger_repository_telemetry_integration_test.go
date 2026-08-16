package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestLedgerCreateIngestQuarantineRecordsFirstDispositionOnce(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition")
	repo := NewLedgerRepository(appDB, rls)
	input := CreateIngestInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerID,
		IdempotencyKey:    "first-disposition-quarantine",
		RequestHash:       "first-disposition-quarantine-hash",
		Status:            string(domain.PlacementRunQuarantined),
		TelemetryRemember: true,
		Evidence: []EvidenceInput{{
			Content: "This evidence was quarantined before semantic assessment.",
		}},
	}

	created, err := repo.CreateIngest(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, created.FirstDisposition)
	assert.Equal(t, string(domain.PlacementRunQuarantined), created.FirstDisposition.Status)
	assert.False(t, created.FirstDisposition.CreatedAt.IsZero())
	assert.False(t, created.FirstDisposition.CompletedAt.IsZero())
	assert.True(t, created.FirstDisposition.IsRemember)

	replayed, err := repo.CreateIngest(ctx, input)
	require.NoError(t, err)
	assert.True(t, replayed.Existing)
	assert.Nil(t, replayed.FirstDisposition)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
			  AND status = 'quarantined'
		`, teamID, created.PlacementRunID).Scan(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), count)
		return nil
	})
	require.NoError(t, err)
}

func TestLedgerFirstDispositionMarkerKeepsLegacyIdempotencyKey(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-idempotency")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-idempotency")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "first-disposition-idempotency",
		RequestHash:    "first-disposition-idempotency-hash",
		Evidence: []EvidenceInput{{
			Content: "A caller outcome must not suppress telemetry's private marker.",
		}},
	})
	require.NoError(t, err)

	_, err = repo.AppendPlacementOutcome(ctx, PlacementOutcomeInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		PlacementRunID: created.PlacementRunID,
		OutcomeKind:    "telemetry_first_disposition",
		Status:         "awaiting_review",
		IdempotencyKey: placementFirstDispositionIdempotencyKey(created.PlacementRunID),
		Payload:        map[string]any{"telemetry": "first_disposition"},
	})
	require.NoError(t, err)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "telemetry-idempotency-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	disposition, err := repo.FinishPlacementRun(ctx, teamID, created.PlacementRunID, "telemetry-idempotency-worker", string(domain.PlacementRunCompleted), "")
	require.NoError(t, err)
	require.Nil(t, disposition)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var markerKey string
		if err := tx.Raw(`
			SELECT idempotency_key
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
		`, teamID, created.PlacementRunID).Scan(&markerKey).Error; err != nil {
			return err
		}
		assert.Equal(t, placementFirstDispositionIdempotencyKey(created.PlacementRunID), markerKey)
		return nil
	})
	require.NoError(t, err)
}

func TestLedgerFirstDispositionTreatsLegacyMarkerRowAsIdempotent(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-legacy-marker")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-legacy-marker")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "first-disposition-legacy-marker",
		RequestHash:    "first-disposition-legacy-marker-hash",
		Evidence: []EvidenceInput{{
			Content: "A legacy telemetry marker can predate the canonical private idempotency key.",
		}},
	})
	require.NoError(t, err)

	err = rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO placement_outcomes (
				team_id, placement_run_id, owner_profile_id,
				outcome_kind, status, idempotency_key, payload
			) VALUES (
				?::uuid, ?::uuid, ?::uuid,
				'telemetry_first_disposition', 'awaiting_review', '', '{}'
			)
		`, teamID, created.PlacementRunID, ownerID).Error
	})
	require.NoError(t, err)

	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "telemetry-legacy-marker-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	disposition, err := repo.FinishPlacementRun(ctx, teamID, created.PlacementRunID, "telemetry-legacy-marker-worker", string(domain.PlacementRunCompleted), "")
	require.NoError(t, err)
	require.Nil(t, disposition)
	assertTelemetryFirstDispositionMarkerCount(t, ctx, appDB, rls, teamID, created.PlacementRunID, 1)
}

func TestLedgerFirstDispositionOriginCannotBeSpoofedByMetadata(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-origin")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-origin")
	repo := NewLedgerRepository(appDB, rls)

	internal, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "internal-origin",
		RequestHash:    "internal-origin-hash",
		Status:         string(domain.PlacementRunQuarantined),
		Metadata: map[string]any{
			ingestMetadataTelemetryOriginKey: ingestMetadataTelemetryOriginRemember,
		},
		Evidence: []EvidenceInput{{Content: "Internal ingest metadata cannot claim a Remember origin."}},
	})
	require.NoError(t, err)
	require.NotNil(t, internal.FirstDisposition)
	assert.False(t, internal.FirstDisposition.IsRemember)

	remember, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerID,
		IdempotencyKey:    "remember-origin",
		RequestHash:       "remember-origin-hash",
		Status:            string(domain.PlacementRunQuarantined),
		TelemetryRemember: true,
		Evidence:          []EvidenceInput{{Content: "The Remember service has an authoritative telemetry origin."}},
	})
	require.NoError(t, err)
	require.NotNil(t, remember.FirstDisposition)
	assert.True(t, remember.FirstDisposition.IsRemember)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var internalOrigin, rememberOrigin string
		if err := tx.Raw(`
			SELECT COALESCE(metadata ->> ?, '')
			FROM knowledge_ingests
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, ingestMetadataTelemetryOriginKey, teamID, internal.IngestID).Scan(&internalOrigin).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COALESCE(metadata ->> ?, '')
			FROM knowledge_ingests
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, ingestMetadataTelemetryOriginKey, teamID, remember.IngestID).Scan(&rememberOrigin).Error; err != nil {
			return err
		}
		assert.Empty(t, internalOrigin)
		assert.Equal(t, ingestMetadataTelemetryOriginRemember, rememberOrigin)
		return nil
	})
	require.NoError(t, err)
}

func TestLedgerFirstDispositionRecognizesLegacyRememberMetadata(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-legacy-origin")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-legacy-origin")
	_, legacyRunID := insertTelemetryBackfillRunWithMetadata(
		t, ctx, appDB, rls, teamID, ownerID, string(domain.PlacementRunQueued), false,
		legacyRememberTelemetryMetadata(teamID, ownerID),
	)
	_, internalRunID := insertTelemetryBackfillRunWithMetadata(
		t, ctx, appDB, rls, teamID, ownerID, string(domain.PlacementRunQueued), false,
		legacyRememberTelemetryMetadata(teamID, uuid.NewString()),
	)

	err := rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		now := time.Now().UTC()
		legacy, err := appendPlacementFirstDisposition(ctx, tx, teamID, ownerID, legacyRunID, string(domain.PlacementRunCompleted), now.Add(-time.Minute), now)
		if err != nil {
			return err
		}
		require.NotNil(t, legacy)
		assert.True(t, legacy.IsRemember)

		internal, err := appendPlacementFirstDisposition(ctx, tx, teamID, ownerID, internalRunID, string(domain.PlacementRunCompleted), now.Add(-time.Minute), now)
		if err != nil {
			return err
		}
		require.NotNil(t, internal)
		assert.False(t, internal.IsRemember)
		return nil
	})
	require.NoError(t, err)
}

func TestLedgerBackfillFirstDispositionMarkersUsesTerminalRememberRunsAndPersistsKeyset(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-backfill")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-backfill")
	repo := NewLedgerRepository(appDB, rls)
	_, firstRunID := insertTelemetryBackfillRun(t, ctx, appDB, rls, teamID, ownerID, string(domain.PlacementRunCompleted), true, true)
	_, secondRunID := insertTelemetryBackfillRunWithMetadata(
		t, ctx, appDB, rls, teamID, ownerID, string(domain.PlacementRunAwaitingReview), true,
		legacyRememberTelemetryMetadata(teamID, ownerID),
	)
	_, internalRunID := insertTelemetryBackfillRunWithMetadata(
		t, ctx, appDB, rls, teamID, ownerID, string(domain.PlacementRunCompleted), true,
		legacyRememberTelemetryMetadata(teamID, uuid.NewString()),
	)

	first, err := repo.BackfillPlacementFirstDispositionMarkers(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.Candidates)
	assert.Equal(t, int64(1), first.Inserted)
	assert.False(t, first.SweepComplete)
	assertTelemetryBackfillState(t, ctx, appDB, rls, true, false)

	second, err := repo.BackfillPlacementFirstDispositionMarkers(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), second.Candidates)
	assert.Equal(t, int64(1), second.Inserted)
	assert.False(t, second.SweepComplete)

	completed, err := repo.BackfillPlacementFirstDispositionMarkers(ctx, 1)
	require.NoError(t, err)
	assert.Zero(t, completed.Candidates)
	assert.Zero(t, completed.Inserted)
	assert.True(t, completed.SweepComplete)
	assertTelemetryBackfillState(t, ctx, appDB, rls, false, true)

	restarted, err := repo.BackfillPlacementFirstDispositionMarkers(ctx, 1)
	require.NoError(t, err)
	assert.Zero(t, restarted.Candidates)
	assert.Zero(t, restarted.Inserted)
	assert.True(t, restarted.SweepComplete)
	assertTelemetryBackfillState(t, ctx, appDB, rls, false, true)

	err = rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		statuses := map[string]string{}
		rows, err := tx.Raw(`
			SELECT placement_run_id::text, status
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id IN (?::uuid, ?::uuid, ?::uuid)
			  AND outcome_kind = 'telemetry_first_disposition'
		`, teamID, firstRunID, secondRunID, internalRunID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var runID, status string
			if err := rows.Scan(&runID, &status); err != nil {
				return err
			}
			statuses[runID] = status
		}
		if err := rows.Err(); err != nil {
			return err
		}
		assert.Equal(t, string(domain.PlacementRunCompleted), statuses[firstRunID])
		assert.Equal(t, string(domain.PlacementRunAwaitingReview), statuses[secondRunID])
		assert.NotContains(t, statuses, internalRunID)
		return nil
	})
	require.NoError(t, err)
}

func TestLedgerBackfillFirstDispositionMarkersSuppressesRequeuedLegacyRun(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-backfill-requeued")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-backfill-requeued")
	repo := NewLedgerRepository(appDB, rls)
	_, runID := insertTelemetryBackfillRunWithMetadata(
		t, ctx, appDB, rls, teamID, ownerID, string(domain.PlacementRunGuarded), false,
		legacyRememberTelemetryMetadata(teamID, ownerID),
	)

	err := rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO placement_outcomes (
				team_id, placement_run_id, owner_profile_id,
				outcome_kind, status, idempotency_key, payload
			) VALUES (
				?::uuid, ?::uuid, ?::uuid,
				'placement_resolution', 'entity_selected', 'resolution-before-telemetry-marker', '{}'
			)
		`, teamID, runID, ownerID).Error
	})
	require.NoError(t, err)

	backfilled, err := repo.BackfillPlacementFirstDispositionMarkers(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), backfilled.Candidates)
	assert.Equal(t, int64(1), backfilled.Inserted)
	assert.False(t, backfilled.SweepComplete)
	assert.False(t, backfilled.WaitingForActiveRun)
	assertTelemetryFirstDispositionMarkerCount(t, ctx, appDB, rls, teamID, runID, 1)

	err = rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		var status string
		if err := tx.Raw(`
			SELECT status
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
		`, teamID, runID).Scan(&status).Error; err != nil {
			return err
		}
		assert.Equal(t, "suppressed", status)
		return nil
	})
	require.NoError(t, err)
}

func TestLedgerBackfillFirstDispositionMarkersKeepsPartialActiveRunsPending(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-backfill-active-partial")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-backfill-active-partial")
	repo := NewLedgerRepository(appDB, rls)
	_, semanticCommitRunID := insertTelemetryBackfillRunWithMetadata(
		t, ctx, appDB, rls, teamID, ownerID, string(domain.PlacementRunGuarded), false,
		legacyRememberTelemetryMetadata(teamID, ownerID),
	)
	_, retryRunID := insertTelemetryBackfillRunWithMetadata(
		t, ctx, appDB, rls, teamID, ownerID, string(domain.PlacementRunQueued), false,
		legacyRememberTelemetryMetadata(teamID, ownerID),
	)

	err := rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO placement_outcomes (
				team_id, placement_run_id, owner_profile_id,
				outcome_kind, status, idempotency_key, payload
			) VALUES
				(?::uuid, ?::uuid, ?::uuid, 'semantic_commit', 'accepted', 'partial-commit-before-telemetry-marker', '{}'::jsonb),
				(?::uuid, ?::uuid, ?::uuid, 'placement_retry', 'retryable', 'retry-before-telemetry-marker', '{}'::jsonb)
		`, teamID, semanticCommitRunID, ownerID, teamID, retryRunID, ownerID).Error
	})
	require.NoError(t, err)

	backfilled, err := repo.BackfillPlacementFirstDispositionMarkers(ctx, 10)
	require.NoError(t, err)
	assert.Zero(t, backfilled.Candidates)
	assert.Zero(t, backfilled.Inserted)
	assert.False(t, backfilled.SweepComplete)
	assert.True(t, backfilled.WaitingForActiveRun)
	assertTelemetryFirstDispositionMarkerCount(t, ctx, appDB, rls, teamID, semanticCommitRunID, 0)
	assertTelemetryFirstDispositionMarkerCount(t, ctx, appDB, rls, teamID, retryRunID, 0)
}

func TestLedgerBackfillFirstDispositionMarkersWaitsForRunTerminalState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-backfill-terminal")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-backfill-terminal")
	repo := NewLedgerRepository(appDB, rls)
	ingestID, runID := insertTelemetryBackfillRun(t, ctx, appDB, rls, teamID, ownerID, string(domain.PlacementRunGuarded), false, true)

	beforeTerminal, err := repo.BackfillPlacementFirstDispositionMarkers(ctx, 1)
	require.NoError(t, err)
	assert.Zero(t, beforeTerminal.Candidates)
	assert.Zero(t, beforeTerminal.Inserted)
	assert.False(t, beforeTerminal.SweepComplete)
	assert.True(t, beforeTerminal.WaitingForActiveRun)
	assertTelemetryBackfillState(t, ctx, appDB, rls, false, false)
	assertTelemetryFirstDispositionMarkerCount(t, ctx, appDB, rls, teamID, runID, 0)

	err = rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE placement_runs
			SET status = 'completed',
				completed_at = clock_timestamp()
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND ingest_id = ?::uuid
		`, teamID, runID, ingestID).Error
	})
	require.NoError(t, err)

	afterTerminal, err := repo.BackfillPlacementFirstDispositionMarkers(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), afterTerminal.Candidates)
	assert.Equal(t, int64(1), afterTerminal.Inserted)
	assert.False(t, afterTerminal.SweepComplete)
	assertTelemetryFirstDispositionMarkerCount(t, ctx, appDB, rls, teamID, runID, 1)
}

func TestLedgerBackfillFirstDispositionMarkersRescansLockedRun(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-backfill-locked")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-backfill-locked")
	repo := NewLedgerRepository(appDB, rls)
	_, runID := insertTelemetryBackfillRun(t, ctx, appDB, rls, teamID, ownerID, string(domain.PlacementRunCompleted), true, true)

	lockTx := adminDB.WithContext(ctx).Begin()
	require.NoError(t, lockTx.Error)
	var lockedRunID string
	require.NoError(t, lockTx.Raw(`
		SELECT placement_run_id::text
		FROM placement_runs
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
		FOR UPDATE
	`, teamID, runID).Row().Scan(&lockedRunID))
	require.Equal(t, runID, lockedRunID)

	skipped, err := repo.BackfillPlacementFirstDispositionMarkers(ctx, 1)
	require.NoError(t, err)
	assert.Zero(t, skipped.Candidates)
	assert.Zero(t, skipped.Inserted)
	assert.False(t, skipped.SweepComplete)
	assertTelemetryFirstDispositionMarkerCount(t, ctx, appDB, rls, teamID, runID, 0)
	require.NoError(t, lockTx.Rollback().Error)

	retried, err := repo.BackfillPlacementFirstDispositionMarkers(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), retried.Candidates)
	assert.Equal(t, int64(1), retried.Inserted)
	assert.False(t, retried.SweepComplete)
	assertTelemetryFirstDispositionMarkerCount(t, ctx, appDB, rls, teamID, runID, 1)
}

func insertTelemetryBackfillRun(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	teamID, ownerID, status string,
	terminal, remember bool,
) (string, string) {
	t.Helper()
	metadata := "{}"
	if remember {
		metadata = `{"_dense_mem_telemetry_origin":"remember"}`
	}
	return insertTelemetryBackfillRunWithMetadata(t, ctx, db, rls, teamID, ownerID, status, terminal, metadata)
}

func insertTelemetryBackfillRunWithMetadata(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	teamID, ownerID, status string,
	terminal bool,
	metadata string,
) (string, string) {
	t.Helper()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO knowledge_ingests (team_id, ingest_id, owner_profile_id, status, metadata)
			VALUES (?::uuid, ?::uuid, ?::uuid, 'completed', ?::jsonb)
		`, teamID, ingestID, ownerID, metadata).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO placement_runs (
				team_id, placement_run_id, ingest_id, owner_profile_id,
				status, created_at, completed_at
			) VALUES (
				?::uuid, ?::uuid, ?::uuid, ?::uuid,
				?, clock_timestamp() - interval '2 days',
				CASE WHEN ? THEN clock_timestamp() - interval '1 day' ELSE NULL END
			)
		`, teamID, runID, ingestID, ownerID, status, terminal).Error
	}))
	return ingestID, runID
}

func legacyRememberTelemetryMetadata(teamID, ownerID string) string {
	return fmt.Sprintf(
		`{"contract_version":%q,"actor":{"team_id":%q,"profile_id":%q}}`,
		domain.ContractVersion,
		teamID,
		ownerID,
	)
}

func assertTelemetryFirstDispositionMarkerCount(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	teamID, runID string,
	want int64,
) {
	t.Helper()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
		`, teamID, runID).Scan(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, want, count)
		return nil
	}))
}

func assertTelemetryBackfillState(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	wantCursor bool,
	wantCompleted bool,
) {
	t.Helper()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		var teamID, ingestID *string
		var completed bool
		if err := tx.Raw(`
			SELECT cursor_team_id::text, cursor_ingest_id::text, completed_at IS NOT NULL
			FROM telemetry_first_disposition_backfill_state
			WHERE state_key = 'placement_first_disposition'
		`).Row().Scan(&teamID, &ingestID, &completed); err != nil {
			return err
		}
		assert.Equal(t, wantCursor, teamID != nil)
		assert.Equal(t, wantCursor, ingestID != nil)
		assert.Equal(t, wantCompleted, completed)
		return nil
	}))
}

func TestLedgerFinishPlacementRunRecordsFirstDispositionOnce(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-finish-first-disposition")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-finish-first-disposition")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "finish-first-disposition",
		RequestHash:    "finish-first-disposition-hash",
		Evidence: []EvidenceInput{{
			Content: "A worker completion records the first terminal placement disposition.",
		}},
	})
	require.NoError(t, err)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "telemetry-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	disposition, err := repo.FinishPlacementRun(ctx, teamID, created.PlacementRunID, "telemetry-worker", string(domain.PlacementRunCompleted), "")
	require.NoError(t, err)
	require.NotNil(t, disposition)
	assert.Equal(t, string(domain.PlacementRunCompleted), disposition.Status)
	assert.False(t, disposition.CreatedAt.IsZero())
	assert.False(t, disposition.CompletedAt.IsZero())

	_, err = repo.FinishPlacementRun(ctx, teamID, created.PlacementRunID, "telemetry-worker", string(domain.PlacementRunCompleted), "")
	require.ErrorIs(t, err, ErrPlacementLeaseConflict)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
			  AND status = 'completed'
		`, teamID, created.PlacementRunID).Scan(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), count)
		return nil
	})
	require.NoError(t, err)
}

func TestLedgerFirstDispositionMarkerAllowsUserRequeue(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-requeue")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-requeue")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "first-disposition-requeue",
		RequestHash:    "first-disposition-requeue-hash",
		Evidence: []EvidenceInput{{
			Content: "A reviewer can requeue this placement after its first disposition.",
		}},
	})
	require.NoError(t, err)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "first-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	first, err := repo.FinishPlacementRun(ctx, teamID, created.PlacementRunID, "first-worker", string(domain.PlacementRunCompleted), "")
	require.NoError(t, err)
	require.NotNil(t, first)

	resolved, err := repo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               string(domain.ResolveSelectEntity),
		IngestID:             created.IngestID,
		PlacementItemID:      created.Items[0].PlacementItemID,
		PlacementItemVersion: created.Items[0].Version,
		EntityRef:            "reviewed entity",
		CandidateEntityID:    uuid.NewString(),
		IdempotencyKey:       "first-disposition-requeue-resolution",
	})
	require.NoError(t, err)
	assert.Equal(t, string(domain.PlacementRunQueued), resolved.Status)
	assert.Nil(t, resolved.FirstDisposition)

	claimed, err = repo.ClaimNextPlacementRun(ctx, teamID, "second-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, created.PlacementRunID, claimed.PlacementRunID)

	second, err := repo.FinishPlacementRun(ctx, teamID, created.PlacementRunID, "second-worker", string(domain.PlacementRunCompleted), "")
	require.NoError(t, err)
	assert.Nil(t, second)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
		`, teamID, created.PlacementRunID).Scan(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), count)
		return nil
	})
	require.NoError(t, err)
}

func TestPlacementResolutionQuarantineRecordsFirstDisposition(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-resolution-first-disposition-quarantine")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-resolution-first-disposition-quarantine")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "resolution-first-disposition-quarantine",
		RequestHash:    "resolution-first-disposition-quarantine-hash",
		Evidence: []EvidenceInput{{
			Content: "This placement starts queued before reviewer evidence arrives.",
		}},
	})
	require.NoError(t, err)

	resolved, err := repo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               string(domain.ResolveAccept),
		IngestID:             created.IngestID,
		PlacementItemID:      created.Items[0].PlacementItemID,
		PlacementItemVersion: created.Items[0].Version,
		IdempotencyKey:       "resolution-first-disposition-quarantine-action",
		Evidence: []EvidenceInput{{
			Content: "Reviewer evidence requires quarantine.",
			InitialEvent: &SecurityEventDraft{
				EventKind: "deterministic_scan",
				Decision:  "quarantine",
				Reason:    "deterministic quarantine",
			},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resolved.FirstDisposition)
	assert.Equal(t, string(domain.PlacementRunQuarantined), resolved.FirstDisposition.Status)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
			  AND status = 'quarantined'
		`, teamID, created.PlacementRunID).Scan(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), count)
		return nil
	})
	require.NoError(t, err)
}
