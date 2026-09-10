package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func feedbackQueryRow() []driver.Value {
	return []driver.Value{
		"rec_test", recallTime, recallTime, recallTime,
		recallTeam, recallID1, recallID2, recallSpace, int64(4),
		"api_key", "recall_memory", "query", []byte(`{"limit":2}`), []byte(`[{"type":"fragment","id":"e1","rank":1}]`),
		int64(1), "captured", "canonical", "ranking-v1", "embedding-v1", "search-v1", "current",
		[]byte(`{"code":"vector_unavailable"}`), []byte(`{"schema":"v1"}`), true, false, "high", false, true, "useful",
		[]byte(`[{"type":"fragment","id":"e1","rank":1}]`), []byte(`[{"dream_id":"h1","quality":"low","used":false}]`),
	}
}

func feedbackQueryRows(values ...[]driver.Value) *sqlmock.Rows {
	rows := sqlmock.NewRows(strings.Split(recallFeedbackEventColumns(), ","))
	for _, value := range values {
		rows.AddRow(value...)
	}
	return rows
}

func TestFeedbackStoreSnapshotBindsIdentityAndOrderedReferences(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	team, profile, key, space := uuid.MustParse(recallTeam), uuid.MustParse(recallID1), uuid.MustParse(recallID2), uuid.MustParse(recallSpace)
	event := domain.RecallFeedbackEvent{
		RecallID: " rec_test ", CreatedAt: recallTime, UpdatedAt: recallTime,
		TeamID: &team, ProfileID: &profile, KeyID: &key, SpaceID: &space, SpaceGeneration: 4,
		AuthMethod: " api_key ", Query: " query ", ToolArgs: map[string]any{"limit": 2}, ResultCount: 99,
		ResultRefs:      []domain.RecallFeedbackResultRef{{Type: "fragment", ID: "second", Rank: 1}, {Type: "relationship", ID: "first", Rank: 2}},
		ContractVersion: "canonical", SearchState: "current",
	}
	refs, err := json.Marshal(event.ResultRefs)
	require.NoError(t, err)
	mock.ExpectExec(`(?s)INSERT INTO recall_feedback_events.*ON CONFLICT.*space_generation = EXCLUDED.space_generation`).WithArgs(
		"rec_test", recallTime, recallTime, recallTeam, recallID1, recallID2, recallSpace, int64(4), "api_key", "recall_memory", "query",
		`{"limit":2}`, string(refs), 2, "captured", "canonical", "", "", "", "current", "{}", "{}",
	).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, NewFeedbackStore(db, recallQueryRLS{}).RecordSnapshot(t.Context(), event))
}

func TestFeedbackStoreWriteFailuresRemainTyped(t *testing.T) {
	for _, method := range []string{"snapshot", "feedback"} {
		for _, failure := range []string{"query", "not_found"} {
			t.Run(method+"/"+failure, func(t *testing.T) {
				db, mock := newRecallSQLMockDB(t)
				query := "INSERT INTO recall_feedback_events"
				if method == "feedback" {
					query = "UPDATE recall_feedback_events"
				}
				expect := mock.ExpectExec(query)
				want := errRecallDB
				if failure == "query" {
					expect.WillReturnError(want)
				} else {
					expect.WillReturnResult(sqlmock.NewResult(0, 0))
					want = ErrRecallFeedbackEventNotFound
				}
				store := NewFeedbackStore(db, recallQueryRLS{})
				write := store.RecordSnapshot
				if method == "feedback" {
					write = store.RecordFeedback
				}
				require.ErrorIs(t, write(t.Context(), domain.RecallFeedbackEvent{RecallID: "rec_test"}), want)
			})
		}
	}
}

func TestFeedbackStoreFeedbackPreservesNullableJudgmentsAndFence(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	team, space := uuid.MustParse(recallTeam), uuid.MustParse(recallSpace)
	used, unsupported := true, false
	mock.ExpectExec(`(?s)UPDATE recall_feedback_events.*WHERE recall_id = \$1 AND team_id = \$14 AND space_id = \$15 AND space_generation = \$16`).WithArgs(
		"rec_test", recallTime, recallTime, nil, "", true, false, "high", nil, nil, "useful", "[]", "[]", recallTeam, recallSpace, int64(4),
	).WillReturnResult(sqlmock.NewResult(0, 1))
	err := NewFeedbackStore(db, recallQueryRLS{}).RecordFeedback(t.Context(), domain.RecallFeedbackEvent{
		RecallID: "rec_test", CreatedAt: recallTime, UpdatedAt: recallTime, FeedbackAt: &recallTime,
		TeamID: &team, SpaceID: &space, SpaceGeneration: 4, Used: &used, AnswerSupported: &unsupported, Quality: " HIGH ", FeedbackComment: " useful ",
	})
	require.NoError(t, err)
}

func TestFeedbackStoreRejectsUnencodablePayloadBeforeWriting(t *testing.T) {
	for _, field := range []string{"tool_args", "degradation", "snapshot_metadata", "result_refs"} {
		t.Run(field, func(t *testing.T) {
			db, _ := newRecallSQLMockDB(t)
			event := domain.RecallFeedbackEvent{RecallID: "rec_test"}
			bad := map[string]any{"invalid": math.NaN()}
			switch field {
			case "tool_args":
				event.ToolArgs = bad
			case "degradation":
				event.Degradation = bad
			case "snapshot_metadata":
				event.SnapshotMetadata = bad
			case "result_refs":
				score := math.NaN()
				event.ResultRefs = []domain.RecallFeedbackResultRef{{Score: &score}}
			}
			store := NewFeedbackStore(db, recallQueryRLS{})
			require.Error(t, store.RecordSnapshot(t.Context(), event))
			require.Error(t, store.RecordFeedback(t.Context(), event))
		})
	}
}

