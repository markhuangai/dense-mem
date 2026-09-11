package postgres

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func evidenceRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"evidence_id", "context", "source", "source_type", "created_at", "relationship_ids", "search_state"})
}

func relationshipRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"team_id", "relationship_id", "semantic_group_key", "subject_entity_id", "subject_name", "predicate_key", "object_entity_id", "object_value_id", "object_name", "object_value_type", "object_value", "polarity", "scope_key", "valid_from", "search_state", "support_count", "source_group_count", "created_at"})
}

func relationshipRow(id, group, state string, created time.Time) []driver.Value {
	return []driver.Value{recallTeam, id, group, recallID3, "Subject", "uses", recallID2, "", "Object", "", "", "+", "", nil, state, 2, 1, created}
}

func TestRecallEvidenceFusesBranchesSkipsUnhydratedAndKnownIDs(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	text := recallHitRows()
	for _, id := range []string{recallSpace, recallID3, recallID1, recallID2} {
		addRecallHit(text, "evidence", id, "current")
	}
	vector := recallHitRows()
	for _, id := range []string{recallSpace, recallID2, recallID1} {
		addRecallHit(vector, "evidence", id, "current")
	}
	expand := addRecallHit(addRecallHit(recallHitRows(), "evidence", recallSpace, "current"), "evidence", recallID2, "pending")
	mock.ExpectQuery(`(?s)FROM search_documents.*plainto_tsquery`).WillReturnRows(text)
	mock.ExpectQuery(`(?s)FROM search_documents.*embedding IS NOT NULL`).WillReturnRows(vector)
	mock.ExpectQuery(`(?s)FROM relationship_records AS relationship.*JOIN search_documents`).WillReturnRows(expand)
	mock.ExpectQuery("WITH eligible AS NOT MATERIALIZED").WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("current"))
	mock.ExpectQuery(`(?s)WITH requested.*SELECT evidence_id`).WillReturnRows(evidenceRows().
		AddRow(recallID1, " evidence A ", "source A", "document", recallTime, pq.StringArray{recallID3}, "current").
		AddRow(recallID2, "evidence B", "source B", "manual", recallTime, pq.StringArray{}, "failed")).RowsWillBeClosed()
	store := NewStore(db, recallQueryRLS{}, recallSearchContract{contract: recallTestContract()},
		func(ctx context.Context, _ *gorm.DB, team string, known *time.Time, hits []RecallEvidenceHit) ([]RelationshipConflictCaseRecord, error) {
			require.Equal(t, recallTeam, team)
			require.Equal(t, &recallTime, known)
			require.Equal(t, []string{recallID2, recallID1}, []string{hits[0].EvidenceID, hits[1].EvidenceID})
			return []RelationshipConflictCaseRecord{{ConflictID: recallID3}}, nil
		}, func(context.Context, *gorm.DB, RecallEvidenceInput, []RecallEvidenceHit) ([]EvidenceConflictCaseRecord, error) {
			return []EvidenceConflictCaseRecord{{ConflictID: recallID1}}, nil
		})
	result, err := store.RecallEvidence(t.Context(), RecallEvidenceInput{TeamID: recallTeam, Query: "query", QueryEmbedding: []float32{1, 0}, ExpandFromEntityIDs: []string{recallID3}, KnownEvidenceIDs: []string{recallID3}, KnownAt: &recallTime, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, "failed", result.SearchState)
	require.Equal(t, recallTeam, result.TeamID)
	require.Equal(t, 2, len(result.Results))
	require.Equal(t, 1, result.Results[0].Rank)
	require.Equal(t, 2, result.Results[1].Rank)
	require.Equal(t, "team_shared", result.Results[0].SpaceKind)
	require.Equal(t, "evidence A", result.Results[1].Context)
	require.Equal(t, recallID3, result.Conflicts[0].ConflictID)
	require.Equal(t, recallID1, result.EvidenceConflicts[0].ConflictID)
}

