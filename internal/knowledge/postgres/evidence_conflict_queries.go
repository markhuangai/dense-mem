package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func loadEvidenceConflictPositions(ctx context.Context, tx *gorm.DB, teamID, conflictID string) ([]EvidenceConflictPositionRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`SELECT conflict_id::text, position_id::text, position_key, canonical_evidence_id::text, canonical_owner_profile_id::text, occurrence_id::text, occurrence_owner_profile_id::text, quote, span_start, span_end, authority, submitted, created_at FROM evidence_conflict_positions WHERE team_id = ?::uuid AND conflict_id = ?::uuid ORDER BY position_key, position_id`, teamID, conflictID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EvidenceConflictPositionRecord{}
	for rows.Next() {
		var item EvidenceConflictPositionRecord
		if err := rows.Scan(&item.ConflictID, &item.PositionID, &item.PositionKey, &item.CanonicalEvidenceID, &item.CanonicalOwnerProfileID, &item.OccurrenceID, &item.OccurrenceOwnerProfileID, &item.Quote, &item.SpanStart, &item.SpanEnd, &item.Authority, &item.Submitted, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Store) ResolveEvidenceConflict(ctx context.Context, input EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error) {
	input.TeamID, input.ConflictID, input.Decision, input.Reason = strings.TrimSpace(input.TeamID), strings.TrimSpace(input.ConflictID), strings.ToLower(strings.TrimSpace(input.Decision)), strings.TrimSpace(input.Reason)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(input.ConflictID); err != nil {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if input.Decision != "resolve" && input.Decision != "dismiss" {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if input.ExpectedVersion < 1 || input.Reason == "" || len([]rune(input.Reason)) > 512 {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if input.Decision == "dismiss" && strings.TrimSpace(input.PreferredPositionID) != "" {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if input.ActorKind == "" {
		input.ActorKind = "control"
	}
	if input.ActorKind != "control" && input.ActorKind != "system" {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if input.PreferredPositionID != "" {
		if _, err := uuid.Parse(input.PreferredPositionID); err != nil {
			return nil, ErrEvidenceConflictInvalidCommand
		}
	}
	var result *EvidenceConflictCaseRecord
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var caseKey, spaceID string
		var generation int64
		err := tx.WithContext(ctx).Raw(`SELECT case_key, space_id::text, space_generation FROM evidence_conflict_cases WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, input.TeamID, input.ConflictID).Row().Scan(&caseKey, &spaceID, &generation)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEvidenceConflictNotFound
		}
		if err != nil {
			return err
		}
		if err := lockEvidenceConflictCaseKeys(ctx, tx, input.TeamID, spaceID, generation, []string{caseKey}); err != nil {
			return err
		}
		var status string
		var version int
		if err := tx.WithContext(ctx).Raw(`SELECT status, version, space_id::text, space_generation FROM evidence_conflict_cases WHERE team_id = ?::uuid AND conflict_id = ?::uuid FOR UPDATE`, input.TeamID, input.ConflictID).Row().Scan(&status, &version, &spaceID, &generation); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrEvidenceConflictNotFound
			}
			return err
		}
		if status != "open" {
			return ErrEvidenceConflictNotOpen
		}
		if version != input.ExpectedVersion {
			return ErrEvidenceConflictVersionStale
		}
		if input.PreferredPositionID != "" {
			var exists bool
			if err := tx.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM evidence_conflict_positions WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND position_id = ?::uuid)`, input.TeamID, input.ConflictID, input.PreferredPositionID).Row().Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrEvidenceConflictInvalidCommand
			}
		}
		newStatus := "resolved"
		action := "resolved"
		if input.Decision == "dismiss" {
			newStatus = "dismissed"
			action = "dismissed"
		}
		positions, err := loadEvidenceConflictPositions(ctx, tx, input.TeamID, input.ConflictID)
		if err != nil {
			return err
		}
		ordinal, err := nextEvidenceConflictOrdinal(ctx, tx, input.TeamID, input.ConflictID)
		if err != nil {
			return err
		}
		newVersion := version + 1
		if err := tx.WithContext(ctx).Exec(`UPDATE evidence_conflict_cases SET status = ?, version = ?, preferred_position_id = NULLIF(?, '')::uuid, resolved_at = now(), resolution_reason = ?, updated_at = now() WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND status = 'open' AND version = ?`, newStatus, newVersion, input.PreferredPositionID, input.Reason, input.TeamID, input.ConflictID, version).Error; err != nil {
			return err
		}
		if err := insertEvidenceConflictEvent(ctx, tx, SynchronousRememberCommitInput{TeamID: input.TeamID, SpaceID: spaceID, SpaceGeneration: generation}, input.ConflictID, ordinal, action, newStatus, newVersion, input.ActorKind, input.ActorID, input.Reason, input.PreferredPositionID, positions); err != nil {
			return err
		}
		loaded, err := loadEvidenceConflictCaseForSystem(ctx, tx, input.TeamID, input.ConflictID)
		if err != nil {
			return err
		}
		result = loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func loadEvidenceConflictCaseForSystem(ctx context.Context, tx *gorm.DB, teamID, conflictID string) (*EvidenceConflictCaseRecord, error) {
	var item EvidenceConflictCaseRecord
	err := tx.WithContext(ctx).Raw(`SELECT team_id::text, conflict_id::text, space_id::text, space_generation, status, version, COALESCE(preferred_position_id::text, ''), resolved_at, resolution_reason, created_at, updated_at FROM evidence_conflict_cases WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, teamID, conflictID).Row().Scan(&item.TeamID, &item.ConflictID, &item.SpaceID, &item.SpaceGeneration, &item.Status, &item.Version, &item.PreferredPositionID, &item.ResolvedAt, &item.ResolutionReason, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEvidenceConflictNotFound
	}
	if err != nil {
		return nil, err
	}
	item.Kind = "evidence_conflict"
	item.Positions, err = loadEvidenceConflictPositions(ctx, tx, teamID, conflictID)
	if err != nil {
		return nil, err
	}
	return &item, nil
}
