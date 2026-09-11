package postgres

import (
	"database/sql/driver"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func conflictCaseRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"team_id", "conflict_id", "space_id", "space_generation", "status", "version", "preferred_position_id", "resolved_at", "resolution_reason", "created_at", "updated_at"})
}

func conflictPositionRows(id string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"conflict_id", "position_id", "position_key", "canonical_evidence_id", "canonical_owner_profile_id", "occurrence_id", "occurrence_owner_profile_id", "quote", "span_start", "span_end", "authority", "submitted", "created_at"}).
		AddRow(id, recallID1, "key1", recallID1, recallID2, recallID3, recallID2, "quote", 0, 5, "primary", true, recallTime)
}

func conflictEventRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"event_id", "conflict_id", "ordinal", "action", "status_after", "case_version", "actor_kind", "actor_id", "reason", "preferred_position_id", "citation_snapshot", "created_at"})
}

func addConflictEvent(rows *sqlmock.Rows, id, action, status string, ordinal int, at time.Time, snapshot string) *sqlmock.Rows {
	return rows.AddRow(id, recallID1, ordinal, action, status, ordinal, "profile", recallID2, "reason "+id, recallID3, []byte(snapshot), at)
}

func expectConflictCase(mock sqlmock.Sqlmock, id, status string, at time.Time) {
	mock.ExpectQuery(`(?s)SELECT team_id::text.*FROM evidence_conflict_cases WHERE team_id = \$1::uuid AND conflict_id = \$2::uuid`).WithArgs(recallTeam, id).
		WillReturnRows(conflictCaseRows().AddRow(recallTeam, id, recallSpace, int64(4), status, 9, recallID3, at, "current reason", recallTime, at))
	mock.ExpectQuery(`(?s)FROM evidence_conflict_positions WHERE team_id = \$1::uuid AND conflict_id = \$2::uuid ORDER BY position_key, position_id`).WithArgs(recallTeam, id).
		WillReturnRows(conflictPositionRows(id)).RowsWillBeClosed()
}

func TestRecallConflictHydrationPreservesOrderLimitsAndSkipsIneligibleCases(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	input := RecallEvidenceInput{TeamID: recallTeam, SpaceID: recallSpace, SpaceKind: "profile_private"}
	got, err := LoadRecallEvidenceConflictRecords(t.Context(), db, input, []RecallEvidenceHit{{EvidenceID: " "}})
	require.NoError(t, err)
	require.Empty(t, got)
	ids := sqlmock.NewRows([]string{"conflict_id"})
	for i := 0; i < 24; i++ {
		ids.AddRow(fmt.Sprintf("00000000-0000-4000-8000-%012d", i))
	}
	mock.ExpectQuery(`(?s)SELECT conflict.conflict_id::text.*position.canonical_evidence_id = ANY\(\$2::uuid\[\]\).*conflict.space_id = '`+recallSpace+`'.*NOT EXISTS.*conflict.status IN \('open', 'resolved'\).*LIMIT \$11`).
		WithArgs(recallTeam, pq.Array([]string{recallID1}), nil, nil, nil, nil, nil, nil, nil, nil, EvidenceConflictRecallCandidateLimit).WillReturnRows(ids).RowsWillBeClosed()
	for i := 0; i < 24; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		if i == 0 {
			mock.ExpectQuery("FROM evidence_conflict_cases WHERE").WithArgs(recallTeam, id).WillReturnRows(conflictCaseRows())
			continue
		}
		status := "open"
		if i == 1 {
			status = "dismissed"
		}
		at := recallTime
		if i == 23 {
			at = at.Add(time.Minute)
		}
		expectConflictCase(mock, id, status, at)
		count := 1
		if i == 2 {
			count = 0
		}
		mock.ExpectQuery(`(?s)SELECT count\(\*\)::int FROM evidence_conflict_positions AS position.*occurrence.canonical_owner_profile_id = fragment.owner_profile_id`).
			WithArgs(recallTeam, id, nil, nil, nil, nil, nil, nil, nil, nil).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
	}
	got, err = LoadRecallEvidenceConflictRecords(t.Context(), db, input, []RecallEvidenceHit{{EvidenceID: " " + recallID1 + " "}, {EvidenceID: recallID1}})
	require.NoError(t, err)
	require.Len(t, got, EvidenceConflictMaxResults)
	require.Equal(t, "00000000-0000-4000-8000-000000000023", got[0].ConflictID)
	require.Equal(t, "00000000-0000-4000-8000-000000000022", got[1].ConflictID)
	require.Equal(t, "00000000-0000-4000-8000-000000000004", got[19].ConflictID)
	require.Equal(t, "evidence_conflict", got[0].Kind)
	require.Equal(t, "quote", got[0].Positions[0].Quote)
}

