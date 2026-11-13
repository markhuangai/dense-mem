package postgres

import (
	"context"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

func loadTraceEvidenceLifecycleEvents(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	spaceID string,
	fragmentIDs []string,
	limit int,
) ([]TraceEvidenceLifecycleEvent, error) {
	if len(fragmentIDs) == 0 {
		return []TraceEvidenceLifecycleEvent{}, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT lifecycle.lifecycle_event_id::text,
		       lifecycle.lifecycle_operation_id::text,
		       lifecycle.target_fragment_id::text,
		       COALESCE(lifecycle.replacement_fragment_id::text, ''),
		       lifecycle.owner_profile_id::text,
		       lifecycle.action,
		       operation.reason,
		       lifecycle.created_at
		       ,ARRAY(SELECT occurrence.canonical_fragment_id::text FROM remember_item_partitions mapping JOIN evidence_occurrences occurrence ON occurrence.team_id=mapping.team_id AND occurrence.occurrence_id=mapping.occurrence_id AND occurrence.owner_profile_id=mapping.owner_profile_id AND occurrence.space_id=mapping.space_id AND occurrence.space_generation=mapping.space_generation
		         WHERE mapping.team_id=lifecycle.team_id AND mapping.source_item_id=lifecycle.replacement_source_item_id AND mapping.space_id=lifecycle.space_id ORDER BY mapping.partition_index)
		       ,ARRAY(SELECT mapping.occurrence_id::text FROM remember_item_partitions mapping WHERE mapping.team_id=lifecycle.team_id AND mapping.source_item_id=lifecycle.replacement_source_item_id AND mapping.space_id=lifecycle.space_id ORDER BY mapping.partition_index)
		FROM evidence_lifecycle_events AS lifecycle
		JOIN evidence_lifecycle_operations AS operation
		  ON operation.team_id = lifecycle.team_id
		 AND operation.lifecycle_operation_id = lifecycle.lifecycle_operation_id
		 AND operation.space_id = lifecycle.space_id
		 AND `+activeSemanticSpaceGenerationSQL("operation")+`
		WHERE lifecycle.team_id = ?::uuid
		  AND lifecycle.space_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("lifecycle")+`
		  AND (
		      lifecycle.target_fragment_id = ANY(?::uuid[])
		      OR lifecycle.replacement_fragment_id = ANY(?::uuid[])
		      OR EXISTS(SELECT 1 FROM remember_item_partitions mapping JOIN evidence_occurrences occurrence ON occurrence.team_id=mapping.team_id AND occurrence.occurrence_id=mapping.occurrence_id AND occurrence.owner_profile_id=mapping.owner_profile_id AND occurrence.space_id=mapping.space_id AND occurrence.space_generation=mapping.space_generation
		        WHERE mapping.team_id=lifecycle.team_id AND mapping.source_item_id=lifecycle.replacement_source_item_id AND mapping.space_id=lifecycle.space_id AND (occurrence.canonical_fragment_id=ANY(?::uuid[]) OR occurrence.occurrence_id=ANY(?::uuid[])))
		  )
		ORDER BY lifecycle.created_at ASC, lifecycle.lifecycle_event_id ASC
		LIMIT ?
	`, teamID, spaceID, pq.Array(fragmentIDs), pq.Array(fragmentIDs), pq.Array(fragmentIDs), pq.Array(fragmentIDs), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TraceEvidenceLifecycleEvent{}
	for rows.Next() {
		var event TraceEvidenceLifecycleEvent
		if err := rows.Scan(
			&event.LifecycleEventID,
			&event.LifecycleOperationID,
			&event.TargetFragmentID,
			&event.ReplacementFragmentID,
			&event.OwnerProfileID,
			&event.Action,
			&event.Reason,
			&event.CreatedAt,
			pq.Array(&event.ReplacementEvidenceIDs), pq.Array(&event.ReplacementOccurrenceIDs),
		); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}
