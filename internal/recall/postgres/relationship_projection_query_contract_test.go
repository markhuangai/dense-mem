package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
)

type projectionQueryStatement struct {
	SQL     string   `json:"sql"`
	Args    []string `json:"args"`
	rawSQL  string
	rawArgs []any
}

type projectionQueryCapture struct {
	Statements []projectionQueryStatement `json:"statements"`
}

func (c *projectionQueryCapture) add(query string, args []any) {
	statement := projectionQueryStatement{
		SQL: strings.Join(strings.Fields(query), " "), Args: make([]string, len(args)),
		rawSQL: query, rawArgs: append([]any(nil), args...),
	}
	for index, arg := range args {
		statement.Args[index] = fmt.Sprintf("%T:%v", arg, arg)
	}
	c.Statements = append(c.Statements, statement)
}

type projectionQueryConnPool struct {
	gorm.ConnPool
	capture *projectionQueryCapture
}

func (pool *projectionQueryConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return pool.ConnPool.PrepareContext(ctx, query)
}

func (pool *projectionQueryConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	pool.capture.add(query, args)
	return pool.ConnPool.ExecContext(ctx, query, args...)
}

func (pool *projectionQueryConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	pool.capture.add(query, args)
	return pool.ConnPool.QueryContext(ctx, query, args...)
}

func (pool *projectionQueryConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	pool.capture.add(query, args)
	return pool.ConnPool.QueryRowContext(ctx, query, args...)
}

func projectionCapturedDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *projectionQueryCapture) {
	t.Helper()
	db, mock := newRecallSQLMockDB(t)
	capture := &projectionQueryCapture{}
	wrapped := db.Session(&gorm.Session{})
	pool := &projectionQueryConnPool{ConnPool: db.ConnPool, capture: capture}
	wrapped.ConnPool = pool
	wrapped.Statement.ConnPool = pool
	return wrapped, mock, capture
}

func projectionMockSearchContract(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("(?s).*").WillReturnRows(sqlmock.NewRows([]string{
		"embedding_contract_id", "search_index_generation_id", "dimensions", "provider", "model",
		"distance_metric", "vector_normalization", "document_format_version", "query_format_version",
		"generation", "ann_strategy", "operator_class", "indexed_expression", "physical_index_name",
		"query_ef_search", "exact_max_rows", "candidate_limit", "allow_exact_fallback",
	}).AddRow(recallContract, recallID3, 2, "test", "model", "cosine", "provider", 1, 1,
		1, "exact", "", "", "", 40, 100, 80, true))
}

