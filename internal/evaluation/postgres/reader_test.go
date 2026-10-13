package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type evaluationReaderTestRLS struct {
	storagepostgres.RLSHelper
}

func (evaluationReaderTestRLS) WithTeamTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func newEvaluationReaderSQLMock(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	return db, mock
}

func TestEvaluationReaderDecodesCanonicalRows(t *testing.T) {
	db, mock := newEvaluationReaderSQLMock(t)
	mock.ExpectQuery(`SELECT jsonb_build_object`).
		WillReturnRows(sqlmock.NewRows([]string{"item"}).AddRow([]byte(`{"type":"evidence","id":"evidence-id"}`)))

	reader := NewEvaluationReader(db, evaluationReaderTestRLS{})
	page, err := reader.ListEvaluationRefs(t.Context(), EvaluationListInput{
		TeamID: "00000000-0000-0000-0000-000000000101",
		Type:   "evidence",
		Limit:  1,
	})
	require.NoError(t, err)
	require.False(t, page.HasMore)
	require.Equal(t, "evidence-id", page.Items[0]["id"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvaluationReaderReturnsQueryAndDecodeErrors(t *testing.T) {
	databaseErr := errors.New("database unavailable")
	tests := []struct {
		name       string
		rows       *sqlmock.Rows
		queryError error
		decode     bool
	}{
		{name: "query error", queryError: databaseErr},
		{name: "decode error", rows: sqlmock.NewRows([]string{"item"}).AddRow([]byte("{")), decode: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock := newEvaluationReaderSQLMock(t)
			expectation := mock.ExpectQuery(`SELECT jsonb_build_object`)
			if test.queryError != nil {
				expectation.WillReturnError(test.queryError)
			} else {
				expectation.WillReturnRows(test.rows)
			}

			reader := NewEvaluationReader(db, evaluationReaderTestRLS{})
			_, err := reader.ListEvaluationRefs(t.Context(), EvaluationListInput{
				TeamID: "00000000-0000-0000-0000-000000000101",
				Type:   "evidence",
				Limit:  1,
			})
			if test.queryError != nil {
				require.ErrorIs(t, err, test.queryError)
			} else if test.decode {
				require.ErrorContains(t, err, "decode evaluation item")
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestEvaluationReaderReturnsScanAndRowsErrors(t *testing.T) {
	t.Run("scan error", func(t *testing.T) {
		db, mock := newEvaluationReaderSQLMock(t)
		mock.ExpectQuery(`SELECT jsonb_build_object`).
			WillReturnRows(sqlmock.NewRows([]string{"item", "unexpected"}).AddRow(
				[]byte(`{"type":"evidence","id":"evidence-id"}`), "unexpected"))

		reader := NewEvaluationReader(db, evaluationReaderTestRLS{})
		_, err := reader.ListEvaluationRefs(t.Context(), EvaluationListInput{
			TeamID: "00000000-0000-0000-0000-000000000101",
			Type:   "evidence",
			Limit:  1,
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "expected 2 destination arguments")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rows error", func(t *testing.T) {
		db, mock := newEvaluationReaderSQLMock(t)
		rowsErr := errors.New("row iterator failed")
		mock.ExpectQuery(`SELECT jsonb_build_object`).
			WillReturnRows(sqlmock.NewRows([]string{"item"}).
				AddRow([]byte(`{"type":"evidence","id":"evidence-id"}`)).
				RowError(0, rowsErr))

		reader := NewEvaluationReader(db, evaluationReaderTestRLS{})
		_, err := reader.ListEvaluationRefs(t.Context(), EvaluationListInput{
			TeamID: "00000000-0000-0000-0000-000000000101",
			Type:   "evidence",
			Limit:  1,
		})
		require.ErrorIs(t, err, rowsErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestEvaluationReaderGetsItemsAndRejectsInvalidInputs(t *testing.T) {
	db, mock := newEvaluationReaderSQLMock(t)
	mock.ExpectQuery(`SELECT jsonb_build_object`).
		WillReturnRows(sqlmock.NewRows([]string{"item"}).AddRow([]byte(`{"type":"evidence","id":"evidence-id"}`)))

	reader := NewReader(db, evaluationReaderTestRLS{})
	item, err := reader.GetEvaluationItem(t.Context(), EvaluationGetInput{
		TeamID: "00000000-0000-0000-0000-000000000101",
		Type:   "evidence",
		ID:     "00000000-0000-0000-0000-000000000102",
	})
	require.NoError(t, err)
	require.Equal(t, "evidence-id", item["id"])
	require.NoError(t, mock.ExpectationsWereMet())

	_, err = reader.GetEvaluationItem(t.Context(), EvaluationGetInput{TeamID: "bad", Type: "evidence", ID: "bad"})
	require.Error(t, err)

	_, err = reader.ListEvaluationRefs(t.Context(), EvaluationListInput{TeamID: "bad", Type: "unsupported"})
	require.Error(t, err)
}

func TestEvaluationReaderGetReturnsNotFound(t *testing.T) {
	db, mock := newEvaluationReaderSQLMock(t)
	mock.ExpectQuery(`SELECT jsonb_build_object`).
		WillReturnRows(sqlmock.NewRows([]string{"item"}))

	reader := NewReader(db, evaluationReaderTestRLS{})
	_, err := reader.GetEvaluationItem(t.Context(), EvaluationGetInput{
		TeamID: "00000000-0000-0000-0000-000000000101",
		Type:   "evidence",
		ID:     "00000000-0000-0000-0000-000000000102",
	})
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvaluationReaderRejectsMissingDependencies(t *testing.T) {
	_, err := (&EvaluationReader{}).ListEvaluationRefs(t.Context(), EvaluationListInput{
		TeamID: "00000000-0000-0000-0000-000000000101",
		Type:   "evidence",
	})
	require.EqualError(t, err, "evaluation: list evidence: semantic: database is required")

	db, _ := newEvaluationReaderSQLMock(t)
	reader := NewEvaluationReader(db, nil)
	_, err = reader.ListEvaluationRefs(t.Context(), EvaluationListInput{
		TeamID: "00000000-0000-0000-0000-000000000101",
		Type:   "evidence",
	})
	require.EqualError(t, err, "evaluation: list evidence: semantic: rls helper is required")
}
