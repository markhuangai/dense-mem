package conflictread

import (
	"context"
	"database/sql"
	"errors"

	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	"gorm.io/gorm"
)

func LoadEvidenceConflictPositions(ctx context.Context, tx *gorm.DB, teamID, conflictID string) ([]conflictcontract.EvidenceConflictPositionRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`SELECT conflict_id::text, position_id::text, position_key, canonical_evidence_id::text, canonical_owner_profile_id::text, occurrence_id::text, occurrence_owner_profile_id::text, quote, span_start, span_end, authority, submitted, created_at FROM evidence_conflict_positions WHERE team_id = ?::uuid AND conflict_id = ?::uuid ORDER BY position_key, position_id`, teamID, conflictID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []conflictcontract.EvidenceConflictPositionRecord{}
	for rows.Next() {
		var item conflictcontract.EvidenceConflictPositionRecord
		if err := rows.Scan(&item.ConflictID, &item.PositionID, &item.PositionKey, &item.CanonicalEvidenceID, &item.CanonicalOwnerProfileID, &item.OccurrenceID, &item.OccurrenceOwnerProfileID, &item.Quote, &item.SpanStart, &item.SpanEnd, &item.Authority, &item.Submitted, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func LoadCurrentEvidenceConflictCase(ctx context.Context, tx *gorm.DB, teamID, conflictID string) (*conflictcontract.EvidenceConflictCaseRecord, error) {
	var item conflictcontract.EvidenceConflictCaseRecord
	err := tx.WithContext(ctx).Raw(`SELECT team_id::text, conflict_id::text, space_id::text, space_generation, status, version, COALESCE(preferred_position_id::text, ''), resolved_at, resolution_reason, created_at, updated_at FROM evidence_conflict_cases WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, teamID, conflictID).Row().Scan(&item.TeamID, &item.ConflictID, &item.SpaceID, &item.SpaceGeneration, &item.Status, &item.Version, &item.PreferredPositionID, &item.ResolvedAt, &item.ResolutionReason, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, conflictcontract.ErrEvidenceConflictNotFound
	}
	if err != nil {
		return nil, err
	}
	item.Kind = "evidence_conflict"
	item.Positions, err = LoadEvidenceConflictPositions(ctx, tx, teamID, conflictID)
	if err != nil {
		return nil, err
	}
	return &item, nil
}
