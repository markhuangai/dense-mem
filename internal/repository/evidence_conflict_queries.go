package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (r *LedgerRepositoryImpl) ListEvidenceConflicts(ctx context.Context, input EvidenceConflictListInput) (*EvidenceConflictListResult, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if input.Status != "" && !validEvidenceConflictStatus(input.Status) {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if input.Limit == 0 {
		input.Limit = EvidenceConflictDefaultLimit
	}
	if input.Limit < 1 || input.Limit > EvidenceConflictMaxLimit {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if input.Cursor != nil {
		if err := input.Cursor.validate(input.TeamID, input.Status); err != nil {
			return nil, err
		}
	}
	result := &EvidenceConflictListResult{Items: []EvidenceConflictCaseRecord{}}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		where := "WHERE team_id = ?::uuid"
		args := []any{input.TeamID}
		if input.Status != "" {
			where += " AND status = ?"
			args = append(args, input.Status)
		}
		if input.Cursor != nil {
			where += " AND (updated_at < ? OR (updated_at = ? AND conflict_id < ?::uuid))"
			args = append(args, input.Cursor.UpdatedAt, input.Cursor.UpdatedAt, input.Cursor.ConflictID)
		}
		args = append(args, input.Limit+1)
		rows, err := tx.WithContext(ctx).Raw(`SELECT team_id::text, conflict_id::text, space_id::text, space_generation, status, version, COALESCE(preferred_position_id::text, ''), resolved_at, resolution_reason, created_at, updated_at FROM evidence_conflict_cases `+where+` ORDER BY updated_at DESC, conflict_id DESC LIMIT ?`, args...).Rows()
		if err != nil {
			return err
		}
		cases := make([]EvidenceConflictCaseRecord, 0, input.Limit+1)
		for rows.Next() {
			var item EvidenceConflictCaseRecord
			if err := rows.Scan(&item.TeamID, &item.ConflictID, &item.SpaceID, &item.SpaceGeneration, &item.Status, &item.Version, &item.PreferredPositionID, &item.ResolvedAt, &item.ResolutionReason, &item.CreatedAt, &item.UpdatedAt); err != nil {
				_ = rows.Close()
				return err
			}
			item.Kind = "evidence_conflict"
			cases = append(cases, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for index := range cases {
			cases[index].Positions, err = loadEvidenceConflictPositions(ctx, tx, input.TeamID, cases[index].ConflictID)
			if err != nil {
				return err
			}
		}
		result.Items = cases
		if len(result.Items) > input.Limit {
			last := result.Items[input.Limit-1]
			result.Items = result.Items[:input.Limit]
			cursor := EvidenceConflictCursor{Version: 1, TeamID: input.TeamID, StatusFilter: input.Status, UpdatedAt: last.UpdatedAt, ConflictID: last.ConflictID}
			result.NextCursor = &cursor
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *LedgerRepositoryImpl) GetEvidenceConflict(ctx context.Context, input EvidenceConflictGetInput) (*EvidenceConflictGetResult, error) {
	input.TeamID, input.ConflictID = strings.TrimSpace(input.TeamID), strings.TrimSpace(input.ConflictID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ConflictID); err != nil {
		return nil, fmt.Errorf("conflict_id is required: %w", err)
	}
	if input.EventLimit == 0 {
		input.EventLimit = EvidenceConflictDefaultEventLimit
	}
	if input.EventLimit < 1 || input.EventLimit > EvidenceConflictMaxEventLimit {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	result := &EvidenceConflictGetResult{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var item EvidenceConflictCaseRecord
		err := tx.WithContext(ctx).Raw(`SELECT team_id::text, conflict_id::text, space_id::text, space_generation, status, version, COALESCE(preferred_position_id::text, ''), resolved_at, resolution_reason, created_at, updated_at FROM evidence_conflict_cases WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, input.TeamID, input.ConflictID).Row().Scan(&item.TeamID, &item.ConflictID, &item.SpaceID, &item.SpaceGeneration, &item.Status, &item.Version, &item.PreferredPositionID, &item.ResolvedAt, &item.ResolutionReason, &item.CreatedAt, &item.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEvidenceConflictNotFound
		}
		if err != nil {
			return err
		}
		item.Kind = "evidence_conflict"
		positions, err := loadEvidenceConflictPositions(ctx, tx, input.TeamID, input.ConflictID)
		if err != nil {
			return err
		}
		item.Positions = positions
		events, next, err := loadEvidenceConflictEvents(ctx, tx, input, item.SpaceID)
		if err != nil {
			return err
		}
		item.Events = events
		result.Conflict = &item
		result.NextEventCursor = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

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

func loadEvidenceConflictEvents(ctx context.Context, tx *gorm.DB, input EvidenceConflictGetInput, _ string) ([]EvidenceConflictEventRecord, *EvidenceConflictEventCursor, error) {
	where := "WHERE team_id = ?::uuid AND conflict_id = ?::uuid"
	args := []any{input.TeamID, input.ConflictID}
	if input.EventCursor != nil {
		if err := input.EventCursor.validate(input.TeamID, input.ConflictID); err != nil {
			return nil, nil, err
		}
		where += " AND (ordinal < ? OR (ordinal = ? AND conflict_event_id < ?::uuid))"
		args = append(args, input.EventCursor.Ordinal, input.EventCursor.Ordinal, input.EventCursor.EventID)
	}
	args = append(args, input.EventLimit+1)
	rows, err := tx.WithContext(ctx).Raw(`SELECT conflict_event_id::text, conflict_id::text, ordinal, action, status_after, case_version, actor_kind, actor_id, reason, COALESCE(preferred_position_id::text, ''), citation_snapshot, created_at FROM evidence_conflict_events `+where+` ORDER BY ordinal DESC, conflict_event_id DESC LIMIT ?`, args...).Rows()
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	events := []EvidenceConflictEventRecord{}
	for rows.Next() {
		var event EvidenceConflictEventRecord
		var raw []byte
		if err := rows.Scan(&event.ConflictEventID, &event.ConflictID, &event.Ordinal, &event.Action, &event.StatusAfter, &event.CaseVersion, &event.ActorKind, &event.ActorID, &event.Reason, &event.PreferredPositionID, &raw, &event.CreatedAt); err != nil {
			return nil, nil, err
		}
		if len(raw) == 0 {
			event.CitationSnapshot = []EvidenceConflictPositionRecord{}
		} else if err := json.Unmarshal(raw, &event.CitationSnapshot); err != nil {
			return nil, nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *EvidenceConflictEventCursor
	if len(events) > input.EventLimit {
		last := events[input.EventLimit-1]
		events = events[:input.EventLimit]
		next = &EvidenceConflictEventCursor{Version: 1, TeamID: input.TeamID, ConflictID: input.ConflictID, Ordinal: last.Ordinal, EventID: last.ConflictEventID}
	}
	return events, next, nil
}

func (r *LedgerRepositoryImpl) ResolveEvidenceConflict(ctx context.Context, input EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error) {
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
		newVersion := version + 1
		now := time.Now().UTC()
		if err := tx.WithContext(ctx).Exec(`UPDATE evidence_conflict_cases SET status = ?, version = ?, preferred_position_id = NULLIF(?, '')::uuid, resolved_at = ?, resolution_reason = ?, updated_at = ? WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND status = 'open' AND version = ?`, newStatus, newVersion, input.PreferredPositionID, now, input.Reason, now, input.TeamID, input.ConflictID, version).Error; err != nil {
			return err
		}
		if err := insertEvidenceConflictEvent(ctx, tx, SynchronousRememberCommitInput{TeamID: input.TeamID, SpaceID: spaceID, SpaceGeneration: generation}, input.ConflictID, nextEvidenceConflictOrdinal(ctx, tx, input.TeamID, input.ConflictID), action, newStatus, newVersion, input.ActorKind, input.ActorID, input.Reason, input.PreferredPositionID, nil); err != nil {
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
