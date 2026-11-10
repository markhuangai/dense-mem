package postgres

import (
	"database/sql/driver"
	"math"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecallSearchBranchesBindQueriesAndDecodeHits(t *testing.T) {
	input := RecallEvidenceInput{TeamID: recallTeam, Query: "O'Reilly", QueryEmbedding: []float32{1, 0}, KnownAt: &recallTime}
	contract := recallTestContract()
	ann := *contract
	ann.IndexStrategy = "vector_hnsw"
	for _, tc := range []struct {
		name, query, kind string
		args              []driver.Value
		ann               bool
		call              func(*gorm.DB) ([]SearchHit, error)
	}{
		{"evidence text", `(?s)FROM search_documents.*source_kind\s*=\s*'evidence'.*ORDER BY text_rank DESC`, "evidence", []driver.Value{input.Query, recallTeam, recallContract, recallTime, input.Query, recallTime, recallTime, recallTime, recallTime, recallTime, recallTime, recallTime, 10}, false, func(db *gorm.DB) ([]SearchHit, error) {
			return searchRecallFullText(t.Context(), db, input, contract, 10)
		}},
		{"evidence exact", `(?s)FROM search_documents.*embedding_dimensions = \$4.*search_state = 'current'.*ORDER BY search_documents.embedding`, "evidence", []driver.Value{"[1,0]", recallTeam, recallContract, 2, recallTime, recallTime, recallTime, recallTime, recallTime, recallTime, recallTime, "[1,0]", 10}, false, func(db *gorm.DB) ([]SearchHit, error) {
			return searchRecallVector(t.Context(), db, input, contract, 10)
		}},
		{"evidence ANN", `(?s)WITH ann_candidates AS MATERIALIZED.*embedding_contract_id = '` + recallContract + `'.*FROM ann_candidates AS candidate`, "evidence", []driver.Value{recallTeam, recallTime, recallTime, recallTime, recallTime, recallTime, recallTime, recallTime, "[1,0]", 80, "[1,0]", "[1,0]", 10}, true, func(db *gorm.DB) ([]SearchHit, error) { return searchRecallVector(t.Context(), db, input, &ann, 10) }},
	} {
		for _, fails := range []bool{false, true} {
			name := tc.name + "/success"
			if fails {
				name = tc.name + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				db, mock := newRecallSQLMockDB(t)
				if tc.ann {
					mock.ExpectExec(`SELECT set_config\('hnsw.ef_search', \$1, true\)`).WithArgs("80").WillReturnResult(sqlmock.NewResult(0, 1))
				}
				query := mock.ExpectQuery(tc.query)
				if tc.args != nil {
					query.WithArgs(tc.args...)
				}
				if fails {
					query.WillReturnError(errRecallDB)
				} else {
					query.WillReturnRows(addRecallHit(recallHitRows(), tc.kind, recallID1, "current")).RowsWillBeClosed()
				}
				hits, err := tc.call(db)
				if fails {
					require.ErrorIs(t, err, errRecallDB)
					require.Nil(t, hits)
					return
				}
				require.NoError(t, err)
				require.Equal(t, []SearchHit{{TeamID: recallTeam, SearchDocumentID: recallID1, SourceKind: tc.kind, SourceID: recallID1, SourceVersion: 2, DocumentVersion: 3, EmbeddingContractID: recallContract, SearchState: "current", Distance: 0.25, TextRank: 0.75}}, hits)
			})
		}
	}
}

func TestRecallSearchRejectsMalformedRowsAndIterationFailures(t *testing.T) {
	for _, scan := range []bool{false, true} {
		db, mock := newRecallSQLMockDB(t)
		rows := addRecallHit(recallHitRows(), "evidence", recallID1, "current")
		if scan {
			rows.AddRow(recallTeam, recallID2, "evidence", recallID2, "invalid version", 1, recallContract, "current", 0, 0)
		} else {
			addRecallHit(rows, "evidence", recallID2, "current")
			rows.RowError(1, errRecallDB)
		}
		mock.ExpectQuery("FROM search_documents").WillReturnRows(rows).RowsWillBeClosed()
		_, err := searchRecallFullText(t.Context(), db, RecallEvidenceInput{TeamID: recallTeam, Query: "query"}, recallTestContract(), 10)
		require.Error(t, err)
		if !scan {
			require.ErrorIs(t, err, errRecallDB)
		}
	}
}

func TestRecallVectorQueriesRejectInvalidContractsBeforeSQL(t *testing.T) {
	for _, kind := range []string{"evidence"} {
		for _, strategy := range []string{"exact", "vector_hnsw"} {
			for _, failure := range []string{"dimensions", "non_finite", "strategy", "contract_id", "ef_search"} {
				if strategy == "exact" && (failure == "contract_id" || failure == "ef_search") {
					continue
				}
				t.Run(kind+"/"+strategy+"/"+failure, func(t *testing.T) {
					db, mock := newRecallSQLMockDB(t)
					contract := recallTestContract()
					contract.IndexStrategy = strategy
					vector := []float32{1, 0}
					switch failure {
					case "dimensions":
						vector = []float32{1}
					case "non_finite":
						vector[1] = float32(math.Inf(1))
					case "strategy":
						contract.IndexStrategy = "unknown"
					case "contract_id":
						contract.EmbeddingContractID = "invalid"
					case "ef_search":
						mock.ExpectExec("SELECT set_config").WithArgs("80").WillReturnError(errRecallDB)
					}
					var err error
					_, err = searchRecallVector(t.Context(), db, RecallEvidenceInput{TeamID: recallTeam, QueryEmbedding: vector}, contract, 10)
					require.Error(t, err)
					if failure == "ef_search" {
						require.ErrorIs(t, err, errRecallDB)
					}
				})
			}
		}
	}
}
