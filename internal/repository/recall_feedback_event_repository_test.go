package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestScanRecallFeedbackEventRejectsMalformedToolArgsJSON(t *testing.T) {
	rows := recallFeedbackEventRows(t, []byte("{bad-json"), []byte("[]"), []byte("[]"), []byte("[]"))
	_, err := scanRecallFeedbackEvent(rows)
	require.ErrorContains(t, err, "invalid recall_feedback_events.tool_args JSON")
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
	require.Equal(t, "canonical", got.ContractVersion)
	require.Equal(t, "current", got.SearchState)
	require.Equal(t, map[string]any{"code": "vector_unavailable"}, got.Degradation)
	require.Equal(t, map[string]any{"result_schema": "v2.evidence_relationship_refs.v1"}, got.SnapshotMetadata)
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
		"space_id",
		"space_generation",
		"auth_method",
		"tool_name",
		"query",
		"tool_args",
		"result_refs",
		"result_count",
		"snapshot_state",
		"contract_version",
		"ranking_profile_version",
		"embedding_contract_version",
		"search_index_profile_version",
		"search_state",
		"degradation",
		"snapshot_metadata",
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
		nil,
		nil,
		"api_key",
		"recall_memory",
		"query",
		toolArgs,
		resultRefs,
		0,
		"captured",
		"canonical",
		"",
		"",
		"",
		"current",
		[]byte(`{"code":"vector_unavailable"}`),
		[]byte(`{"result_schema":"v2.evidence_relationship_refs.v1"}`),
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
