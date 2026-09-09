package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type communityPassthroughRLS struct{}

func (communityPassthroughRLS) WithTeamTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (communityPassthroughRLS) WithTeamProfileTx(_ context.Context, db *gorm.DB, _, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (communityPassthroughRLS) WithSystemTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (communityPassthroughRLS) WithTeamReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (communityPassthroughRLS) WithTeamProfileReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (communityPassthroughRLS) WithSystemReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}

func newCommunitySQLMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	return db, mock
}

func TestNormalizeCommunityRunClaimInputPreservesDefaultsAndBounds(t *testing.T) {
	teamID := uuid.NewString()
	got := normalizeCommunityRunClaimInput(CommunityRunClaimInput{TeamID: " " + teamID + " "})
	assert.Equal(t, teamID, got.TeamID)
	assert.Equal(t, CommunityAlgorithmKind, got.AlgorithmKind)
	assert.Equal(t, CommunityAlgorithmVersion, got.AlgorithmVersion)
	assert.Equal(t, CommunityProfileVersion, got.ProfileVersion)
	assert.False(t, got.LeaseUntil.IsZero())
}

func TestCommunityStoreReportsMissingDatabaseAndRLS(t *testing.T) {
	var store *Store
	_, err := store.ListCommunityInputs(context.Background(), CommunityInputListInput{TeamID: uuid.NewString()})
	assert.EqualError(t, err, "community: list inputs: community: database is required")
}

func TestCommunityStoreListCommunitiesBindsFenceAndScansRows(t *testing.T) {
	db, mock := newCommunitySQLMockDB(t)
	teamID := uuid.NewString()
	spaceID := uuid.NewString()
	communityID := uuid.NewString()
	runID := uuid.NewString()
	createdAt := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text, generation")).
		WithArgs(teamID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "generation"}).AddRow(spaceID, int64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT team_id::text, community_id::text")).
		WithArgs(teamID, spaceID, int64(4), "current", 10).
		WillReturnRows(sqlmock.NewRows([]string{
			"team_id", "community_id", "logical_community_id", "run_id", "ordinal", "status",
			"summary", "summary_version", "member_count", "source_count", "top_entities", "top_predicates",
			"source_fingerprint", "stale_reason", "created_at", "updated_at", "superseded_at",
		}).AddRow(
			teamID, communityID, communityID, runID, 2, "current", "summary", "v1", 3, 4,
			"{Dense-Mem,PostgreSQL}", "{uses,works_on}", "sha256:source", "", createdAt,
			createdAt.Add(time.Minute), nil,
		))

	store := NewStore(db, communityPassthroughRLS{})
	records, err := store.ListCommunities(context.Background(), CommunityListInput{TeamID: teamID, Limit: 10})

	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, teamID, records[0].TeamID)
	assert.Equal(t, communityID, records[0].CommunityID)
	assert.Equal(t, []string{"Dense-Mem", "PostgreSQL"}, records[0].TopEntities)
	assert.Equal(t, []string{"uses", "works_on"}, records[0].TopPredicates)
	assert.Equal(t, createdAt, records[0].CreatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCommunityStoreListCommunitiesReturnsFenceQueryError(t *testing.T) {
	db, mock := newCommunitySQLMockDB(t)
	teamID := uuid.NewString()
	fenceErr := errors.New("community fence query failed")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text, generation")).
		WithArgs(teamID).
		WillReturnError(fenceErr)

	store := NewStore(db, communityPassthroughRLS{})
	_, err := store.ListCommunities(context.Background(), CommunityListInput{TeamID: teamID})

	require.ErrorIs(t, err, fenceErr)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCommunityStoreListCommunitiesReturnsRowScanError(t *testing.T) {
	db, mock := newCommunitySQLMockDB(t)
	teamID := uuid.NewString()
	spaceID := uuid.NewString()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text, generation")).
		WithArgs(teamID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "generation"}).AddRow(spaceID, int64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT team_id::text, community_id::text")).
		WithArgs(teamID, spaceID, int64(4), "current", 20).
		WillReturnRows(sqlmock.NewRows([]string{
			"team_id", "community_id", "logical_community_id", "run_id", "ordinal", "status",
			"summary", "summary_version", "member_count", "source_count", "top_entities", "top_predicates",
			"source_fingerprint", "stale_reason", "created_at", "updated_at", "superseded_at",
		}).AddRow(
			teamID, uuid.NewString(), uuid.NewString(), uuid.NewString(), "not-an-int", "current", "summary", "v1", 3, 4,
			"{}", "{}", "sha256:source", "", time.Now().UTC(), time.Now().UTC(), nil,
		))

	store := NewStore(db, communityPassthroughRLS{})
	_, err := store.ListCommunities(context.Background(), CommunityListInput{TeamID: teamID})

	require.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
