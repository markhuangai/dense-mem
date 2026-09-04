package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedPrivateMemoryEvidenceConflict(t *testing.T, db *gorm.DB, rls interface {
	WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
}, teamID uuid.UUID, target *domain.Credential, ingestID uuid.UUID) {
	t.Helper()
	conflictID, firstPositionID, secondPositionID, fragmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		var generation int64
		if err := tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?`, target.MemorySpaceID).Row().Scan(&generation); err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE knowledge_ingests SET status = 'completed' WHERE team_id = ? AND ingest_id = ?`, teamID, ingestID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				content, content_hash, source_type, authority, labels, metadata,
				space_id, space_generation
			) VALUES (?, ?, ?, ?, 0, 'private conflict citation', ?, 'manual', 'primary', ARRAY[]::text[], '{}'::jsonb, ?, ?)
		`, teamID, fragmentID, ingestID, target.ID, sha256Hex("private conflict citation"), target.MemorySpaceID, generation).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO evidence_conflict_cases (team_id, conflict_id, space_id, space_generation, case_key, status, version)
			VALUES (?, ?, ?, ?, ?, 'open', 1)
		`, teamID, conflictID, target.MemorySpaceID, generation, "private-erasure-conflict").Error; err != nil {
			return err
		}
		for _, position := range []struct {
			id        uuid.UUID
			key       string
			submitted bool
		}{{firstPositionID, "private-erasure-position-a", true}, {secondPositionID, "private-erasure-position-b", false}} {
			if err := tx.Exec(`
				INSERT INTO evidence_conflict_positions (
					team_id, conflict_id, space_id, space_generation, position_id, position_key,
					canonical_evidence_id, canonical_owner_profile_id, occurrence_id,
					occurrence_owner_profile_id, quote, span_start, span_end, authority, submitted
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'private', 0, 7, 'primary', ?)
			`, teamID, conflictID, target.MemorySpaceID, generation, position.id, position.key, fragmentID, target.ID, fragmentID, target.ID, position.submitted).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`
			INSERT INTO evidence_conflict_events (
				team_id, conflict_event_id, conflict_id, space_id, space_generation, ordinal,
				action, status_after, case_version, actor_kind, actor_id, citation_snapshot
			) VALUES (?, ?, ?, ?, ?, 1, 'opened', 'open', 1, 'profile', ?, '[]'::jsonb)
		`, teamID, uuid.New(), conflictID, target.MemorySpaceID, generation, target.ID).Error
	}))
}