func TestRecallConflictHistoricalStateUsesBoundedEvents(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	known := recallTime.Add(time.Hour)
	input := RecallEvidenceInput{TeamID: recallTeam, KnownAt: &known}
	args := []driver.Value{recallTeam, pq.Array([]string{recallID1}), known, known, known, known, known, known, known, known, known, known, known, EvidenceConflictRecallCandidateLimit}
	mock.ExpectQuery(`(?s)SELECT conflict.conflict_id::text.*event.status_after.*ORDER BY \(SELECT event.created_at.*LIMIT \$14`).WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(recallID1))
	expectConflictCase(mock, recallID1, "dismissed", known.Add(time.Hour))
	resolved := recallTime.Add(10 * time.Minute)
	updated := recallTime.Add(20 * time.Minute)
	mock.ExpectQuery(`(?s)FROM evidence_conflict_events.*ORDER BY ordinal DESC.*LIMIT 1`).WithArgs(recallTeam, recallID1, known).
		WillReturnRows(addConflictEvent(conflictEventRows(), recallID2, "recurred", "open", 5, updated, `[{"quote":"historical quote"}]`))
	mock.ExpectQuery(`(?s)FROM evidence_conflict_events.*action IN \('resolved', 'dismissed'\).*LIMIT 1`).WithArgs(recallTeam, recallID1, known).
		WillReturnRows(addConflictEvent(conflictEventRows(), recallID3, "resolved", "resolved", 3, resolved, "null"))
	mock.ExpectQuery(`SELECT count\(\*\)::int`).WithArgs(recallTeam, recallID1, known, known, known, known, known, known, known, known).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	got, err := LoadRecallEvidenceConflictRecords(t.Context(), db, input, []RecallEvidenceHit{{EvidenceID: recallID1}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "open", got[0].Status)
	require.Equal(t, 5, got[0].Version)
	require.Equal(t, updated, got[0].UpdatedAt)
	require.Equal(t, &resolved, got[0].ResolvedAt)
	require.Equal(t, "reason "+recallID3, got[0].ResolutionReason)
}

func TestRecallConflictHistoricalEventsDeduplicateAndDecode(t *testing.T) {
	for _, state := range []string{"duplicate", "empty", "invalid_json", "query_error"} {
		t.Run(state, func(t *testing.T) {
			db, mock := newRecallSQLMockDB(t)
			for i := 0; i < 2; i++ {
				q := mock.ExpectQuery("FROM evidence_conflict_events").WithArgs(recallTeam, recallID1, recallTime)
				if state == "query_error" {
					q.WillReturnError(errRecallDB)
					break
				}
				rows := conflictEventRows()
				if state != "empty" {
					snapshot := `[{"quote":"a quote"}]`
					if state == "invalid_json" {
						snapshot = "{"
					}
					addConflictEvent(rows, recallID2, "resolved", "resolved", 2, recallTime, snapshot)
				}
				q.WillReturnRows(rows)
				if state == "invalid_json" {
					break
				}
			}
			got, err := LoadRecallEvidenceConflictEventsAt(t.Context(), db, recallTeam, recallID1, &recallTime)
			if state == "query_error" || state == "invalid_json" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if state == "empty" {
				require.Empty(t, got)
			} else {
				require.Len(t, got, 1)
				require.Equal(t, "a quote", got[0].CitationSnapshot[0].Quote)
			}
		})
	}
}

func TestRecallConflictMissingHistoryDoesNotExposeCurrentState(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	expectConflictCase(mock, recallID1, "resolved", recallTime)
	for i := 0; i < 2; i++ {
		mock.ExpectQuery("FROM evidence_conflict_events").WillReturnRows(conflictEventRows())
	}
	got, err := LoadRecallEvidenceConflictCase(t.Context(), db, recallTeam, recallID1, &recallTime)
	require.ErrorIs(t, err, ErrEvidenceConflictNotFound)
	require.Nil(t, got)
	authorized, err := evidenceConflictCasePositionsAuthorized(t.Context(), db, RecallEvidenceInput{TeamID: recallTeam}, EvidenceConflictCaseRecord{})
	require.NoError(t, err)
	require.False(t, authorized)
}

func TestRecallConflictHydrationReadErrorsRemainVisible(t *testing.T) {
	for _, stage := range []string{"candidate_query", "candidate_scan", "candidate_iteration", "case", "positions_query", "positions_scan", "positions_iteration", "history", "authorization"} {
		t.Run(stage, func(t *testing.T) {
			db, mock := newRecallSQLMockDB(t)
			input := RecallEvidenceInput{TeamID: recallTeam}
			q := mock.ExpectQuery("SELECT conflict.conflict_id::text")
			if stage == "candidate_query" {
				q.WillReturnError(errRecallDB)
			} else {
				row := driver.Value(recallID1)
				if stage == "candidate_scan" {
					row = nil
				}
				rows := sqlmock.NewRows([]string{"id"}).AddRow(row)
				if stage == "candidate_iteration" {
					rows.RowError(0, errRecallDB)
				}
				q.WillReturnRows(rows).RowsWillBeClosed()
				if stage != "candidate_scan" && stage != "candidate_iteration" {
					q = mock.ExpectQuery("FROM evidence_conflict_cases WHERE")
					if stage == "case" {
						q.WillReturnError(errRecallDB)
					} else {
						q.WillReturnRows(conflictCaseRows().AddRow(recallTeam, recallID1, recallSpace, 4, "open", 1, "", nil, "", recallTime, recallTime))
						q = mock.ExpectQuery("FROM evidence_conflict_positions WHERE")
						if stage == "positions_query" {
							q.WillReturnError(errRecallDB)
						} else {
							rows = conflictPositionRows(recallID1)
							if stage == "positions_scan" {
								rows.AddRow(recallID1, recallID2, "key", recallID1, recallID1, recallID1, recallID1, "quote", "bad", 5, "primary", true, recallTime)
							}
							if stage == "positions_iteration" {
								rows.RowError(0, errRecallDB)
							}
							q.WillReturnRows(rows).RowsWillBeClosed()
							if stage == "history" {
								input.KnownAt = &recallTime
								mock.ExpectQuery("FROM evidence_conflict_events").WillReturnError(errRecallDB)
							}
							if stage == "authorization" {
								mock.ExpectQuery(`SELECT count\(\*\)::int`).WillReturnError(errRecallDB)
							}
						}
					}
				}
			}
			got, err := LoadRecallEvidenceConflictRecords(t.Context(), db, input, []RecallEvidenceHit{{EvidenceID: recallID1}})
			require.Error(t, err)
			require.Nil(t, got)
			if stage != "candidate_scan" && stage != "positions_scan" {
				require.ErrorIs(t, err, errRecallDB)
			}
		})
	}
}
