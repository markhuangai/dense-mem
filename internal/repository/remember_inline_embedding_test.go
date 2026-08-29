package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSynchronousRememberEmbeddingResultRejectsDuplicateHashes(t *testing.T) {
	result := SynchronousRememberEmbeddingResult{
		EmbeddingContractID:     uuid.NewString(),
		EmbeddingDimensions:     2,
		EmbeddingModel:          "embedding-test",
		SearchGenerationID:      uuid.NewString(),
		SearchGenerationVersion: 1,
		Embeddings: []SynchronousRememberEmbedding{
			{DocumentHash: searchDocumentTextHash("same"), Vector: []float32{1, 0}},
			{DocumentHash: searchDocumentTextHash("same"), Vector: []float32{1, 0}},
		},
	}

	err := validateSynchronousRememberEmbeddingResult(result)

	require.ErrorIs(t, err, ErrSynchronousRememberEmbeddingFence)
}

func TestSearchDocumentTextHashNormalizesWhitespace(t *testing.T) {
	require.Equal(t, searchDocumentTextHash("document"), searchDocumentTextHash(" document "))
}

func TestSynchronousRememberEmbeddingEntityTextsClassifiesInactiveReuse(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	teamID, entityID := uuid.NewString(), uuid.NewString()
	mock.ExpectQuery(`SELECT COALESCE\(canonical\.display_name`).WithArgs(teamID, entityID).WillReturnError(sql.ErrNoRows)

	_, err = synchronousRememberEmbeddingEntityTexts(context.Background(), db, teamID, &ActiveSearchContract{}, []SubmissionAssessmentEntityResolutionInput{{Resolution: PlacementEntityResolutionInput{EntityID: entityID}}})
	require.ErrorIs(t, err, ErrRememberExactReferenceStale)
	require.NoError(t, mock.ExpectationsWereMet())
}
