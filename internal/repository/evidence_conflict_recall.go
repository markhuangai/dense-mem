package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

const evidenceConflictRecallCandidateLimit = EvidenceConflictMaxResults * recallOverfetchMultiple

func loadRecallEvidenceConflictRecords(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
	results []RecallEvidenceHit,
) ([]EvidenceConflictCaseRecord, error) {
	evidenceIDs := make([]string, 0, len(results))
	seenEvidence := make(map[string]struct{}, len(results))
	for _, result := range results {
		id := strings.TrimSpace(result.EvidenceID)
		if id == "" {
			continue
		}
		if _, exists := seenEvidence[id]; exists {
			continue
		}
		seenEvidence[id] = struct{}{}
		evidenceIDs = append(evidenceIDs, id)
	}
	if len(evidenceIDs) == 0 {
		return []EvidenceConflictCaseRecord{}, nil
	}
	spaceID := strings.TrimSpace(input.SpaceID)
	spaceClause := recallSpacePredicate("conflict.space_id", input.TeamID, spaceID, input.SpaceKind)
	where := `
		WHERE conflict.team_id = ?::uuid
		  AND position.canonical_evidence_id = ANY(?::uuid[])
	` + spaceClause
	args := []any{input.TeamID, pq.Array(evidenceIDs)}
	where += `
		  AND NOT EXISTS (
			  SELECT 1
			  FROM evidence_conflict_positions AS candidate_position
			  WHERE candidate_position.team_id = conflict.team_id
				AND candidate_position.conflict_id = conflict.conflict_id
				AND NOT (` + evidenceConflictPositionEligibilitySQL("candidate_position") + `)
		  )
	`
	args = append(args, evidenceConflictPositionEligibilityArgs(input)...)
	if input.KnownAt == nil {
		where += ` AND conflict.space_generation = dense_mem_active_space_generation(conflict.team_id, conflict.space_id)
		  AND conflict.status IN ('open', 'resolved')`
	} else {
		where += ` AND conflict.created_at <= ?::timestamptz`
		args = append(args, *input.KnownAt)
		where += ` AND COALESCE((
			SELECT event.status_after
			FROM evidence_conflict_events AS event
			WHERE event.team_id = conflict.team_id
			  AND event.conflict_id = conflict.conflict_id
			  AND event.created_at <= ?::timestamptz
			ORDER BY event.ordinal DESC, event.conflict_event_id DESC
			LIMIT 1
		), conflict.status) IN ('open', 'resolved')`
		args = append(args, *input.KnownAt)
	}
	groupBy := "conflict.team_id, conflict.conflict_id, conflict.updated_at"
	orderBy := "conflict.updated_at DESC"
	if input.KnownAt != nil {
		orderBy = `(SELECT event.created_at
			FROM evidence_conflict_events AS event
			WHERE event.team_id = conflict.team_id
			  AND event.conflict_id = conflict.conflict_id
			  AND event.created_at <= ?::timestamptz
			ORDER BY event.created_at DESC, event.ordinal DESC, event.conflict_event_id DESC
			LIMIT 1) DESC NULLS LAST`
		args = append(args, *input.KnownAt)
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT conflict.conflict_id::text
		FROM evidence_conflict_cases AS conflict
		JOIN evidence_conflict_positions AS position
		  ON position.team_id = conflict.team_id AND position.conflict_id = conflict.conflict_id
		`+where+` GROUP BY `+groupBy+` ORDER BY `+orderBy+`, conflict.conflict_id DESC LIMIT ?`, append(args, evidenceConflictRecallCandidateLimit)...).Rows()
	if err != nil {
		return nil, err
	}
	conflictIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		conflictIDs = append(conflictIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]EvidenceConflictCaseRecord, 0, len(conflictIDs))
	for _, conflictID := range conflictIDs {
		item, err := loadRecallEvidenceConflictCase(ctx, tx, input.TeamID, conflictID, input.KnownAt)
		if errors.Is(err, ErrEvidenceConflictNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if authorized, err := evidenceConflictCasePositionsAuthorized(ctx, tx, input, *item); err != nil {
			return nil, err
		} else if !authorized {
			continue
		}
		if item.Status == "dismissed" {
			continue
		}
		item.Kind = "evidence_conflict"
		out = append(out, *item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ConflictID > out[j].ConflictID
	})
	if len(out) > EvidenceConflictMaxResults {
		out = out[:EvidenceConflictMaxResults]
	}
	return out, nil
}

func evidenceConflictCasePositionsAuthorized(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
	item EvidenceConflictCaseRecord,
) (bool, error) {
	if len(item.Positions) == 0 {
		return false, nil
	}
	var authorized int
	query := `
		SELECT count(*)::int
		FROM evidence_conflict_positions AS position
		WHERE position.team_id = ?::uuid
		  AND position.conflict_id = ?::uuid
		  AND ` + evidenceConflictPositionEligibilitySQL("position") + `
		`
	args := append([]any{input.TeamID, item.ConflictID}, evidenceConflictPositionEligibilityArgs(input)...)
	if err := tx.WithContext(ctx).Raw(query, args...).Row().Scan(&authorized); err != nil {
		return false, err
	}
	return authorized == len(item.Positions), nil
}

func evidenceConflictPositionEligibilitySQL(positionAlias string) string {
	return fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM evidence_fragments AS fragment
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = fragment.team_id
		 AND ingest.ingest_id = fragment.ingest_id
		JOIN memory_spaces AS space
		  ON space.team_id = fragment.team_id
		 AND space.id = fragment.space_id
		LEFT JOIN evidence_sources AS source
		  ON source.team_id = fragment.team_id
		 AND source.source_id = fragment.source_id
		 AND source.owner_profile_id = fragment.owner_profile_id
		JOIN evidence_occurrences AS occurrence
		  ON occurrence.team_id = fragment.team_id
		 AND occurrence.occurrence_id = %[1]s.occurrence_id
		 AND occurrence.canonical_fragment_id = fragment.fragment_id
		 AND occurrence.canonical_owner_profile_id = fragment.owner_profile_id
		 AND occurrence.owner_profile_id = %[1]s.occurrence_owner_profile_id
		WHERE fragment.team_id = %[1]s.team_id
		  AND fragment.fragment_id = %[1]s.canonical_evidence_id
		  AND fragment.owner_profile_id = %[1]s.canonical_owner_profile_id
		  AND fragment.space_id = %[1]s.space_id
		  AND fragment.space_generation = %[1]s.space_generation
		  AND (?::timestamptz IS NOT NULL OR fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id))
		  AND ingest.status = 'completed'
		  AND space.lifecycle_state = 'active'
		  AND (space.kind = 'team_shared' OR dense_mem_space_allowed(space.id))
		  `+recallEvidenceAliasVisibilitySQL("fragment")+`
		  AND NOT EXISTS (
		      SELECT 1
		      FROM evidence_lifecycle_events AS lifecycle
		      WHERE lifecycle.team_id = fragment.team_id
		        AND lifecycle.target_fragment_id = fragment.fragment_id
		        AND (?::timestamptz IS NULL OR lifecycle.created_at <= ?::timestamptz)
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM evidence_quarantines AS quarantine
		      WHERE quarantine.team_id = fragment.team_id
		        AND quarantine.fragment_id = fragment.fragment_id
		        AND quarantine.status = 'active'
		  )
		  AND NOT (
		      ingest.source_summary = 'overdue conflict deletion-only derivation'
		      AND ingest.metadata ->> 'conflict_resolution_deletion_only' = 'true'
		  )
		  `+recallEvidenceHistoricalSourceVisibilitySQL("fragment", "source")+`
		  AND (?::timestamptz IS NULL OR fragment.created_at <= ?::timestamptz)
	)`, positionAlias)
}

