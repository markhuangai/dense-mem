package postgres

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const testCutoverMarkerVersion = "dense-mem.v2.6.1.cutover.v1"

func TestAuthorityRepositoryGetLatestMarkerReturnsMarker(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()

	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	id := uuid.NewString()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT marker_id::text")).WithArgs(domain.MigrationMarkerKindCutover).WillReturnRows(sqlmock.NewRows([]string{
		"marker_id", "marker_kind", "version", "status", "run_id", "corpus_hash", "gate_report_hash", "metadata", "created_at",
	}).AddRow(id, domain.MigrationMarkerKindCutover, testCutoverMarkerVersion, domain.MigrationMarkerCompatible, "", "corpus", "gates", `{"source":"test","fresh_install":true}`, now))

	marker, err := NewAuthorityRepository(db, passthroughRLS{}).GetLatestMarker(context.Background())
	require.NoError(t, err)
	require.NotNil(t, marker)
	require.Equal(t, id, marker.MarkerID)
	require.Equal(t, domain.MigrationMarkerKindCutover, marker.MarkerKind)
	require.Equal(t, testCutoverMarkerVersion, marker.Version)
	require.Equal(t, domain.MigrationMarkerCompatible, marker.Status)
	require.Equal(t, now, marker.CreatedAt)
	require.Equal(t, "test", marker.Metadata["source"])
	require.Equal(t, true, marker.Metadata["fresh_install"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorityRepositoryGetLatestMarkerTreatsMissingMarkerAsEmpty(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT marker_id::text")).WithArgs(domain.MigrationMarkerKindCutover).WillReturnError(sql.ErrNoRows)

	marker, err := NewAuthorityRepository(db, passthroughRLS{}).GetLatestMarker(context.Background())
	require.NoError(t, err)
	require.Nil(t, marker)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorityRepositoryGetLatestMarkerWrapsUnexpectedReadError(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT marker_id::text")).WithArgs(domain.MigrationMarkerKindCutover).WillReturnError(errors.New("database unavailable"))

	_, err := NewAuthorityRepository(db, passthroughRLS{}).GetLatestMarker(context.Background())
	require.ErrorContains(t, err, "authority repository: get marker")
	require.ErrorContains(t, err, "database unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorityRepositoryWithSystemTxUsesDatabaseTransactionWithoutRLS(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()
	mock.ExpectBegin()
	mock.ExpectCommit()

	repo := NewAuthorityRepository(db, nil)
	require.NoError(t, repo.withSystemTx(context.Background(), func(*gorm.DB) error { return nil }))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorityRepositoryHelpersHandleNotFound(t *testing.T) {
	require.True(t, isAuthorityRowNotFound(sql.ErrNoRows))
	require.True(t, isAuthorityRowNotFound(gorm.ErrRecordNotFound))
	require.False(t, isAuthorityRowNotFound(errors.New("other")))
	var metadata map[string]any
	require.NoError(t, unmarshalAuthorityJSON("null", &metadata))
	require.Empty(t, metadata)
}

func TestAuthorityRepositoryGetLatestMarkerRejectsInvalidMetadata(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT marker_id::text")).WithArgs(domain.MigrationMarkerKindCutover).WillReturnRows(sqlmock.NewRows([]string{
		"marker_id", "marker_kind", "version", "status", "run_id", "corpus_hash", "gate_report_hash", "metadata", "created_at",
	}).AddRow(uuid.NewString(), domain.MigrationMarkerKindCutover, testCutoverMarkerVersion, domain.MigrationMarkerCompatible, "", "", "", "[]", time.Now().UTC()))

	_, err := NewAuthorityRepository(db, passthroughRLS{}).GetLatestMarker(context.Background())
	require.ErrorContains(t, err, "decode json")
	require.NoError(t, mock.ExpectationsWereMet())
}
