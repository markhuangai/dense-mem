package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"

	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
)

type ConflictRuntimeConfig = conflictcontract.ConflictRuntimeConfig

type ConflictReviewRunInput = conflictcontract.ConflictReviewRunInput

type ConflictReviewRunCompleteInput = conflictcontract.ConflictReviewRunCompleteInput

type ConflictReviewRunRecord = conflictcontract.ConflictReviewRunRecord

type ClaimRelationshipConflictCasesInput = conflictcontract.ClaimRelationshipConflictCasesInput

// ReleaseRelationshipConflictCaseClaimInput identifies the worker claim that
// should be returned to the conflict queue after retryable processing fails.
type ReleaseRelationshipConflictCaseClaimInput = conflictcontract.ReleaseRelationshipConflictCaseClaimInput

type ReviewRelationshipConflictCaseInput = conflictcontract.ReviewRelationshipConflictCaseInput

type ReviewRelationshipConflictCaseResult = conflictcontract.ReviewRelationshipConflictCaseResult

// RelationshipConflictResolutionInput identifies one selected resolution. The
// repository revalidates every field before it mutates durable state.
type RelationshipConflictResolutionInput = conflictcontract.RelationshipConflictResolutionInput

type RelationshipConflictResolutionDocument = conflictcontract.RelationshipConflictResolutionDocument

type RelationshipConflictResolutionFence = conflictcontract.RelationshipConflictResolutionFence

type RelationshipConflictResolutionPlan = conflictcontract.RelationshipConflictResolutionPlan

type RelationshipConflictResolutionEmbedding = conflictcontract.RelationshipConflictResolutionEmbedding

type CommitRelationshipConflictResolutionInput = conflictcontract.CommitRelationshipConflictResolutionInput

type RelationshipConflictCaseRecord = conflictcontract.RelationshipConflictCaseRecord
type RelationshipConflictPositionRecord = conflictcontract.RelationshipConflictPositionRecord
type RelationshipConflictSupporterRecord = conflictcontract.RelationshipConflictSupporterRecord

type ValidateRelationshipConflictContextInput = conflictcontract.ValidateRelationshipConflictContextInput
type ReserveOverdueConflictAssessmentInput = conflictcontract.ReserveOverdueConflictAssessmentInput
type OverdueConflictAssessmentReservation = conflictcontract.OverdueConflictAssessmentReservation
type OverdueConflictAssessmentDossier = conflictcontract.OverdueConflictAssessmentDossier
type OverdueConflictAssessmentPosition = conflictcontract.OverdueConflictAssessmentPosition
type OverdueConflictAssessmentEvidence = conflictcontract.OverdueConflictAssessmentEvidence
type CompleteOverdueConflictAssessmentInput = conflictcontract.CompleteOverdueConflictAssessmentInput
type CompleteOverdueConflictAssessmentResult = conflictcontract.CompleteOverdueConflictAssessmentResult
type ApplyOverdueConflictResolutionInput = conflictcontract.ApplyOverdueConflictResolutionInput
type ApplyOverdueConflictResolutionResult = conflictcontract.ApplyOverdueConflictResolutionResult
type ResumePendingOverdueConflictResolutionInput = conflictcontract.ResumePendingOverdueConflictResolutionInput
type ConflictDerivedEvidenceTarget = conflictcontract.ConflictDerivedEvidenceTarget
type ClaimConflictDerivedEvidenceTasksInput = conflictcontract.ClaimConflictDerivedEvidenceTasksInput
type StageConflictDerivedEvidenceResult = conflictcontract.StageConflictDerivedEvidenceResult

func ApplyConflictKnownAt(record *RelationshipConflictCaseRecord, knownAt *time.Time) {
	applyConflictKnownAt(record, knownAt)
}

func ApplyConflictPositionKnownAtDispositions(record *RelationshipConflictCaseRecord, knownAt *time.Time) {
	applyConflictPositionKnownAtDispositions(record, knownAt)
}

func loadRelationshipConflictRecords(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipIDs []string,
	knownAt *time.Time,
) ([]RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsInSpace(ctx, tx, teamID, relationshipIDs, knownAt, "")
}

