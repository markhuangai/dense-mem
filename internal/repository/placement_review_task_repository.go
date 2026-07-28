package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// hydratePlacementItemReviewTasks exposes only safe semantic-review fields,
// including migrated tasks that predate V2.4 assessments.
func hydratePlacementItemReviewTasks(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerProfileID, ingestID string,
	items []PlacementItem,
) error {
	if len(items) == 0 {
		return nil
	}
	byID := make(map[string]*PlacementItem, len(items))
	for index := range items {
		items[index].ReviewTasks = []PlacementReviewTask{}
		byID[items[index].PlacementItemID] = &items[index]
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT task.placement_item_id::text, task.review_task_id::text, task.version, task.status,
		       COALESCE(NULLIF(task.payload->>'semantic_kind', ''),
		           CASE task.task_type
		               WHEN 'identity_needs_review' THEN 'identity'
		               WHEN 'predicate_needs_review' THEN 'predicate'
		               ELSE 'support_confidence'
		           END),
		       COALESCE(task.payload->>'question', ''),
		       COALESCE(task.payload->'options', '[]'::jsonb),
		       COALESCE(task.payload->>'guidance', ''), task.expires_at
		FROM review_tasks AS task
		WHERE task.team_id = ?::uuid
		  AND task.owner_profile_id = ?::uuid
		  AND task.ingest_id = ?::uuid
		  AND task.placement_item_id IS NOT NULL
		  AND (
		      jsonb_exists(task.payload, 'semantic_kind')
		      OR (task.task_type = 'identity_needs_review' AND task.reason = 'ambiguous_entity')
		      OR (task.task_type = 'predicate_needs_review' AND task.reason = 'unknown_predicate')
		  )
		ORDER BY task.placement_item_id, task.created_at ASC, task.review_task_id ASC
	`, teamID, ownerProfileID, ingestID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID string
		var task PlacementReviewTask
		var optionsRaw []byte
		var expiresAt sql.NullTime
		if err := rows.Scan(
			&itemID,
			&task.ReviewTaskID,
			&task.Version,
			&task.Status,
			&task.Kind,
			&task.Question,
			&optionsRaw,
			&task.Guidance,
			&expiresAt,
		); err != nil {
			return err
		}
		item := byID[itemID]
		if item == nil {
			continue
		}
		if err := json.Unmarshal(optionsRaw, &task.Options); err != nil {
			return fmt.Errorf("placement review task options: %w", err)
		}
		if task.Options == nil {
			task.Options = []map[string]any{}
		}
		if expiresAt.Valid {
			value := expiresAt.Time
			task.ExpiresAt = &value
		}
		task.Kind = strings.TrimSpace(task.Kind)
		task.Question = strings.TrimSpace(task.Question)
		task.Guidance = strings.TrimSpace(task.Guidance)
		item.ReviewTasks = append(item.ReviewTasks, task)
	}
	return rows.Err()
}
