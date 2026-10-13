package postgres

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
)

func normalizeListHypothesesInput(input ListHypothesesInput) ListHypothesesInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Status = strings.TrimSpace(input.Status)
	input.Cursor = strings.TrimSpace(input.Cursor)
	input.Sort = strings.ToLower(strings.TrimSpace(input.Sort))
	input.Direction = strings.ToLower(strings.TrimSpace(input.Direction))
	if input.Limit <= 0 {
		input.Limit = 20
	}
	if input.Limit > 100 {
		input.Limit = 100
	}
	if input.Sort == "" {
		input.Sort = "updated_at"
	}
	if input.Direction == "" {
		input.Direction = "desc"
	}
	return input
}

func validateListHypothesesInput(input ListHypothesesInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.Status != "" && !domain.DreamStatus(input.Status).IsValid() {
		return fmt.Errorf("unsupported hypothesis status %q", input.Status)
	}
	switch input.Sort {
	case "updated_at", "created_at":
	default:
		return fmt.Errorf("unsupported hypothesis sort %q", input.Sort)
	}
	switch input.Direction {
	case "asc", "desc":
	default:
		return fmt.Errorf("unsupported hypothesis direction %q", input.Direction)
	}
	return nil
}

func hypothesisListOrder(sort, direction string) string {
	column := "updated_at"
	switch sort {
	case "created_at":
		column = "created_at"
	}
	if direction == "asc" {
		return column + " ASC"
	}
	return column + " DESC"
}