func TestRecallEvidenceRequiredReadFailuresReturnNoResult(t *testing.T) {
	for _, stage := range []string{"contract", "text", "vector", "expansion", "state", "hydration", "missing_readers", "relationship_conflicts", "evidence_conflicts"} {
		t.Run(stage, func(t *testing.T) {
			db, mock := newRecallSQLMockDB(t)
			search := recallSearchContract{contract: recallTestContract()}
			input := RecallEvidenceInput{TeamID: recallTeam, Query: "q"}
			relReader := func(context.Context, *gorm.DB, string, *time.Time, []RecallEvidenceHit) ([]RelationshipConflictCaseRecord, error) {
				if stage == "relationship_conflicts" {
					return nil, errRecallDB
				}
				return nil, nil
			}
			evidenceReader := func(context.Context, *gorm.DB, RecallEvidenceInput, []RecallEvidenceHit) ([]EvidenceConflictCaseRecord, error) {
				return nil, errRecallDB
			}
			if stage == "contract" {
				search.err = errRecallDB
			} else {
				q := mock.ExpectQuery("FROM search_documents")
				if stage == "text" {
					q.WillReturnError(errRecallDB)
				} else {
					q.WillReturnRows(addRecallHit(recallHitRows(), "evidence", recallID1, "current"))
					switch stage {
					case "vector":
						input.QueryEmbedding = []float32{1, 0}
						mock.ExpectQuery("FROM search_documents").WillReturnError(errRecallDB)
					case "expansion":
						input.ExpandFromEntityIDs = []string{recallID3}
						mock.ExpectQuery("FROM relationship_records AS relationship").WillReturnError(errRecallDB)
					default:
						q = mock.ExpectQuery("WITH eligible AS NOT MATERIALIZED")
						if stage == "state" {
							q.WillReturnError(errRecallDB)
						} else {
							q.WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("current"))
							q = mock.ExpectQuery("WITH requested")
							if stage == "hydration" {
								q.WillReturnError(errRecallDB)
							} else {
								q.WillReturnRows(evidenceRows().AddRow(recallID1, "context", "source", "manual", recallTime, pq.StringArray{}, "current"))
							}
						}
					}
				}
			}
			if stage == "missing_readers" {
				relReader = nil
			}
			result, err := NewStore(db, recallQueryRLS{}, search, relReader, evidenceReader).RecallEvidence(t.Context(), input)
			require.Error(t, err)
			require.Nil(t, result)
			if stage == "missing_readers" {
				require.ErrorContains(t, err, "conflict readers are required")
			} else {
				require.ErrorIs(t, err, errRecallDB)
			}
		})
	}
}

func TestRecallEvidenceEmptyBranchAndHydrationFailures(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	store := NewStore(db, recallQueryRLS{}, recallSearchContract{contract: recallTestContract()}, nil, nil)
	_, err := store.RecallEvidence(t.Context(), RecallEvidenceInput{})
	require.ErrorContains(t, err, "team_id")
	mock.ExpectQuery("FROM relationship_records AS relationship").WillReturnRows(recallHitRows())
	mock.ExpectQuery("WITH eligible AS NOT MATERIALIZED").WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(""))
	result, err := store.RecallEvidence(t.Context(), RecallEvidenceInput{TeamID: recallTeam, ExpandFromEntityIDs: []string{recallID1}})
	require.NoError(t, err)
	require.Empty(t, result.Results)
	require.Equal(t, "current", result.SearchState)
	for _, iteration := range []bool{false, true} {
		rows := evidenceRows().AddRow(recallID1, "context", "source", "manual", recallTime, pq.StringArray{}, "current")
		if iteration {
			rows.RowError(0, errRecallDB)
		} else {
			rows = evidenceRows().AddRow(recallID1, "context", "source", "manual", "invalid timestamp", pq.StringArray{}, "current")
		}
		mock.ExpectQuery("WITH requested").WillReturnRows(rows).RowsWillBeClosed()
		_, err := hydrateRecallEvidence(t.Context(), db, RecallEvidenceInput{TeamID: recallTeam}, recallTestContract(), []string{recallID1})
		require.Error(t, err)
	}
}

