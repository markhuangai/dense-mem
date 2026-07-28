package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// hydratePlacementItemReviewTasks exposes only the safe V2.4 semantic-review
// fields; raw assessments, rationales, and resolution data remain internal.
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
		SELECT placement_item_id::text, review_task_id::text, version, status,
		       COALESCE(payload->>'semantic_kind', ''),
		       COALESCE(payload->>'question', ''),
		       COALESCE(payload->'options', '[]'::jsonb),
		       COALESCE(payload->>'guidance', ''), expires_at
		FROM review_tasks
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
		  AND placement_item_id IS NOT NULL
		  AND assessment_id IS NOT NULL
		ORDER BY placement_item_id, created_at ASC, review_task_id ASC
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