func TestFeedbackStoreListBindsFiltersAndHydratesFullEvent(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	team, profile := uuid.MustParse(recallTeam), uuid.MustParse(recallID1)
	missing, irrelevant := true, false
	until := recallTime.Add(time.Hour)
	filter := domain.RecallFeedbackEventFilter{TeamID: &team, ProfileID: &profile, Quality: " HIGH ", MissingContext: &missing, Irrelevant: &irrelevant, From: &recallTime, To: &until, Limit: 600, Offset: -5}
	where := `(?s)FROM recall_feedback_events.*team_id = \$1 AND profile_id = \$2 AND quality = \$3 AND missing_context IS TRUE AND irrelevant IS FALSE AND created_at >= \$4 AND created_at <= \$5`
	mock.ExpectQuery(`SELECT count\(\*\) `+where).WithArgs(recallTeam, recallID1, "high", recallTime, until).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(where+`.*ORDER BY created_at DESC, recall_id DESC LIMIT \$6 OFFSET \$7`).WithArgs(recallTeam, recallID1, "high", recallTime, until, 500, 0).WillReturnRows(feedbackQueryRows(feedbackQueryRow())).RowsWillBeClosed()
	page, err := NewFeedbackStore(db, recallQueryRLS{}).List(t.Context(), filter)
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Len(t, page.Items, 1)
	event := page.Items[0]
	require.Equal(t, &team, event.TeamID)
	require.Equal(t, &profile, event.ProfileID)
	require.Equal(t, uuid.MustParse(recallID2), *event.KeyID)
	require.Equal(t, uuid.MustParse(recallSpace), *event.SpaceID)
	require.EqualValues(t, 4, event.SpaceGeneration)
	require.Equal(t, &recallTime, event.FeedbackAt)
	require.True(t, *event.Used)
	require.False(t, *event.AnswerSupported)
	require.Equal(t, "e1", event.ResultRefs[0].ID)
	require.Equal(t, 1, event.ResultRefs[0].Rank)
	require.Equal(t, "h1", event.DreamFeedback[0].DreamID)
	require.Equal(t, "v1", event.SnapshotMetadata["schema"])
}

func TestFeedbackStoreReadFailuresDoNotReturnPartialPages(t *testing.T) {
	for _, failure := range []string{"count", "query", "scan", "iteration", "metadata_json", "degradation_json"} {
		t.Run(failure, func(t *testing.T) {
			db, mock := newRecallSQLMockDB(t)
			count := mock.ExpectQuery(`SELECT count\(\*\)`)
			if failure == "count" {
				count.WillReturnError(errRecallDB)
			} else {
				count.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
				query := mock.ExpectQuery("FROM recall_feedback_events")
				if failure == "query" {
					query.WillReturnError(errRecallDB)
				} else {
					row := feedbackQueryRow()
					switch failure {
					case "scan":
						row[14] = "invalid count"
					case "metadata_json":
						row[22] = []byte("{")
					case "degradation_json":
						row[21] = []byte("{")
					}
					rows := feedbackQueryRows(feedbackQueryRow(), row)
					if failure == "iteration" {
						rows.RowError(1, errRecallDB)
					}
					query.WillReturnRows(rows).RowsWillBeClosed()
				}
			}
			page, err := NewFeedbackStore(db, recallQueryRLS{}).List(t.Context(), domain.RecallFeedbackEventFilter{})
			require.Error(t, err)
			require.Nil(t, page)
		})
	}
}

func TestFeedbackStoreGetAndPrune(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	store := NewFeedbackStore(db, nil)
	empty, err := store.Get(t.Context(), " ")
	require.NoError(t, err)
	require.Nil(t, empty)
	for _, found := range []bool{false, true} {
		mock.ExpectBegin()
		rows := feedbackQueryRows()
		if found {
			rows.AddRow(feedbackQueryRow()...)
		}
		mock.ExpectQuery(`(?s)FROM recall_feedback_events WHERE recall_id = \$1.*recall_feedback_events.space_generation = dense_mem_active_space_generation.*LIMIT 1`).WithArgs("rec_test").WillReturnRows(rows).RowsWillBeClosed()
		mock.ExpectCommit()
		got, err := store.Get(t.Context(), " rec_test ")
		require.NoError(t, err)
		if found {
			require.Equal(t, "rec_test", got.RecallID)
		} else {
			require.Nil(t, got)
		}
	}
	for _, fails := range []bool{false, true} {
		mock.ExpectBegin()
		exec := mock.ExpectExec(`DELETE FROM recall_feedback_events WHERE created_at < \$1`).WithArgs(recallTime)
		if fails {
			exec.WillReturnError(errRecallDB)
			mock.ExpectRollback()
		} else {
			exec.WillReturnResult(sqlmock.NewResult(0, 3))
			mock.ExpectCommit()
		}
		err := store.PruneBefore(t.Context(), recallTime.In(time.FixedZone("offset", 3600)))
		if fails {
			require.ErrorIs(t, err, errRecallDB)
		} else {
			require.NoError(t, err)
		}
	}
}

func TestFeedbackStoreGetPropagatesDatabaseAndDecodeErrors(t *testing.T) {
	for _, decode := range []bool{false, true} {
		db, mock := newRecallSQLMockDB(t)
		query := mock.ExpectQuery("FROM recall_feedback_events")
		if decode {
			row := feedbackQueryRow()
			row[13] = []byte("{")
			query.WillReturnRows(feedbackQueryRows(row))
		} else {
			query.WillReturnError(errRecallDB)
		}
		got, err := NewFeedbackStore(db, recallQueryRLS{}).Get(t.Context(), "rec_test")
		require.Error(t, err)
		require.Nil(t, got)
	}
}
