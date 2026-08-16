//go:build evaluation

package registry

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func evalListKnowledgeRefs(ctx context.Context, deps Dependencies, fallbackTeamID string, input map[string]any, limit int, metadataOnly bool) (map[string]any, error) {
	if err := requireEvaluationKnowledgeTypesVisible(deps); err != nil {
		return nil, err
	}
	teamID, err := evalActorTeamID(ctx, fallbackTeamID)
	if err != nil {
		return nil, err
	}
	kind := strings.ToLower(stringInput(input["type"]))
	page, err := deps.Evaluation.ListEvaluationRefs(ctx, repository.EvaluationListInput{
		TeamID: teamID,
		Type:   kind,
		Limit:  limit,
		Cursor: stringInput(input["cursor"]),
		Status: stringInput(input["status"]),
	})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		copied := copyEvalItem(item)
		if metadataOnly {
			stripEvalContent(kind, copied)
		}
		items = append(items, copied)
	}
	return map[string]any{
		"items":       items,
		"next_cursor": page.NextCursor,
		"has_more":    page.HasMore,
	}, nil
}

func evalActorTeamID(ctx context.Context, fallbackTeamID string) (string, error) {
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		return actor.TeamID.String(), nil
	}
	parsed, err := uuid.Parse(fallbackTeamID)
	if err != nil {
		return "", errors.New("evaluation tool requires authenticated team context")
	}
	return parsed.String(), nil
}

func evalListKnowledgeRefTypes(deps Dependencies) []string {
	types := make([]string, 0, 6)
	if deps.Dreams != nil {
		types = append(types, "dream")
	}
	if evaluationKnowledgeTypesVisible(deps) {
		types = append(types,
			"evidence",
			"relationship",
			"entity",
			"value",
			"hypothesis",
		)
	}
	return types
}

func evaluationKnowledgeTypesVisible(deps Dependencies) bool {
	return deps.Evaluation != nil
}

func requireEvaluationKnowledgeTypesVisible(deps Dependencies) error {
	if deps.Evaluation == nil {
		return ErrToolUnavailable
	}
	return nil
}

func copyEvalItem(item map[string]any) map[string]any {
	copied := make(map[string]any, len(item))
	for key, value := range item {
		copied[key] = value
	}
	return copied
}