func TestRelationshipProjectionQueryContract(t *testing.T) {
	contract := recallTestContract()
	input := RecallRelationshipsInput{TeamID: recallTeam, Query: "projection", QueryEmbedding: []float32{1, 0}, ExpandFromEntityIDs: []string{recallID1}, Limit: 5}
	cases := []struct {
		name    string
		prepare func(sqlmock.Sqlmock)
		run     func(*gorm.DB) (any, error)
	}{
		{
			name: "search_readiness",
			prepare: func(mock sqlmock.Sqlmock) {
				projectionMockSearchContract(mock)
				mock.ExpectQuery("(?s).*").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
				mock.ExpectQuery("(?s).*").WillReturnRows(sqlmock.NewRows([]string{"incomplete"}).AddRow(false))
			},
			run: func(db *gorm.DB) (any, error) {
				return searchpostgres.NewStore(db, recallQueryRLS{}).CheckSearchReadiness(t.Context())
			},
		},
		{
			name: "search_full_text",
			prepare: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("(?s).*").WillReturnRows(addRecallHit(recallHitRows(), "relationship", recallID1, "current"))
			},
			run: func(db *gorm.DB) (any, error) {
				return searchpostgres.NewStore(db, recallQueryRLS{}).SearchFullText(t.Context(), searchcontract.FullTextSearchInput{
					TeamID: recallTeam, Query: "projection", SourceKind: "relationship", Limit: 5,
				})
			},
		},
		{
			name: "search_exact_vector",
			prepare: func(mock sqlmock.Sqlmock) {
				projectionMockSearchContract(mock)
				mock.ExpectQuery("(?s).*").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery("(?s).*").WillReturnRows(addRecallHit(recallHitRows(), "relationship", recallID1, "current"))
			},
			run: func(db *gorm.DB) (any, error) {
				return searchpostgres.NewStore(db, recallQueryRLS{}).SearchExactVector(t.Context(), searchcontract.ExactVectorSearchInput{
					TeamID: recallTeam, QueryEmbedding: []float32{1, 0}, SourceKind: "relationship", Limit: 5,
				})
			},
		},
		{
			name: "recall_readiness",
			prepare: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("(?s).*").WillReturnRows(sqlmock.NewRows([]string{"state", "eligible", "current", "failed"}).AddRow("current", 1, 1, 0))
			},
			run: func(db *gorm.DB) (any, error) {
				return relationshipProjectionSearchState(t.Context(), db, input, contract)
			},
		},
		{
			name: "recall_full_text",
			prepare: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("(?s).*").WillReturnRows(addRecallHit(recallHitRows(), "relationship", recallID1, "current"))
			},
			run: func(db *gorm.DB) (any, error) {
				return searchRecallRelationshipFullText(t.Context(), db, input, contract, 5)
			},
		},
		{
			name: "recall_exact_vector",
			prepare: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("(?s).*").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery("(?s).*").WillReturnRows(addRecallHit(recallHitRows(), "relationship", recallID1, "current"))
			},
			run: func(db *gorm.DB) (any, error) {
				withBudget := *contract
				withBudget.ExactMaxRows = 5
				return searchRecallRelationshipExactVector(t.Context(), db, input, &withBudget, 5)
			},
		},
		{
			name: "recall_ann_vector",
			prepare: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec("(?s).*").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery("(?s).*").WillReturnRows(addRecallHit(recallHitRows(), "relationship", recallID1, "current"))
			},
			run: func(db *gorm.DB) (any, error) {
				ann := *contract
				ann.IndexStrategy = "vector_hnsw"
				return searchRecallRelationshipANNVector(t.Context(), db, input, &ann, 5)
			},
		},
		{
			name: "recall_expansion",
			prepare: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("(?s).*").WillReturnRows(recallHitRows())
			},
			run: func(db *gorm.DB) (any, error) {
				return searchRecallRelationshipEntityExpansion(t.Context(), db, input, contract, 5)
			},
		},
		{
			name: "recall_hydration",
			prepare: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("(?s).*").WillReturnRows(sqlmock.NewRows([]string{"relationship_id"}))
			},
			run: func(db *gorm.DB) (any, error) {
				return hydrateRecallRelationships(t.Context(), db, input, contract, []string{recallID1})
			},
		},
	}
	type result struct {
		Case       string                     `json:"case"`
		Statements []projectionQueryStatement `json:"statements"`
		Result     json.RawMessage            `json:"result"`
	}
	report := make([]result, 0, len(cases))
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db, mock, capture := projectionCapturedDB(t)
			testCase.prepare(mock)
			value, err := testCase.run(db)
			require.NoError(t, err)
			payload, err := json.Marshal(value)
			require.NoError(t, err)
			require.NotEmpty(t, capture.Statements)
			report = append(report, result{Case: testCase.name, Statements: capture.Statements, Result: payload})
		})
	}
	if output := os.Getenv("DENSE_MEM_PROJECTION_QUERY_REPORT"); output != "" {
		path, err := filepath.Abs(output)
		require.NoError(t, err)
		require.Contains(t, filepath.ToSlash(path), "/tests/eval/runs/issue-457/")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		payload, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(payload, '\n'), 0o644))
	}
}
