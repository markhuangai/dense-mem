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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT marker_id::text")).WithArgs(domain.MigrationMarkerKindCutover).WillReturnRows(sqlmock.NewRows([]string{
		"marker_id", "marker_kind", "version", "status", "run_id", "corpus_hash", "gate_report_hash", "metadata", "created_at",
	}).AddRow(uuid.New().String(), domain.MigrationMarkerKindCutover, testCutoverMarkerVersion, domain.MigrationMarkerCompatible, "", "corpus", "gates", `{"source":"test"}`, now))

	marker, err := NewAuthorityRepository(db, passthroughRLS{}).GetLatestMarker(context.Background())
	require.NoError(t, err)
	require.NotNil(t, marker)
	require.Equal(t, domain.MigrationMarkerKindCutover, marker.MarkerKind)
	require.Equal(t, testCutoverMarkerVersion, marker.Version)
	require.Equal(t, "test", marker.Metadata["source"])
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

func TestAuthorityRepositoryCommitFreshAuthorityBlocksExistingMarker(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*)::int")).WithArgs(domain.MigrationMarkerKindCutover).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	_, err := NewAuthorityRepository(db, passthroughRLS{}).CommitFreshAuthority(context.Background(), CommitFreshAuthorityInput{
		MarkerVersion: testCutoverMarkerVersion,
		Metadata:      map[string]any{"operator": "test"},
		Now:           time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	})
	require.ErrorIs(t, err, ErrFreshAuthorityBlocked)
	require.ErrorContains(t, err, "cutover marker already exists")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorityRepositoryCommitFreshAuthorityRecordsMarkerWhenTablesAreEmpty(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()

	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*)::int")).WithArgs(domain.MigrationMarkerKindCutover).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	for _, table := range freshAuthorityApplicationTables {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT to_regclass($1) IS NOT NULL")).WithArgs(table).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	}
	markerID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO v2_compatibility_markers")).WithArgs(
		sqlmock.AnyArg(), domain.MigrationMarkerKindCutover, testCutoverMarkerVersion, domain.MigrationMarkerCompatible,
		`{"fresh_install":true,"operator":"test"}`, now,
	).WillReturnRows(sqlmock.NewRows([]string{
		"marker_id", "marker_kind", "version", "status", "run_id", "corpus_hash", "gate_report_hash", "metadata", "created_at",
	}).AddRow(markerID.String(), domain.MigrationMarkerKindCutover, testCutoverMarkerVersion, domain.MigrationMarkerCompatible, "", "", "", `{"fresh_install":true,"operator":"test"}`, now))

	marker, err := NewAuthorityRepository(db, passthroughRLS{}).CommitFreshAuthority(context.Background(), CommitFreshAuthorityInput{
		MarkerVersion: testCutoverMarkerVersion,
		Metadata:      map[string]any{"operator": "test"},
		Now:           now,
	})
	require.NoError(t, err)
	require.Equal(t, markerID.String(), marker.MarkerID)
	require.Equal(t, true, marker.Metadata["fresh_install"])
	require.Equal(t, "test", marker.Metadata["operator"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorityRepositoryCommitFreshAuthorityBlocksNonemptyTable(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*)::int")).WithArgs(domain.MigrationMarkerKindCutover).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	for index, table := range freshAuthorityApplicationTables {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT to_regclass($1) IS NOT NULL")).WithArgs(table).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(index == 0))
		if index == 0 {
			mock.ExpectExec(`LOCK TABLE "teams" IN SHARE MODE`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM "teams" LIMIT 1\)`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		}
	}

	_, err := NewAuthorityRepository(db, passthroughRLS{}).CommitFreshAuthority(context.Background(), CommitFreshAuthorityInput{MarkerVersion: testCutoverMarkerVersion})
	require.ErrorIs(t, err, ErrFreshAuthorityBlocked)
	require.ErrorContains(t, err, "nonempty application tables: teams")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorityRepositoryCommitFreshAuthorityPropagatesTableProbeError(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*)::int")).WithArgs(domain.MigrationMarkerKindCutover).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT to_regclass($1) IS NOT NULL")).WithArgs(freshAuthorityApplicationTables[0]).WillReturnError(errors.New("catalog unavailable"))

	_, err := NewAuthorityRepository(db, passthroughRLS{}).CommitFreshAuthority(context.Background(), CommitFreshAuthorityInput{MarkerVersion: testCutoverMarkerVersion})
	require.ErrorContains(t, err, "catalog unavailable")
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

func TestAuthorityRepositoryHelpersHandleQuotedIdentifiersAndNotFound(t *testing.T) {
	require.Equal(t, `"table""name"`, pqQuoteIdentifier(`table"name`))
	require.True(t, isAuthorityRowNotFound(sql.ErrNoRows))
	require.True(t, isAuthorityRowNotFound(gorm.ErrRecordNotFound))
	require.False(t, isAuthorityRowNotFound(errors.New("other")))
	var metadata map[string]any
	require.NoError(t, unmarshalAuthorityJSON("null", &metadata))
	require.Empty(t, metadata)
	encoded, err := marshalAuthorityJSON(nil)
	require.NoError(t, err)
	require.Equal(t, `{}`, string(encoded))
}

