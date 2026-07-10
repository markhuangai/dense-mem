package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestCreateMemoryPlacementRunSQLBindsEveryColumn(t *testing.T) {
	assertInsertBindsEveryColumn(t, createMemoryPlacementRunSQL, 20)
	assertInsertBindsEveryColumn(t, createMemoryPlacementItemSQL, 26)
}

func assertInsertBindsEveryColumn(t *testing.T, query string, wantColumns int) {
	t.Helper()
	parts := strings.SplitN(query, ") VALUES (", 2)
	require.Len(t, parts, 2)
	columns := strings.Split(strings.TrimSpace(strings.SplitN(parts[0], "(", 2)[1]), ",")
	require.Len(t, columns, wantColumns)
	require.Equal(t, len(columns), strings.Count(parts[1], "?"))
}

func TestReadPlacementRunClosesRunRowsBeforeReadingItems(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	now := time.Date(2026, 6, 27, 2, 30, 0, 0, time.UTC)
	runRows := sqlmock.NewRows([]string{
		"ingest_id",
		"profile_id",
		"actor_profile_id",
		"actor_role",
		"status",
		"check_after_seconds",
		"status_tool",
		"pipeline_version",
		"evidence",
		"proposal",
		"review_tasks",
		"security",
		"migration_refs",
		"requires_acknowledgement",
		"error",
		"created_at",
		"updated_at",
		"started_at",
		"completed_at",
		"acknowledged_at",
	}).AddRow(
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"",
		"",
		"completed",
		60,
		"get_memory_placement",
		"semantic-edge-v2",
		[]byte(`[{"index":0,"content":"memory"}]`),
		[]byte(`{}`),
		[]byte(`[]`),
		[]byte(`{}`),
		[]byte(`[]`),
		true,
		"",
		now,
		now,
		nil,
		now,
		nil,
	)
	itemRows := sqlmock.NewRows([]string{
		"item_id",
		"ingest_id",
		"profile_id",
		"evidence_index",
		"fragment_id",
		"evidence_indexes",
		"fragment_ids",
		"category",
		"status",
		"reason",
		"error",
		"claim_id",
		"fact_id",
		"assertion_id",
		"relationship_type",
		"tier",
		"assertion_status",
		"policy_family",
		"verifier_verdict",
		"verifier_confidence",
		"review_task_id",
		"proposed_relationship",
		"reviewed_relationship",
		"security_signals",
		"created_at",
		"updated_at",
	}).AddRow(
		"33333333-3333-3333-3333-333333333333",
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		0,
		"fragment-1",
		[]byte(`[0]`),
		[]byte(`["fragment-1"]`),
		"fragment_only",
		"completed",
		"stored as fragment",
		"",
		"",
		"",
		"assertion-1",
		"RELATES_TO",
		"candidate",
		"active",
		"multi_state",
		"entailed",
		0.9,
		"",
		[]byte(`{}`),
		[]byte(`{}`),
		[]byte(`[]`),
		now,
		now,
	)

	mock.ExpectQuery(`(?s)SELECT ingest_id::text, profile_id::text.*FROM memory_placement_runs`).
		WithArgs("22222222-2222-2222-2222-222222222222", "11111111-1111-1111-1111-111111111111").
		WillReturnRows(runRows).
		RowsWillBeClosed()
	mock.ExpectQuery(`SELECT item_id::text, ingest_id::text, profile_id::text`).
		WithArgs("11111111-1111-1111-1111-111111111111").
		WillReturnRows(itemRows).
		RowsWillBeClosed()

	run, err := readPlacementRun(
		context.Background(),
		db,
		"WHERE profile_id = ? AND ingest_id = ?",
		"22222222-2222-2222-2222-222222222222",
		"11111111-1111-1111-1111-111111111111",
	)

	require.NoError(t, err)
	require.NotNil(t, run)
	require.Len(t, run.Items, 1)
	require.False(t, run.Evidence[0].TrustedAuthority)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppendTransitionEventsIsTransactionalAndValidatesLedgerRows(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewMemoryPlacementRepository(db, nil)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	event := domain.AssertionTransitionEvent{
		EventID: "11111111-1111-4111-8111-111111111111", ProfileID: "22222222-2222-4222-8222-222222222222",
		IngestID: "33333333-3333-4333-8333-333333333333", ItemID: "44444444-4444-4444-8444-444444444444",
		AssertionID: "assertion-1", EventType: "promoted", FromTier: domain.AssertionTierValidatedClaim, ToTier: domain.AssertionTierFact,
		FromStatus: domain.AssertionStatusActive, ToStatus: domain.AssertionStatusActive, ReasonCode: "two_source_fact_gate", Source: "server_verifier", OccurredAt: now,
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO assertion_transition_events`).
		WithArgs(event.EventID, event.ProfileID, event.IngestID, event.ItemID, event.AssertionID, event.EventType,
			string(event.FromTier), string(event.ToTier), string(event.FromStatus), string(event.ToStatus), event.ReasonCode, event.Source, now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.AppendTransitionEvents(context.Background(), []domain.AssertionTransitionEvent{event}))
	require.NoError(t, mock.ExpectationsWereMet())

	mock.ExpectBegin()
	mock.ExpectRollback()
	err = repo.AppendTransitionEvents(context.Background(), []domain.AssertionTransitionEvent{{EventID: event.EventID}})
	require.ErrorContains(t, err, "requires event_id, team_id, event_type, reason_code, and source")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCountAssertionTransitionsUsesExactWindowAndScope(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewMemoryPlacementRepository(db, nil)
	from := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	teamID := "11111111-1111-4111-8111-111111111111"
	actorProfileID := "22222222-2222-4222-8222-222222222222"

	for _, tc := range []struct {
		name           string
		teamID         string
		actorProfileID string
	}{
		{name: "system"},
		{name: "team", teamID: teamID},
		{name: "profile", teamID: teamID, actorProfileID: actorProfileID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)WITH filters AS.*FROM assertion_transition_events AS events.*runs.actor_profile_id`).
				WithArgs(tc.teamID, tc.actorProfileID, from, to).
				WillReturnRows(sqlmock.NewRows([]string{"event_type", "count"}).
					AddRow("proposed", int64(3)).
					AddRow("promoted", int64(2)))
			mock.ExpectCommit()

			counts, err := repo.CountAssertionTransitions(context.Background(), tc.teamID, tc.actorProfileID, from, to)
			require.NoError(t, err)
			require.Equal(t, map[string]int64{"proposed": 3, "promoted": 2}, counts)
		})
	}

	_, err = repo.CountAssertionTransitions(context.Background(), teamID, "", to, from)
	require.ErrorContains(t, err, "valid time window")
	_, err = repo.CountAssertionTransitions(context.Background(), "not-a-uuid", "", from, to)
	require.ErrorContains(t, err, "invalid team_id")
	_, err = repo.CountAssertionTransitions(context.Background(), teamID, "not-a-uuid", from, to)
	require.ErrorContains(t, err, "invalid actor_profile_id")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCountAssertionTransitionsPropagatesLedgerErrors(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewMemoryPlacementRepository(db, nil)
	from := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH filters AS.*FROM assertion_transition_events AS events`).
		WithArgs("", "", from, to).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()
	_, err = repo.CountAssertionTransitions(context.Background(), "", "", from, to)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimNextQueuedRunCanReclaimStartedStaleProcessingRun(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	now := time.Date(2026, 6, 27, 2, 45, 0, 0, time.UTC)
	ingestID := "11111111-1111-1111-1111-111111111111"
	profileID := "22222222-2222-2222-2222-222222222222"
	runRows := sqlmock.NewRows([]string{
		"ingest_id",
		"profile_id",
		"actor_profile_id",
		"actor_role",
		"status",
		"check_after_seconds",
		"status_tool",
		"pipeline_version",
		"evidence",
		"proposal",
		"review_tasks",
		"security",
		"migration_refs",
		"requires_acknowledgement",
		"error",
		"created_at",
		"updated_at",
		"started_at",
		"completed_at",
		"acknowledged_at",
	}).AddRow(
		ingestID,
		profileID,
		"",
		"",
		"processing",
		60,
		"get_memory_placement",
		"semantic-edge-v2",
		[]byte(`[{"index":0,"content":"memory"}]`),
		[]byte(`{}`),
		[]byte(`[]`),
		[]byte(`{}`),
		[]byte(`[]`),
		false,
		"",
		now.Add(-10*time.Minute),
		now,
		now,
		nil,
		nil,
	)
	itemRows := sqlmock.NewRows([]string{
		"item_id",
		"ingest_id",
		"profile_id",
		"evidence_index",
		"fragment_id",
		"evidence_indexes",
		"fragment_ids",
		"category",
		"status",
		"reason",
		"error",
		"claim_id",
		"fact_id",
		"assertion_id",
		"relationship_type",
		"tier",
		"assertion_status",
		"policy_family",
		"verifier_verdict",
		"verifier_confidence",
		"review_task_id",
		"proposed_relationship",
		"reviewed_relationship",
		"security_signals",
		"created_at",
		"updated_at",
	})

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH next AS.*status = 'queued'.*status = 'processing'.*started_at IS NOT NULL.*updated_at < now\(\) - interval '5 minutes'.*RETURNING run\.ingest_id::text`).
		WillReturnRows(sqlmock.NewRows([]string{"ingest_id"}).AddRow(ingestID)).
		RowsWillBeClosed()
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT ingest_id::text, profile_id::text.*FROM memory_placement_runs`).
		WithArgs(ingestID).
		WillReturnRows(runRows).
		RowsWillBeClosed()
	mock.ExpectQuery(`SELECT item_id::text, ingest_id::text, profile_id::text`).
		WithArgs(ingestID).
		WillReturnRows(itemRows).
		RowsWillBeClosed()
	mock.ExpectCommit()

	repo := NewMemoryPlacementRepository(db, nil)
	run, err := repo.ClaimNextQueuedRun(context.Background())

	require.NoError(t, err)
	require.NotNil(t, run)
	require.Equal(t, ingestID, run.IngestID)
	require.Equal(t, profileID, run.ProfileID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateDisputeWithRunLocksAndSavesInOneTransaction(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	now := time.Date(2026, 6, 27, 3, 0, 0, 0, time.UTC)
	disputeID := "44444444-4444-4444-4444-444444444444"
	ingestID := "11111111-1111-1111-1111-111111111111"
	profileID := "22222222-2222-2222-2222-222222222222"
	itemID := "33333333-3333-3333-3333-333333333333"

	disputeRows := sqlmock.NewRows([]string{
		"dispute_id",
		"profile_id",
		"ingest_id",
		"placement_item_id",
		"status",
		"turns",
		"final_reason",
		"created_at",
		"updated_at",
		"completed_at",
	}).AddRow(disputeID, profileID, ingestID, itemID, "open", []byte(`[]`), "", now, now, nil)
	runRows := sqlmock.NewRows([]string{
		"ingest_id",
		"profile_id",
		"actor_profile_id",
		"actor_role",
		"status",
		"check_after_seconds",
		"status_tool",
		"pipeline_version",
		"evidence",
		"proposal",
		"review_tasks",
		"security",
		"migration_refs",
		"requires_acknowledgement",
		"error",
		"created_at",
		"updated_at",
		"started_at",
		"completed_at",
		"acknowledged_at",
	}).AddRow(ingestID, profileID, "", "", "completed", 60, "get_memory_placement", "semantic-edge-v2", []byte(`[]`), []byte(`{}`), []byte(`[]`), []byte(`{}`), []byte(`[]`), false, "", now, now, nil, nil, nil)
	itemRows := sqlmock.NewRows([]string{
		"item_id",
		"ingest_id",
		"profile_id",
		"evidence_index",
		"fragment_id",
		"evidence_indexes",
		"fragment_ids",
		"category",
		"status",
		"reason",
		"error",
		"claim_id",
		"fact_id",
		"assertion_id",
		"relationship_type",
		"tier",
		"assertion_status",
		"policy_family",
		"verifier_verdict",
		"verifier_confidence",
		"review_task_id",
		"proposed_relationship",
		"reviewed_relationship",
		"security_signals",
		"created_at",
		"updated_at",
	}).AddRow(itemID, ingestID, profileID, 0, "fragment-1", []byte(`[0]`), []byte(`["fragment-1"]`), "fragment_only", "completed", "", "", "", "", "", "", "", "", "", "", 0.0, "", []byte(`{}`), []byte(`{}`), []byte(`[]`), now, now)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT dispute_id::text, profile_id::text, ingest_id::text.*FROM memory_dispute_sessions.*LIMIT 1 FOR UPDATE`).
		WithArgs(profileID, disputeID).
		WillReturnRows(disputeRows).
		RowsWillBeClosed()
	mock.ExpectQuery(`(?s)SELECT ingest_id::text, profile_id::text.*FROM memory_placement_runs.*LIMIT 1 FOR UPDATE`).
		WithArgs(profileID, ingestID).
		WillReturnRows(runRows).
		RowsWillBeClosed()
	mock.ExpectQuery(`(?s)SELECT item_id::text, ingest_id::text, profile_id::text.*FROM memory_placement_items.*FOR UPDATE`).
		WithArgs(ingestID).
		WillReturnRows(itemRows).
		RowsWillBeClosed()
	mock.ExpectExec(`UPDATE memory_dispute_sessions`).
		WithArgs(
			string(domain.MemoryDisputeOpen),
			sqlmock.AnyArg(),
			"updated",
			sqlmock.AnyArg(),
			nil,
			disputeID,
			profileID,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE memory_placement_runs`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE memory_placement_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repo := NewMemoryPlacementRepository(db, nil)
	session, run, err := repo.UpdateDisputeWithRun(context.Background(), profileID, disputeID, func(session *domain.MemoryDisputeSession, run *domain.MemoryPlacementRun) error {
		session.FinalReason = "updated"
		run.Items[0].Category = domain.MemoryPlacementNeedsEvidence
		run.Items[0].Reason = "updated"
		return nil
	})

	require.NoError(t, err)
	require.NotNil(t, session)
	require.NotNil(t, run)
	require.Equal(t, "updated", session.FinalReason)
	require.Equal(t, domain.MemoryPlacementNeedsEvidence, run.Items[0].Category)
	require.NoError(t, mock.ExpectationsWereMet())
}
