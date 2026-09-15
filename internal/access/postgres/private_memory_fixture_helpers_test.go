package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const privateMemoryAuditMetadataJSON = `{"private_content_erased": true}`

func seedPrivateMemoryIngest(t *testing.T, db *gorm.DB, rls storagepostgres.RLSHelper, teamID, ownerID, spaceID uuid.UUID, content string) uuid.UUID {
	t.Helper()
	ingestID := uuid.New()
	createdAt := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, request_hash, source_summary,
				status, proposal, metadata, space_id, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, 'queued', '{}'::jsonb, '{}'::jsonb, ?, ?, ?)
		`, teamID, ingestID, ownerID, "hash-"+ingestID.String(), content, spaceID, createdAt, createdAt).Error
	}))
	return ingestID
}