func evidenceConflictPositionEligibilityArgs(input RecallEvidenceInput) []any {
	eventAt := recallEventAt(input.ValidAt, input.KnownAt)
	return []any{eventAt, eventAt, eventAt, eventAt, input.KnownAt, eventAt, input.KnownAt, input.KnownAt}
}

func loadRecallEvidenceConflictCase(ctx context.Context, tx *gorm.DB, teamID, conflictID string, knownAt *time.Time) (*EvidenceConflictCaseRecord, error) {
	var item EvidenceConflictCaseRecord
	err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, conflict_id::text, space_id::text, space_generation,
		       status, version, COALESCE(preferred_position_id::text, ''),
		       resolved_at, resolution_reason, created_at, updated_at
		FROM evidence_conflict_cases
		WHERE team_id = ?::uuid AND conflict_id = ?::uuid
	`, teamID, conflictID).Row().Scan(&item.TeamID, &item.ConflictID, &item.SpaceID, &item.SpaceGeneration, &item.Status, &item.Version, &item.PreferredPositionID, &item.ResolvedAt, &item.ResolutionReason, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEvidenceConflictNotFound
	}
	if err != nil {
		return nil, err
	}
	item.Kind = "evidence_conflict"
	positions, err := loadEvidenceConflictPositions(ctx, tx, teamID, conflictID)
	if err != nil {
		return nil, err
	}
	item.Positions = positions
	if knownAt == nil {
		return &item, nil
	}
	events, err := loadEvidenceConflictEventsAt(ctx, tx, teamID, conflictID, knownAt)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, ErrEvidenceConflictNotFound
	}
	item.ResolvedAt = nil
	item.ResolutionReason = ""
	for _, event := range events {
		if event.Action != "resolved" && event.Action != "dismissed" {
			continue
		}
		resolvedAt := event.CreatedAt
		item.ResolvedAt = &resolvedAt
		item.ResolutionReason = event.Reason
	}
	last := events[len(events)-1]
	item.Status = last.StatusAfter
	item.Version = last.CaseVersion
	item.PreferredPositionID = last.PreferredPositionID
	item.UpdatedAt = last.CreatedAt
	return &item, nil
}

func loadEvidenceConflictEventsAt(ctx context.Context, tx *gorm.DB, teamID, conflictID string, knownAt *time.Time) ([]EvidenceConflictEventRecord, error) {
	const selectColumns = `
		SELECT conflict_event_id::text, conflict_id::text, ordinal, action, status_after,
		       case_version, actor_kind, actor_id, reason,
		       COALESCE(preferred_position_id::text, ''), citation_snapshot, created_at
		FROM evidence_conflict_events
		WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND created_at <= ?::timestamptz
	`
	queries := []string{
		selectColumns + ` ORDER BY ordinal DESC, conflict_event_id DESC LIMIT 1`,
		selectColumns + ` AND action IN ('resolved', 'dismissed') ORDER BY ordinal DESC, conflict_event_id DESC LIMIT 1`,
	}
	out := make([]EvidenceConflictEventRecord, 0, len(queries))
	seen := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		var event EvidenceConflictEventRecord
		var raw []byte
		err := tx.WithContext(ctx).Raw(query, teamID, conflictID, knownAt).Row().Scan(&event.ConflictEventID, &event.ConflictID, &event.Ordinal, &event.Action, &event.StatusAfter, &event.CaseVersion, &event.ActorKind, &event.ActorID, &event.Reason, &event.PreferredPositionID, &raw, &event.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, exists := seen[event.ConflictEventID]; exists {
			continue
		}
		seen[event.ConflictEventID] = struct{}{}
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &event.CitationSnapshot); err != nil {
				return nil, fmt.Errorf("decode evidence conflict citation history: %w", err)
			}
		}
		if event.CitationSnapshot == nil {
			event.CitationSnapshot = []EvidenceConflictPositionRecord{}
		}
		out = append(out, event)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ordinal != out[j].Ordinal {
			return out[i].Ordinal < out[j].Ordinal
		}
		return out[i].ConflictEventID < out[j].ConflictEventID
	})
	return out, nil
}
