package repository

import (
	"context"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

func loadTraceEvidenceLifecycleEvents(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	fragmentIDs []string,
	limit int,
) ([]TraceEvidenceLifecycleEvent, error) {
	if len(fragmentIDs) == 0 {
		return nil, nil
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
		FROM evidence_lifecycle_events AS lifecycle
		JOIN evidence_lifecycle_operations AS operation
		  ON operation.team_id = lifecycle.team_id
		 AND operation.lifecycle_operation_id = lifecycle.lifecycle_operation_id
		WHERE lifecycle.team_id = ?::uuid
		  AND (
		      lifecycle.target_fragment_id = ANY(?::uuid[])
		      OR lifecycle.replacement_fragment_id = ANY(?::uuid[])
		  )
		ORDER BY lifecycle.created_at ASC, lifecycle.lifecycle_event_id ASC
		LIMIT ?
	`, teamID, pq.Array(fragmentIDs), pq.Array(fragmentIDs), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceEvidenceLifecycleEvent
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
		); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}
