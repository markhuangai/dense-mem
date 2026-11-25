package casewrite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	contract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/storage/postgres/conflictread"
	"gorm.io/gorm"
)

type caseScope struct {
	TeamID, SpaceID, OwnerProfileID string
	SpaceGeneration                 int64
}

// CommitCitedCasesTx persists the validated Conflict positions in the caller's
// Evidence transaction.
func CommitCitedCasesTx(ctx context.Context, tx *gorm.DB, teamID, spaceID string, generation int64, ownerID string, keys []string, positions [][]contract.EvidenceConflictPositionRecord) error {
	if len(keys) != len(positions) {
		return contract.ErrEvidenceConflictInvalidCommand
	}
	if err := lockEvidenceConflictCaseKeys(ctx, tx, teamID, spaceID, generation, keys); err != nil {
		return err
	}
	input := caseScope{TeamID: teamID, SpaceID: spaceID, OwnerProfileID: ownerID, SpaceGeneration: generation}
	for i, key := range keys {
		if err := upsertCitedCase(ctx, tx, input, key, positions[i]); err != nil {
			return err
		}
	}
	return nil
}

func upsertCitedCase(
	ctx context.Context,
	tx *gorm.DB,
	input caseScope,
	caseKey string,
	positions []contract.EvidenceConflictPositionRecord,
) error {
	var conflictID, status, preferred string
	var version int
	err := tx.WithContext(ctx).Raw(`
		SELECT conflict_id::text, status, version, COALESCE(preferred_position_id::text, '')
		FROM evidence_conflict_cases
		WHERE team_id = ?::uuid AND space_id = ?::uuid AND space_generation = ? AND case_key = ?
		FOR UPDATE
	`, input.TeamID, input.SpaceID, input.SpaceGeneration, caseKey).Row().Scan(&conflictID, &status, &version, &preferred)
	if errors.Is(err, sql.ErrNoRows) {
		conflictID = uuid.NewString()
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO evidence_conflict_cases (team_id, conflict_id, space_id, space_generation, case_key, status, version)
			VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, 'open', 1)
		`, input.TeamID, conflictID, input.SpaceID, input.SpaceGeneration, caseKey).Error; err != nil {
			return err
		}
		for index := range positions {
			positions[index].ConflictID = conflictID
			positions[index].PositionID = uuid.NewString()
			if err := insertEvidenceConflictPosition(ctx, tx, input, positions[index]); err != nil {
				return err
			}
		}
		storedPositions, err := conflictread.LoadEvidenceConflictPositions(ctx, tx, input.TeamID, conflictID)
		if err != nil {
			return err
		}
		return insertEvidenceConflictEvent(ctx, tx, input, conflictID, 1, "opened", "open", 1, "profile", input.OwnerProfileID, "", "", storedPositions)
	}
	if err != nil {
		return err
	}
	storedPositions, err := conflictread.LoadEvidenceConflictPositions(ctx, tx, input.TeamID, conflictID)
	if err != nil {
		return err
	}
	storedPositionIDs := make(map[string]contract.EvidenceConflictPositionRecord, len(storedPositions))
	for _, stored := range storedPositions {
		storedPositionIDs[stored.PositionKey] = stored
	}
	for index := range positions {
		stored, exists := storedPositionIDs[positions[index].PositionKey]
		if !exists {
			return errors.New("evidence conflict recurrence position set does not match the stored case")
		}
		positions[index].ConflictID = conflictID
		positions[index].PositionID = stored.PositionID
		positions[index].CreatedAt = stored.CreatedAt
	}
	if status == "open" {
		version++
		if err := tx.WithContext(ctx).Exec(`
			UPDATE evidence_conflict_cases SET version = ?, updated_at = now()
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND status = 'open' AND version = ?
		`, version, input.TeamID, conflictID, version-1).Error; err != nil {
			return err
		}
		ordinal, err := nextEvidenceConflictOrdinal(ctx, tx, input.TeamID, conflictID)
		if err != nil {
			return err
		}
		return insertEvidenceConflictEvent(ctx, tx, input, conflictID, ordinal, "recited", "open", version, "profile", input.OwnerProfileID, "", preferred, positions)
	}
	ordinal, err := nextEvidenceConflictOrdinal(ctx, tx, input.TeamID, conflictID)
	if err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		UPDATE evidence_conflict_cases SET updated_at = now()
		WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND status = ? AND version = ?
	`, input.TeamID, conflictID, status, version).Error; err != nil {
		return err
	}
	return insertEvidenceConflictEvent(ctx, tx, input, conflictID, ordinal, "recited", status, version, "profile", input.OwnerProfileID, "", preferred, positions)
}

