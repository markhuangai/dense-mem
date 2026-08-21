package repository

import (
	"context"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

func loadActiveRelationshipConflictRecordsByIDBounded(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positionLimit int,
	supporterLimit int,
) ([]RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsByIDBoundedWithFence(ctx, tx, teamID, conflictIDs, knownAt, positionLimit, supporterLimit, true)
}

func loadRelationshipConflictRecordsByIDBoundedWithFence(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positionLimit int,
	supporterLimit int,
	activeOnly bool,
) ([]RelationshipConflictCaseRecord, error) {
	conflictIDs = normalizeRecallUUIDList(conflictIDs)
	if len(conflictIDs) == 0 {
		return []RelationshipConflictCaseRecord{}, nil
	}
	cases, err := loadRelationshipConflictCaseRowsWithFence(ctx, tx, teamID, conflictIDs, knownAt, activeOnly)
	if err != nil {
		return nil, err
	}
	positions, err := loadRelationshipConflictPositionRowsWithLimitAndFence(ctx, tx, teamID, conflictIDs, knownAt, positionLimit, activeOnly)
	if err != nil {
		return nil, err
	}
	var supportersErr error
	if activeOnly {
		supportersErr = loadActiveRelationshipConflictSupporters(ctx, tx, teamID, conflictIDs, knownAt, positions, supporterLimit)
	} else {
		supportersErr = loadRelationshipConflictSupporters(ctx, tx, teamID, conflictIDs, knownAt, positions, supporterLimit)
	}
	if supportersErr != nil {
		return nil, supportersErr
	}
	for i := range cases {
		cases[i].Positions = positionsForConflict(cases[i].ConflictID, positions)
		applyConflictPositionKnownAtDispositions(&cases[i], knownAt)
	}
	return cases, nil
}

func loadRelationshipConflictCaseRows(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
) ([]RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictCaseRowsWithFence(ctx, tx, teamID, conflictIDs, knownAt, false)
}

func loadRelationshipConflictCaseRowsWithFence(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	activeOnly bool,
) ([]RelationshipConflictCaseRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, conflict_id::text, COALESCE(space_id::text, ''), semantic_scope_key, kind, status,
		       subject_entity_id::text, predicate_key, predicate_version,
		       relationship_kind, current_cardinality, polarity, COALESCE(scope_key, ''),
		       question, policy_version, review_due_at, next_review_at, review_ttl_days,
		       timezone, COALESCE(preferred_position_id::text, ''),
		       resolved_at, effective_at, effective_time_basis, resolution_reason,
		       version, attempts, created_at, updated_at,
		       (
		           SELECT max(event.created_at)
		           FROM relationship_conflict_events AS event
		           WHERE event.team_id = relationship_conflict_cases.team_id
		             AND event.conflict_id = relationship_conflict_cases.conflict_id
		             AND event.action = 'dismissed'
		       ) AS dismissed_at
		FROM relationship_conflict_cases
		WHERE team_id = ?::uuid
		  AND conflict_id = ANY(?::uuid[])
		  AND (?::timestamptz IS NULL OR created_at <= ?::timestamptz)
		`+activeConflictCaseFence(activeOnly)+`
		ORDER BY created_at, conflict_id
	`, teamID, pq.Array(conflictIDs), knownAt, knownAt).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RelationshipConflictCaseRecord{}
	for rows.Next() {
		var record RelationshipConflictCaseRecord
		if err := rows.Scan(
			&record.TeamID,
			&record.ConflictID,
			&record.SpaceID,
			&record.SemanticScopeKey,
			&record.Kind,
			&record.Status,
			&record.SubjectEntityID,
			&record.PredicateKey,
			&record.PredicateVersion,
			&record.RelationshipKind,
			&record.CurrentCardinality,
			&record.Polarity,
			&record.ScopeKey,
			&record.Question,
			&record.PolicyVersion,
			&record.ReviewDueAt,
			&record.NextReviewAt,
			&record.ReviewTTLDays,
			&record.Timezone,
			&record.PreferredPositionID,
			&record.ResolvedAt,
			&record.EffectiveAt,
			&record.EffectiveTimeBasis,
			&record.ResolutionReason,
			&record.Version,
			&record.Attempts,
			&record.CreatedAt,
			&record.UpdatedAt,
			&record.DismissedAt,
		); err != nil {
			return nil, err
		}
		applyConflictKnownAt(&record, knownAt)
		out = append(out, record)
	}
	return out, rows.Err()
}

func activeConflictCaseFence(activeOnly bool) string {
	if !activeOnly {
		return ""
	}
	return " AND " + activeSemanticSpaceGenerationSQL("relationship_conflict_cases") + "\n"
}
