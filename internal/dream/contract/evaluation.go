package contract

import "context"

type EvaluationListInput struct {
	TeamID string
	Type   string
	Limit  int
	Cursor string
	Status string
}

type EvaluationGetInput struct {
	TeamID string
	Type   string
	ID     string
}

type EvaluationPage struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

type EvaluationRepository interface {
	ListEvaluationRefs(context.Context, EvaluationListInput) (*EvaluationPage, error)
	GetEvaluationItem(context.Context, EvaluationGetInput) (map[string]any, error)
}
