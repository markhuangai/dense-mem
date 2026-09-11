package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const (
	recallTeam     = "11111111-1111-4111-8111-111111111111"
	recallSpace    = "22222222-2222-4222-8222-222222222222"
	recallContract = "33333333-3333-4333-8333-333333333333"
	recallID1      = "44444444-4444-4444-8444-444444444444"
	recallID2      = "55555555-5555-4555-8555-555555555555"
	recallID3      = "66666666-6666-4666-8666-666666666666"
)

var (
	recallTime  = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	errRecallDB = errors.New("test database failure")
)

func newRecallSQLMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		_ = sqlDB.Close()
	})
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db, mock
}

// SQL execution and RLS are covered by the registered PostgreSQL precheck.
type recallQueryRLS struct{ storagepostgres.RLSHelper }

func (recallQueryRLS) WithTeamTx(ctx context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db.WithContext(ctx))
}

func (recallQueryRLS) WithSystemTx(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db.WithContext(ctx))
}

type recallSearchContract struct {
	searchcontract.SearchRepository
	contract *ActiveSearchContract
	err      error
}

func (s recallSearchContract) GetActiveSearchContract(context.Context) (*ActiveSearchContract, error) {
	return s.contract, s.err
}

func recallTestContract() *ActiveSearchContract {
	return &ActiveSearchContract{EmbeddingContractID: recallContract, EmbeddingDimensions: 2, IndexStrategy: "exact", DistanceMetric: "cosine", CandidateLimit: 80}
}

func recallHitRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"team_id", "search_document_id", "source_kind", "source_id", "source_version", "document_version", "embedding_contract_id", "search_state", "distance", "text_rank"})
}

func addRecallHit(rows *sqlmock.Rows, kind, id, state string) *sqlmock.Rows {
	return rows.AddRow(recallTeam, id, kind, id, 2, 3, recallContract, state, 0.25, 0.75)
}

type recallStoreSource struct{ *Store }

func (s recallStoreSource) RecallDatabase() *gorm.DB                                { return s.db }
func (s recallStoreSource) RecallRLS() storagepostgres.RLSHelper                    { return s.rls }
func (s recallStoreSource) RecallSearchRepository() searchcontract.SearchRepository { return s.search }
func (s recallStoreSource) RecallRelationshipConflictReader() RelationshipConflictReader {
	return s.relationshipConflicts
}
func (s recallStoreSource) RecallEvidenceConflictReader() EvidenceConflictReader {
	return s.evidenceConflicts
}

func TestRecallStoreConstructionPreservesDependenciesAndReportsMissingOnes(t *testing.T) {
	db, _ := newRecallSQLMockDB(t)
	contract := recallTestContract()
	source := recallStoreSource{NewStore(db, recallQueryRLS{}, recallSearchContract{contract: contract}, nil, nil)}
	store := NewStoreFromSource(source)
	got, err := store.GetActiveSearchContract(t.Context())
	require.NoError(t, err)
	require.Same(t, contract, got)
	gotDB, err := store.database()
	require.NoError(t, err)
	require.Same(t, db, gotDB)
	require.Nil(t, NewStoreFromSource(nil))
	var missing *Store
	_, err = missing.database()
	require.EqualError(t, err, "recall: database is required")
	_, err = missing.GetActiveSearchContract(t.Context())
	require.EqualError(t, err, "recall: search adapter is required")
	fn := func(*gorm.DB) error { t.Fatal("unexpected query"); return nil }
	require.EqualError(t, missing.withTeamTx(t.Context(), recallTeam, fn), "recall: database is required")
	require.EqualError(t, NewStore(db, nil, nil, nil, nil).withTeamTx(t.Context(), recallTeam, fn), "recall: rls helper is required")
}