func TestRecallRelationshipsDeduplicatesGroupsAndSortsEqualScoresByAge(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	mock.ExpectQuery(`(?s)WITH.*COUNT\(eligible.relationship_id\)`).WillReturnRows(sqlmock.NewRows([]string{"state", "eligible", "current", "failed"}).AddRow("current", 4, 4, 0))
	text := recallHitRows()
	vector := recallHitRows()
	for _, id := range []string{recallSpace, recallID1, recallID2, recallID3} {
		addRecallHit(text, "relationship", id, "current")
	}
	for _, id := range []string{recallSpace, recallID2, recallID1, recallID3} {
		addRecallHit(vector, "relationship", id, "current")
	}
	mock.ExpectQuery(`(?s)WITH.*plainto_tsquery`).WillReturnRows(text)
	mock.ExpectQuery(`(?s)WITH generation_count.*ORDER BY document.embedding`).WillReturnRows(vector)
	mock.ExpectQuery(`(?s)WITH.*ORDER BY relationship.updated_at DESC`).WillReturnRows(recallHitRows())
	mock.ExpectQuery("WITH requested").WillReturnRows(relationshipRows().
		AddRow(relationshipRow(recallID1, "group1", "current", recallTime)...).
		AddRow(relationshipRow(recallID2, "group2", "failed", recallTime.Add(-time.Hour))...).
		AddRow(relationshipRow(recallID3, "group1", "current", recallTime)...))
	mock.ExpectQuery(`(?s)WITH requested.*effective_support AS`).WillReturnRows(sqlmock.NewRows([]string{"id", "evidence_ids"}).AddRow(recallID1, pq.StringArray{recallID1}).AddRow(recallID2, pq.StringArray{recallID2}).AddRow(recallID3, pq.StringArray{recallID3}))
	mock.ExpectQuery(`(?s)WITH requested.*equivalents AS`).WillReturnRows(sqlmock.NewRows([]string{"id", "equivalent_ids"}).AddRow(recallID1, pq.StringArray{recallID3}))
	result, err := NewStore(db, recallQueryRLS{}, recallSearchContract{contract: recallTestContract()}, nil, nil).RecallRelationships(t.Context(), RecallRelationshipsInput{TeamID: recallTeam, Query: "q", QueryEmbedding: []float32{1, 0}, ExpandFromEntityIDs: []string{recallID3}})
	require.NoError(t, err)
	require.Len(t, result.Results, 2)
	require.Equal(t, recallID2, result.Results[0].RelationshipID)
	require.Equal(t, 1, result.Results[0].Rank)
	require.Equal(t, 2, result.Results[1].Rank)
	require.Equal(t, []string{recallID3}, result.Results[1].EquivalentRelationshipIDs)
	require.Equal(t, "failed", result.SearchState)
	require.False(t, result.VectorOmitted)
}

func TestRecallRelationshipsHydrationDropsKnownAndUnsupportedEvidence(t *testing.T) {
	db, mock := newRecallSQLMockDB(t)
	mock.ExpectQuery("WITH requested").WillReturnRows(relationshipRows().AddRow(relationshipRow(recallID1, "a", "current", recallTime)...).AddRow(relationshipRow(recallID2, "b", "current", recallTime)...))
	mock.ExpectQuery(`(?s)WITH requested.*effective_support AS`).WillReturnRows(sqlmock.NewRows([]string{"id", "evidence_ids"}).AddRow(recallID1, pq.StringArray{recallID3}))
	hits, err := hydrateRecallRelationships(t.Context(), db, RecallRelationshipsInput{TeamID: recallTeam, KnownEvidenceIDs: []string{recallID3}}, recallTestContract(), []string{recallID1, recallID2})
	require.NoError(t, err)
	require.Empty(t, hits)
}

