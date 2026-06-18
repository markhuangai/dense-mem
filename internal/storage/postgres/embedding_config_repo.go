package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// EmbeddingConfigRecord represents a row in the embedding_config table.
// This is a singleton table (id=1) that stores the active embedding model configuration.
type EmbeddingConfigRecord struct {
	Model      string
	Dimensions int
	UpdatedAt  time.Time
}

// EmbeddingConfigRepository is the companion interface for embedding config data access.
type EmbeddingConfigRepository interface {
	// GetActive retrieves the active embedding config record.
	// Returns nil, nil if no record exists (first-write bootstrap pending).
	GetActive(ctx context.Context) (*EmbeddingConfigRecord, error)

	// Upsert inserts or updates the embedding config record.
	// This always operates on the singleton row (id=1).
	Upsert(ctx context.Context, model string, dimensions int) error
}

// postgresEmbeddingConfigRepo implements EmbeddingConfigRepository using Postgres/GORM.
type postgresEmbeddingConfigRepo struct {
	db *gorm.DB
}

// Ensure postgresEmbeddingConfigRepo implements EmbeddingConfigRepository
var _ EmbeddingConfigRepository = (*postgresEmbeddingConfigRepo)(nil)

// NewEmbeddingConfigRepository creates a new embedding config repository instance.
func NewEmbeddingConfigRepository(db *gorm.DB) EmbeddingConfigRepository {
	return &postgresEmbeddingConfigRepo{db: db}
}

// GetActive retrieves the active embedding config record.
// Returns nil, nil if no record exists.
func (r *postgresEmbeddingConfigRepo) GetActive(ctx context.Context) (*EmbeddingConfigRecord, error) {
	var record struct {
		Model      string
		Dimensions int
		UpdatedAt  time.Time
	}

	err := r.db.WithContext(ctx).Raw(`
		SELECT model, dimensions, updated_at
		FROM embedding_config
		WHERE id = 1
	`).Scan(&record).Error

	if err != nil {
		return nil, err
	}

	// If no row found, GORM doesn't return an error - we check if Model is empty
	if record.Model == "" {
		return nil, nil
	}

	return &EmbeddingConfigRecord{
		Model:      record.Model,
		Dimensions: record.Dimensions,
		UpdatedAt:  record.UpdatedAt,
	}, nil
}

// Upsert inserts or updates the embedding config record using ON CONFLICT.
func (r *postgresEmbeddingConfigRepo) Upsert(ctx context.Context, model string, dimensions int) error {
	return r.db.WithContext(ctx).Exec(`
		INSERT INTO embedding_config (id, model, dimensions, updated_at)
		VALUES (1, $1, $2, NOW())
		ON CONFLICT (id) DO UPDATE SET
			model = EXCLUDED.model,
			dimensions = EXCLUDED.dimensions,
			updated_at = NOW()
	`, model, dimensions).Error
}