func TestScanCompatibilityMarkerParsesMetadata(t *testing.T) {
	now := time.Date(2026, 7, 23, 17, 45, 0, 0, time.UTC)
	row := authorityScannerStub{
		values: []any{
			"marker-1",
			domain.MigrationMarkerKindCutover,
			"dense-mem.v2.1.cutover.v1",
			domain.MigrationMarkerCompatible,
			"",
			"sha256:corpus",
			"sha256:gates",
			`{"fresh_install":true}`,
			now,
		},
	}

	marker, err := scanCompatibilityMarker(row)

	require.NoError(t, err)
	require.Equal(t, "marker-1", marker.MarkerID)
	require.Equal(t, domain.MigrationMarkerCompatible, marker.Status)
	require.Equal(t, true, marker.Metadata["fresh_install"])
	require.Equal(t, now, marker.CreatedAt)
}

func TestScanCompatibilityMarkerReturnsScannerError(t *testing.T) {
	_, err := scanCompatibilityMarker(authorityScannerStub{err: sql.ErrNoRows})

	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestScanCompatibilityMarkerRejectsInvalidMetadata(t *testing.T) {
	now := time.Now().UTC()
	_, err := scanCompatibilityMarker(authorityScannerStub{values: []any{
		"marker-1", domain.MigrationMarkerKindCutover, testCutoverMarkerVersion, domain.MigrationMarkerCompatible,
		"", "", "", "{", now,
	}})
	require.ErrorContains(t, err, "decode json")
}

func TestAuthorityJSONHelpersHandleEmptyInvalidAndUnsupportedValues(t *testing.T) {
	var metadata map[string]any
	require.NoError(t, unmarshalAuthorityJSON("", &metadata))
	require.Empty(t, metadata)

	require.Error(t, unmarshalAuthorityJSON("{", &metadata))

	_, err := marshalAuthorityJSON(map[string]any{"bad": func() {}})
	require.Error(t, err)
	require.ErrorContains(t, err, "encode json")
}

func TestFreshAuthorityApplicationTablesExcludeRetiredCleanupTables(t *testing.T) {
	retired := map[string]struct{}{
		"profiles":                 {},
		"api_keys":                 {},
		"memory_placement_runs":    {},
		"memory_placement_items":   {},
		"memory_dispute_sessions":  {},
		"community_detection_runs": {},
	}

	for _, table := range freshAuthorityApplicationTables {
		if _, ok := retired[table]; ok {
			t.Fatalf("fresh-install guard must not inspect retired cleanup table %q", table)
		}
	}
	require.Contains(t, freshAuthorityApplicationTables, "teams")
	require.NotContains(t, freshAuthorityApplicationTables, "team_profiles")
	require.Contains(t, freshAuthorityApplicationTables, "credentials")
	require.Contains(t, freshAuthorityApplicationTables, "v2_migration_runs")
}

type authorityScannerStub struct {
	values []any
	err    error
}

func (s authorityScannerStub) Scan(dest ...any) error {
	if s.err != nil {
		return s.err
	}
	if len(dest) != len(s.values) {
		return errors.New("destination/value length mismatch")
	}
	for i := range dest {
		switch target := dest[i].(type) {
		case *string:
			*target = s.values[i].(string)
		case *time.Time:
			*target = s.values[i].(time.Time)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}