func TestRecallRelationshipsReadFailuresAndVectorOmission(t *testing.T) {
	for _, stage := range []string{"invalid", "contract", "state", "text", "vector", "expansion", "hydration", "omitted", "empty"} {
		t.Run(stage, func(t *testing.T) {
			db, mock := newRecallSQLMockDB(t)
			input := RecallRelationshipsInput{TeamID: recallTeam, Query: "q"}
			search := recallSearchContract{contract: recallTestContract()}
			switch stage {
			case "invalid":
				input.TeamID = "bad"
			case "contract":
				search.err = errRecallDB
			default:
				q := mock.ExpectQuery(`(?s)WITH.*COUNT\(eligible.relationship_id\)`)
				if stage == "state" {
					q.WillReturnError(errRecallDB)
				} else {
					current := 1
					if stage == "omitted" {
						current = 0
						input.QueryEmbedding = []float32{1, 0}
						input.Query = ""
						input.ExpandFromEntityIDs = []string{recallID1}
					}
					q.WillReturnRows(sqlmock.NewRows([]string{"state", "eligible", "current", "failed"}).AddRow("current", 1, current, 0))
					q = mock.ExpectQuery("WITH")
					if stage == "text" {
						q.WillReturnError(errRecallDB)
					} else {
						rows := recallHitRows()
						if stage == "hydration" {
							addRecallHit(rows, "relationship", recallID1, "current")
						}
						q.WillReturnRows(rows)
						switch stage {
						case "vector":
							input.QueryEmbedding = []float32{1, 0}
							mock.ExpectQuery("WITH generation_count").WillReturnError(errRecallDB)
						case "expansion":
							input.ExpandFromEntityIDs = []string{recallID1}
							mock.ExpectQuery("WITH").WillReturnError(errRecallDB)
						case "hydration":
							mock.ExpectQuery("WITH requested").WillReturnError(errRecallDB)
						}
					}
				}
			}
			result, err := NewStore(db, recallQueryRLS{}, search, nil, nil).RecallRelationships(t.Context(), input)
			if stage == "empty" || stage == "omitted" {
				require.NoError(t, err)
				require.Empty(t, result.Results)
				require.Equal(t, stage == "omitted", result.VectorOmitted)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				if stage != "invalid" {
					require.ErrorIs(t, err, errRecallDB)
				}
			}
		})
	}
}

func TestRecallRelationshipHydrationPropagatesEachReadFailure(t *testing.T) {
	for _, part := range []string{"rows", "supports", "equivalents"} {
		for _, failure := range []string{"query", "scan", "iteration"} {
			t.Run(part+"/"+failure, func(t *testing.T) {
				db, mock := newRecallSQLMockDB(t)
				for _, current := range []string{"rows", "supports", "equivalents"} {
					pattern := "WITH requested"
					row := relationshipRow(recallID1, "group", "current", recallTime)
					rows := relationshipRows()
					if current != "rows" {
						pattern = `(?s)WITH requested.*effective_support AS`
						if current == "equivalents" {
							pattern = `(?s)WITH requested.*equivalents AS`
						}
						row = []driver.Value{recallID1, pq.StringArray{recallID2}}
						rows = sqlmock.NewRows([]string{"id", "ids"})
					}
					q := mock.ExpectQuery(pattern)
					if current == part && failure == "query" {
						q.WillReturnError(errRecallDB)
					} else {
						if current == part && failure == "scan" {
							if part == "rows" {
								row[15] = "invalid count"
							} else {
								row[1] = "invalid array"
							}
						}
						rows.AddRow(row...)
						if current == part && failure == "iteration" {
							rows.RowError(0, errRecallDB)
						}
						q.WillReturnRows(rows).RowsWillBeClosed()
					}
					if current == part {
						break
					}
				}
				_, err := hydrateRecallRelationships(t.Context(), db, RecallRelationshipsInput{TeamID: recallTeam}, recallTestContract(), []string{recallID1})
				require.Error(t, err)
				if failure != "scan" {
					require.ErrorIs(t, err, errRecallDB)
				}
			})
		}
	}
}