func loadRelationshipConflictRecordsInSpace(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipIDs []string,
	knownAt *time.Time,
	spaceID string,
) ([]RelationshipConflictCaseRecord, error) {
	relationshipIDs = normalizeRecallUUIDList(relationshipIDs)
	if len(relationshipIDs) == 0 {
		return []RelationshipConflictCaseRecord{}, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT DISTINCT conflict.conflict_id::text
		FROM relationship_conflict_cases AS conflict
		JOIN relationship_conflict_position_members AS member
		  ON member.team_id = conflict.team_id
		 AND member.conflict_id = conflict.conflict_id
		WHERE conflict.team_id = ?::uuid
		  AND member.relationship_id = ANY(?::uuid[])
		  AND (
		      ? = ''
		      OR (
		          conflict.space_id = NULLIF(?, '')::uuid
		          AND member.space_id = NULLIF(?, '')::uuid
		      )
		  )
		  AND (?::timestamptz IS NULL OR conflict.created_at <= ?::timestamptz)
		  AND (
		      (?::timestamptz IS NULL AND member.active)
		      OR (
		          ?::timestamptz IS NOT NULL
		          AND member.first_seen_at <= ?::timestamptz
		          AND (member.retired_at IS NULL OR member.retired_at > ?::timestamptz)
		      )
		  )
		  AND (
		      (?::timestamptz IS NULL AND conflict.status IN ('open', 'overdue', 'resolved'))
		      OR (?::timestamptz IS NOT NULL AND conflict.status IN ('open', 'overdue', 'resolved', 'dismissed'))
		  )
		ORDER BY conflict.conflict_id::text
	`, teamID, pq.Array(relationshipIDs), spaceID, spaceID, spaceID,
		knownAt, knownAt, knownAt, knownAt, knownAt, knownAt, knownAt, knownAt).Rows()
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
	return loadRelationshipConflictRecordsByID(ctx, tx, teamID, conflictIDs, knownAt)
}

func loadRelationshipConflictRecordsByID(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
) ([]RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsByIDBounded(ctx, tx, teamID, conflictIDs, knownAt, 0, relationshipConflictSupporterLimit)
}

func loadRelationshipConflictRecordsByIDBounded(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positionLimit int,
	supporterLimit int,
) ([]RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsByIDBoundedWithFence(ctx, tx, teamID, conflictIDs, knownAt, positionLimit, supporterLimit, false)
}

func loadRelationshipConflictPositionRows(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
) ([]RelationshipConflictPositionRecord, error) {
	return loadRelationshipConflictPositionRowsWithLimit(ctx, tx, teamID, conflictIDs, knownAt, 0)
}

func loadRelationshipConflictPositionRowsWithLimit(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positionLimit int,
) ([]RelationshipConflictPositionRecord, error) {
	return loadRelationshipConflictPositionRowsWithLimitAndFence(ctx, tx, teamID, conflictIDs, knownAt, positionLimit, false)
}

func loadRelationshipConflictPositionRowsWithLimitAndFence(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positionLimit int,
	activeOnly bool,
) ([]RelationshipConflictPositionRecord, error) {
	dispositionSelect := "position.disposition"
	dispositionGroup := ", position.disposition"
	if knownAt != nil {
		dispositionSelect = "'candidate'"
		dispositionGroup = ""
	}
	activeMemberFence := ""
	activePositionFence := ""
	if activeOnly {
		activeMemberFence = "\n\t\t\t AND " + activeSemanticSpaceGenerationSQL("member")
		activePositionFence = "\n\t\t\t  AND " + activeSemanticSpaceGenerationSQL("position")
	}
	rows, err := tx.WithContext(ctx).Raw(fmt.Sprintf(`
		WITH grouped AS (
			SELECT position.conflict_id::text AS conflict_id,
			       position.position_id::text AS position_id,
			       position.position_key,
			       COALESCE(position.object_entity_id::text, '') AS object_entity_id,
			       COALESCE(position.object_value_id::text, '') AS object_value_id,
			       %s AS disposition,
			       COALESCE(array_remove(array_agg(DISTINCT member.relationship_id::text ORDER BY member.relationship_id::text), NULL), ARRAY[]::text[]) AS relationship_ids,
			       COALESCE(array_remove(array_agg(DISTINCT member.owner_profile_id::text ORDER BY member.owner_profile_id::text), NULL), ARRAY[]::text[]) AS owner_profile_ids,
			       COALESCE(array_remove(array_agg(DISTINCT member.fragment_id::text ORDER BY member.fragment_id::text), NULL), ARRAY[]::text[]) AS evidence_ids,
			       max(member.effective_at) AS effective_at,
			       max(member.effective_time_basis) AS effective_time_basis,
			       COALESCE(bool_and(member.recorded_fallback), false) AS recorded_fallback
			FROM relationship_conflict_positions AS position
			LEFT JOIN relationship_conflict_position_members AS member
			 ON member.team_id = position.team_id
			 AND member.position_id = position.position_id
				 AND (
				     (?::timestamptz IS NULL AND member.active)
				     OR (
			         ?::timestamptz IS NOT NULL
			         AND member.first_seen_at <= ?::timestamptz
				         AND (member.retired_at IS NULL OR member.retired_at > ?::timestamptz)
				     )
				 )
				 %s
			WHERE position.team_id = ?::uuid
			  AND position.conflict_id = ANY(?::uuid[])
			  AND (
			      (?::timestamptz IS NULL AND position.active)
			      OR (
			          ?::timestamptz IS NOT NULL
			          AND position.first_seen_at <= ?::timestamptz
			          AND (position.retired_at IS NULL OR position.retired_at > ?::timestamptz)
			      )
			  )
			  %s
			GROUP BY position.conflict_id, position.position_id, position.position_key,
			         position.object_entity_id, position.object_value_id%s
		), ranked AS (
			SELECT grouped.*,
			       COUNT(*) OVER (PARTITION BY conflict_id)::int AS position_count,
			       row_number() OVER (PARTITION BY conflict_id ORDER BY position_key, position_id) AS position_rank
			FROM grouped
		)
		SELECT conflict_id, position_id, position_key, object_entity_id, object_value_id,
		       disposition,
		       relationship_ids, owner_profile_ids, evidence_ids, effective_at,
		       effective_time_basis, recorded_fallback, position_count
		FROM ranked
		WHERE ?::int <= 0 OR position_rank <= ?::int
		ORDER BY conflict_id, position_key, position_id
		`, dispositionSelect, activeMemberFence, activePositionFence, dispositionGroup),
		knownAt, knownAt, knownAt, knownAt,
		teamID, pq.Array(conflictIDs),
		knownAt, knownAt, knownAt, knownAt,
		positionLimit, positionLimit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RelationshipConflictPositionRecord{}
	for rows.Next() {
		var relationshipIDs, ownerProfileIDs, evidenceIDs pq.StringArray
		var record RelationshipConflictPositionRecord
		if err := rows.Scan(
			&record.ConflictID,
			&record.PositionID,
			&record.PositionKey,
			&record.ObjectEntityID,
			&record.ObjectValueID,
			&record.Disposition,
			&relationshipIDs,
			&ownerProfileIDs,
			&evidenceIDs,
			&record.EffectiveAt,
			&record.EffectiveTimeBasis,
			&record.RecordedFallback,
			&record.PositionCount,
		); err != nil {
			return nil, err
		}
		record.RelationshipIDs = []string(relationshipIDs)
		record.OwnerProfileIDs = []string(ownerProfileIDs)
		record.EvidenceIDs = []string(evidenceIDs)
		record.PositionsTruncated = positionLimit > 0 && record.PositionCount > positionLimit
		out = append(out, record)
	}
	return out, rows.Err()
}

func positionsForConflict(
	conflictID string,
	positions []RelationshipConflictPositionRecord,
) []RelationshipConflictPositionRecord {
	out := []RelationshipConflictPositionRecord{}
	for _, position := range positions {
		if position.ConflictID == conflictID {
			out = append(out, position)
		}
	}
	return out
}

func applyConflictKnownAt(record *RelationshipConflictCaseRecord, knownAt *time.Time) {
	if record == nil || knownAt == nil {
		return
	}
	rewound := false
	if record.ResolvedAt != nil && record.ResolvedAt.After(*knownAt) {
		if knownAt.Before(record.ReviewDueAt) {
			record.Status = string(domain.RelationshipConflictOpen)
		} else {
			record.Status = string(domain.RelationshipConflictOverdue)
		}
		record.PreferredPositionID = ""
		record.ResolvedAt = nil
		record.EffectiveAt = nil
		record.EffectiveTimeBasis = ""
		record.ResolutionReason = ""
		record.DismissedAt = nil
		rewound = true
	}
	if record.Status == string(domain.RelationshipConflictOverdue) && knownAt.Before(record.ReviewDueAt) {
		record.Status = string(domain.RelationshipConflictOpen)
		rewound = true
	}
	if record.Status == string(domain.RelationshipConflictDismissed) && conflictDismissedAfterKnownAt(record, knownAt) {
		record.DismissedAt = nil
		if record.ResolvedAt != nil && !record.ResolvedAt.After(*knownAt) {
			record.Status = string(domain.RelationshipConflictResolved)
			rewound = true
			applyConflictKnownAtNextReview(record, knownAt, rewound)
			return
		}
		if knownAt.Before(record.ReviewDueAt) {
			record.Status = string(domain.RelationshipConflictOpen)
		} else {
			record.Status = string(domain.RelationshipConflictOverdue)
		}
		record.PreferredPositionID = ""
		record.ResolvedAt = nil
		record.EffectiveAt = nil
		record.EffectiveTimeBasis = ""
		record.ResolutionReason = ""
		rewound = true
	}
	applyConflictKnownAtNextReview(record, knownAt, rewound)
}

func applyConflictKnownAtNextReview(record *RelationshipConflictCaseRecord, knownAt *time.Time, rewound bool) {
	if record == nil || knownAt == nil || !rewound {
		return
	}
	record.NextReviewAt = time.Time{}
}

func applyConflictPositionKnownAtDispositions(record *RelationshipConflictCaseRecord, knownAt *time.Time) {
	if record == nil || knownAt == nil {
		return
	}
	switch record.Status {
	case string(domain.RelationshipConflictResolved):
		for i := range record.Positions {
			if record.Positions[i].PositionID == record.PreferredPositionID {
				record.Positions[i].Disposition = string(domain.RelationshipConflictPositionPreferred)
			} else {
				record.Positions[i].Disposition = string(domain.RelationshipConflictPositionSuppressedCurrent)
			}
		}
	case string(domain.RelationshipConflictOpen), string(domain.RelationshipConflictOverdue):
		for i := range record.Positions {
			record.Positions[i].Disposition = string(domain.RelationshipConflictPositionCandidate)
		}
	}
}

func conflictDismissedAfterKnownAt(record *RelationshipConflictCaseRecord, knownAt *time.Time) bool {
	if record == nil || knownAt == nil {
		return false
	}
	if record.DismissedAt != nil {
		return record.DismissedAt.After(*knownAt)
	}
	return record.UpdatedAt.After(*knownAt)
}