func evidenceConflictCaseLockKey(teamID, spaceID string, generation int64, caseKey string) string {
	return strings.Join([]string{"evidence-conflict", teamID, spaceID, fmt.Sprintf("%d", generation), caseKey}, ":")
}

func lockEvidenceConflictCaseKeys(ctx context.Context, tx *gorm.DB, teamID, spaceID string, generation int64, caseKeys []string) error {
	ordered := append([]string(nil), caseKeys...)
	sort.Strings(ordered)
	for _, caseKey := range ordered {
		if err := tx.WithContext(ctx).Exec(
			"SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))",
			evidenceConflictCaseLockKey(teamID, spaceID, generation, caseKey),
		).Error; err != nil {
			return err
		}
	}
	return nil
}

func insertEvidenceConflictPosition(ctx context.Context, tx *gorm.DB, input caseScope, position contract.EvidenceConflictPositionRecord) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO evidence_conflict_positions (
			team_id, conflict_id, space_id, space_generation, position_id, position_key,
			canonical_evidence_id, canonical_owner_profile_id, occurrence_id, occurrence_owner_profile_id,
			quote, span_start, span_end, authority, submitted
		) VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?)
	`, input.TeamID, position.ConflictID, input.SpaceID, input.SpaceGeneration, position.PositionID, position.PositionKey,
		position.CanonicalEvidenceID, position.CanonicalOwnerProfileID, position.OccurrenceID, position.OccurrenceOwnerProfileID,
		position.Quote, position.SpanStart, position.SpanEnd, position.Authority, position.Submitted).Error
}

func nextEvidenceConflictOrdinal(ctx context.Context, tx *gorm.DB, teamID, conflictID string) (int64, error) {
	var ordinal int64
	if err := tx.WithContext(ctx).Raw(`SELECT COALESCE(max(ordinal), 0) + 1 FROM evidence_conflict_events WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, teamID, conflictID).Row().Scan(&ordinal); err != nil {
		return 0, err
	}
	if ordinal < 1 {
		return 1, nil
	}
	return ordinal, nil
}

func insertEvidenceConflictEvent(ctx context.Context, tx *gorm.DB, input caseScope, conflictID string, ordinal int64, action, status string, version int, actorKind, actorID, reason, preferred string, positions []contract.EvidenceConflictPositionRecord) error {
	snapshot := make([]map[string]any, 0, len(positions))
	for _, position := range positions {
		snapshot = append(snapshot, map[string]any{
			"position_id": position.PositionID, "position_key": position.PositionKey,
			"evidence_id": position.CanonicalEvidenceID, "canonical_owner_profile_id": position.CanonicalOwnerProfileID,
			"occurrence_id": position.OccurrenceID, "occurrence_owner_profile_id": position.OccurrenceOwnerProfileID,
			"quote": position.Quote, "span_start": position.SpanStart, "span_end": position.SpanEnd,
			"authority": position.Authority, "submitted": position.Submitted, "created_at": position.CreatedAt,
		})
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO evidence_conflict_events (
			team_id, conflict_event_id, conflict_id, space_id, space_generation, ordinal,
			action, status_after, case_version, actor_kind, actor_id, reason, preferred_position_id, citation_snapshot
		) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, '')::uuid, ?::jsonb)
	`, input.TeamID, uuid.NewString(), conflictID, input.SpaceID, input.SpaceGeneration, ordinal, action, status, version, actorKind, actorID, reason, preferred, string(encoded)).Error
}
