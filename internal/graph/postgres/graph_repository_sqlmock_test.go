package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
)

func TestSemanticGraphQueryArgsPreserveBoundOrder(t *testing.T) {
	args := semanticGraphQueryArgs(graphExecutionQuery{Query: graphcontract.Query{
		TeamID:       "team-id",
		Query:        "needle",
		MinRelevance: 0.4,
	}}, 9, "frontier")

	require.Len(t, args, 12)
	assert.Equal(t, []any{"team-id", "needle", "needle", "needle", 0.4, "needle", "needle", 0.4}, args[:8])
	assert.Equal(t, "frontier", args[8])
	assert.Equal(t, []any{"needle", "needle", 9}, args[9:])
}

func TestLoadSemanticOverviewGraphRowsScansAndClosesAdapterRows(t *testing.T) {
	db, mock, gormDB := newGraphSQLMockDB(t)
	defer db.Close()
	columns := graphSQLColumns()
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT e.relationship_id")).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(
			"edge-1", "owner-1", "uses", 1, 2,
			"entity:source", "source", "Source", "project", "active", "owner-1", now,
			"value:target", "target", "value", "Target", "date", "active", "", now,
		))

	rows, err := loadSemanticOverviewGraphRows(context.Background(), gormDB, graphExecutionQuery{
		Query: graphcontract.Query{
			TeamID: uuid.NewString(),
			Scope:  "overview",
			Types:  []string{"entity", "value"},
			Limit:  1,
		},
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "edge-1", rows[0].Edge.ID)
	assert.Equal(t, "value", rows[0].Target.Type)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScanSemanticGraphRowsPropagatesRowError(t *testing.T) {
	db, mock, gormDB := newGraphSQLMockDB(t)
	defer db.Close()
	rowErr := errors.New("row decode failed")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT graph")).
		WillReturnRows(sqlmock.NewRows(graphSQLColumns()).
			AddRow("edge-1", "owner-1", "uses", 1, 2,
				"entity:source", "source", "Source", "project", "active", "owner-1", time.Now(),
				"entity:target", "target", "entity", "Target", "project", "active", "owner-1", time.Now()).
			RowError(0, rowErr))
	rows, err := gormDB.Raw("SELECT graph").Rows()
	require.NoError(t, err)
	_, err = scanSemanticGraphRows(rows, []string{"entity", "value"})
	assert.ErrorIs(t, err, rowErr)
	require.NoError(t, rows.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLoadSemanticGraphNodePropagatesRowsCloseError(t *testing.T) {
	tests := []struct {
		name  string
		query string
		load  func(context.Context, *gorm.DB, string, string) (*graphcontract.Node, error)
	}{
		{name: "entity", query: "SELECT ('entity:'", load: loadSemanticEntityGraphNode},
		{name: "value", query: "SELECT ('value:'", load: loadSemanticValueGraphNode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, gormDB := newGraphSQLMockDB(t)
			defer db.Close()
			closeErr := errors.New("graph node rows close failed")
			mock.ExpectQuery(regexp.QuoteMeta(tt.query)).WillReturnRows(
				sqlmock.NewRows([]string{"key", "id", "title", "body", "status", "owner", "recorded_at"}).
					AddRow(tt.name+":node", "node", "Node", "entity", "active", "owner", time.Now()).
					CloseError(closeErr),
			)

			node, err := tt.load(context.Background(), gormDB, uuid.NewString(), uuid.NewString())
			assert.Nil(t, node)
			assert.ErrorIs(t, err, closeErr)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestLoadSemanticLocalGraphRowsPropagatesQueryError(t *testing.T) {
	db, mock, gormDB := newGraphSQLMockDB(t)
	defer db.Close()
	queryErr := errors.New("local graph query failed")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT e.relationship_id")).WillReturnError(queryErr)

	_, err := loadSemanticLocalGraphRows(context.Background(), gormDB, graphExecutionQuery{
		Query: graphcontract.Query{
			TeamID:     uuid.NewString(),
			Scope:      "local",
			AnchorType: "entity",
			AnchorID:   uuid.NewString(),
			Types:      []string{"entity", "value"},
			Depth:      2,
			Limit:      10,
		},
	})
	assert.ErrorIs(t, err, queryErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLoadSemanticLocalGraphRowsBatchesFrontiersAndRemainingLimit(t *testing.T) {
	db, mock, gormDB := newGraphSQLMockDB(t)
	defer db.Close()
	rootID := uuid.NewString()
	childA := uuid.NewString()
	childB := uuid.NewString()
	grandchild := uuid.NewString()
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)

	rootRows := sqlmock.NewRows(graphSQLColumns())
	rootRows.AddRow("edge-a", "owner", "uses", 1, 1, "entity:"+rootID, rootID, "Root", "project", "active", "owner", now, "entity:"+childA, childA, "entity", "Child A", "project", "active", "owner", now)
	rootRows.AddRow("edge-b", "owner", "uses", 1, 1, "entity:"+rootID, rootID, "Root", "project", "active", "owner", now, "entity:"+childB, childB, "entity", "Child B", "project", "active", "owner", now)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT e.relationship_id")).WithArgs(graphLocalQueryArgs(frontierArgument{items: []string{"entity:" + rootID}}, 3)...).WillReturnRows(rootRows)

	childRows := sqlmock.NewRows(graphSQLColumns())
	childRows.AddRow("edge-c", "owner", "uses", 1, 1, "entity:"+childA, childA, "Child A", "project", "active", "owner", now, "entity:"+grandchild, grandchild, "entity", "Grandchild", "project", "active", "owner", now)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT e.relationship_id")).WithArgs(graphLocalQueryArgs(frontierArgument{items: []string{"entity:" + childA, "entity:" + childB}}, 1)...).WillReturnRows(childRows)

	rows, err := loadSemanticLocalGraphRows(context.Background(), gormDB, graphExecutionQuery{
		Query: graphcontract.Query{
			TeamID:     uuid.NewString(),
			Scope:      "local",
			AnchorType: "entity",
			AnchorID:   rootID,
			Types:      []string{"entity", "value"},
			Depth:      2,
			Limit:      3,
		},
	})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, "edge-c", rows[2].Edge.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func newGraphSQLMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *gorm.DB) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	gormDB, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: db, DriverName: "postgres"}), &gorm.Config{})
	require.NoError(t, err)
	return db, mock, gormDB
}

func graphSQLColumns() []string {
	return []string{
		"edge_id", "owner_id", "predicate", "support_count", "source_group_count",
		"source_key", "source_id", "source_title", "source_body", "source_status", "source_owner", "source_recorded_at",
		"target_key", "target_id", "target_type", "target_title", "target_body", "target_status", "target_owner", "target_recorded_at",
	}
}

type frontierArgument struct {
	items []string
}

func (argument frontierArgument) Match(value driver.Value) bool {
	var textValue string
	switch value := value.(type) {
	case string:
		textValue = value
	case []byte:
		textValue = string(value)
	default:
		return false
	}
	for _, item := range argument.items {
		if !strings.Contains(textValue, item) {
			return false
		}
	}
	return true
}

func graphLocalQueryArgs(frontier frontierArgument, limit int) []driver.Value {
	args := make([]driver.Value, 8)
	for index := range args {
		args[index] = sqlmock.AnyArg()
	}
	args = append(args, frontier, frontier, sqlmock.AnyArg(), sqlmock.AnyArg(), limit)
	return args
}
