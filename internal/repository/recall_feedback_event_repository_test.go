package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestScanRecallFeedbackEventRejectsMalformedToolArgsJSON(t *testing.T) {
	rows := recallFeedbackEventRows(t, []byte("{bad-json"), []byte("[]"), []byte("[]"), []byte("[]"))
	_, err := scanRecallFeedbackEvent(rows)
	require.ErrorContains(t, err, "invalid recall_feedback_events.tool_args JSON")
}

func TestRecallMemoryReviewQueuePersistsAndReadsOrderedReviews(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewRecallFeedbackEventRepository(db, nil)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	teamID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	review := domain.RecallMemoryReview{
		ReviewID: "11111111-1111-4111-8111-111111111111", ProfileID: teamID, RecallID: "recall-1",
		KnowledgeType: domain.RecallFeedbackResultTypeAssertion, KnowledgeID: "assertion-1",
		Reasons: []string{"irrelevant_result", "unsupported_answer"}, FeedbackComment: "wrong relation", CreatedAt: now, UpdatedAt: now,
	}

	require.NoError(t, repo.EnqueueRecallMemoryReviews(context.Background(), nil))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO recall_memory_review_queue`).
		WithArgs(review.ReviewID, review.ProfileID, review.RecallID, review.KnowledgeType, review.KnowledgeID,
			`["irrelevant_result","unsupported_answer"]`, review.FeedbackComment, "pending", now, now, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.EnqueueRecallMemoryReviews(context.Background(), []domain.RecallMemoryReview{review}))

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT review_id::text, profile_id::text, recall_id.*FROM recall_memory_review_queue`).
		WithArgs(teamID.String(), "recall-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"review_id", "profile_id", "recall_id", "knowledge_type", "knowledge_id", "reasons", "feedback_comment", "status", "created_at", "updated_at", "resolved_at",
		}).AddRow(review.ReviewID, teamID.String(), review.RecallID, review.KnowledgeType, review.KnowledgeID,
			[]byte(`["irrelevant_result","unsupported_answer"]`), review.FeedbackComment, "pending", now, now, nil)).
		RowsWillBeClosed()
	mock.ExpectCommit()
	got, err := repo.ListRecallMemoryReviews(context.Background(), " "+teamID.String()+" ", " recall-1 ")
	require.NoError(t, err)
	require.Equal(t, []domain.RecallMemoryReview{{
		ReviewID: review.ReviewID, ProfileID: teamID, RecallID: review.RecallID, KnowledgeType: review.KnowledgeType,
		KnowledgeID: review.KnowledgeID, Reasons: review.Reasons, FeedbackComment: review.FeedbackComment,
		Status: "pending", CreatedAt: now, UpdatedAt: now,
	}}, got)
	require.NoError(t, mock.ExpectationsWereMet())

	empty, err := repo.ListRecallMemoryReviews(context.Background(), teamID.String(), "")
	require.NoError(t, err)
	require.Empty(t, empty)
	empty, err = repo.ListRecallMemoryReviews(context.Background(), "", "recall-1")
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestRecallMemoryReviewQueueRejectsMalformedStoredReasons(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewRecallFeedbackEventRepository(db, nil)
	now := time.Now().UTC()

	profileID := uuid.NewString()
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM recall_memory_review_queue`).WithArgs(profileID, "recall-1").WillReturnRows(sqlmock.NewRows([]string{
		"review_id", "profile_id", "recall_id", "knowledge_type", "knowledge_id", "reasons", "feedback_comment", "status", "created_at", "updated_at", "resolved_at",
	}).AddRow("review-1", uuid.NewString(), "recall-1", "fragment", "fragment-1", []byte(`{`), "", "pending", now, now, nil))
	mock.ExpectRollback()
	_, err = repo.ListRecallMemoryReviews(context.Background(), profileID, "recall-1")
	require.ErrorContains(t, err, "invalid recall memory review reasons JSON")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScanRecallFeedbackEventRejectsMalformedResultRefsJSON(t *testing.T) {
	rows := recallFeedbackEventRows(t, []byte("{}"), []byte("{bad-json"), []byte("[]"), []byte("[]"))
	_, err := scanRecallFeedbackEvent(rows)
	require.ErrorContains(t, err, "invalid recall_feedback_events.result_refs JSON")
}

func TestScanRecallFeedbackEventRejectsMalformedIrrelevantResultRefsJSON(t *testing.T) {
	rows := recallFeedbackEventRows(t, []byte("{}"), []byte("[]"), []byte("{bad-json"), []byte("[]"))
	_, err := scanRecallFeedbackEvent(rows)
	require.ErrorContains(t, err, "invalid recall_feedback_events.irrelevant_result_refs JSON")
}

func TestScanRecallFeedbackEventRejectsMalformedDreamFeedbackJSON(t *testing.T) {
	rows := recallFeedbackEventRows(t, []byte("{}"), []byte("[]"), []byte("[]"), []byte("{bad-json"))
	_, err := scanRecallFeedbackEvent(rows)
	require.ErrorContains(t, err, "invalid recall_feedback_events.dream_feedback JSON")
}

func TestScanRecallFeedbackEventReadsFeedbackComment(t *testing.T) {
	rows := recallFeedbackEventRows(t, []byte("{}"), []byte("[]"), []byte(`[{"type":"fragment","id":"fragment-1","rank":1}]`), []byte(`[{"dream_id":"dream-1","used":true,"quality":"medium","contradicted":false,"feedback_comment":"plausible but weak"}]`))
	got, err := scanRecallFeedbackEvent(rows)
	require.NoError(t, err)
	require.Equal(t, "knowledge explorer listbox pattern was missing", got.FeedbackComment)
	require.Equal(t, []domain.RecallFeedbackJudgedResultRef{{
		Type: domain.RecallFeedbackResultTypeFragment,
		ID:   "fragment-1",
		Rank: 1,
	}}, got.IrrelevantRefs)
	require.Equal(t, []domain.RecallFeedbackDreamFeedback{{
		DreamID:         "dream-1",
		Used:            true,
		Quality:         "medium",
		Contradicted:    false,
		FeedbackComment: "plausible but weak",
	}}, got.DreamFeedback)
}

func TestRecallFeedbackEventWhereExcludesPendingByDefault(t *testing.T) {
	where, args := recallFeedbackEventWhere(domain.RecallFeedbackEventFilter{})
	require.Contains(t, where, "quality <> ''")
	require.Empty(t, args)

	where, args = recallFeedbackEventWhere(domain.RecallFeedbackEventFilter{IncludePending: true})
	require.NotContains(t, where, "quality <> ''")
	require.Empty(t, args)

	where, args = recallFeedbackEventWhere(domain.RecallFeedbackEventFilter{Quality: "low"})
	require.Contains(t, where, "quality = ?")
	require.NotContains(t, where, "quality <> ''")
	require.Equal(t, []any{"low"}, args)
}

func recallFeedbackEventRows(t *testing.T, toolArgs []byte, resultRefs []byte, irrelevantRefs []byte, dreamFeedback []byte) *sql.Rows {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	rows := sqlmock.NewRows([]string{
		"recall_id",
		"created_at",
		"updated_at",
		"feedback_at",
		"team_id",
		"profile_id",
		"key_id",
		"auth_method",
		"tool_name",
		"query",
		"tool_args",
		"result_refs",
		"result_count",
		"snapshot_state",
		"used",
		"answer_supported",
		"quality",
		"missing_context",
		"irrelevant",
		"feedback_comment",
		"irrelevant_result_refs",
		"dream_feedback",
	}).AddRow(
		"rec_1",
		time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 23, 12, 1, 0, 0, time.UTC),
		nil,
		nil,
		nil,
		nil,
		"api_key",
		"recall_memory",
		"query",
		toolArgs,
		resultRefs,
		0,
		"captured",
		nil,
		nil,
		"",
		nil,
		nil,
		"knowledge explorer listbox pattern was missing",
		irrelevantRefs,
		dreamFeedback,
	)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)
	sqlRows, err := db.QueryContext(context.Background(), "SELECT")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlRows.Close() })
	require.True(t, sqlRows.Next())
	require.NoError(t, mock.ExpectationsWereMet())
	return sqlRows
}
