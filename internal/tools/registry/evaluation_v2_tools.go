package registry

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func evalListV2KnowledgeRefs(ctx context.Context, deps Dependencies, profileID string, input map[string]any, limit int, metadataOnly bool) (map[string]any, error) {
	if deps.V2Evaluation == nil {
		return nil, ErrToolUnavailable
	}
	teamID, err := evalActorTeamID(ctx, profileID)
	if err != nil {
		return nil, err
	}
	kind := strings.ToLower(stringInput(input["type"]))
	page, err := deps.V2Evaluation.ListV2EvaluationRefs(ctx, repository.V2EvaluationListInput{
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

func evalGetV2KnowledgeItem(ctx context.Context, deps Dependencies, profileID, kind, id string) (map[string]any, error) {
	if deps.V2Evaluation == nil {
		return nil, ErrToolUnavailable
	}
	teamID, err := evalActorTeamID(ctx, profileID)
	if err != nil {
		return nil, err
	}
	return deps.V2Evaluation.GetV2EvaluationItem(ctx, repository.V2EvaluationGetInput{
		TeamID: teamID,
		Type:   kind,
		ID:     id,
	})
}

func evalActorTeamID(ctx context.Context, profileID string) (string, error) {
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		return actor.TeamID.String(), nil
	}
	parsed, err := uuid.Parse(profileID)
	if err != nil {
		return "", errors.New("evaluation tool requires authenticated team context")
	}
	return parsed.String(), nil
}

func evalListKnowledgeRefTypes() []string {
	return []string{
		"fragment",
		"claim",
		"fact",
		"community",
		"dream",
		"edge",
		"evidence",
		"relationship",
		"entity",
		"value",
		"hypothesis",
	}
}

func evalGetKnowledgeItemTypes() []string {
	return []string{
		"fragment",
		"claim",
		"fact",
		"community",
		"dream",
		"evidence",
		"relationship",
		"entity",
		"value",
		"hypothesis",
	}
}

func evalScoredKnowledgeRefTypes() []string {
	return []string{
		"fragment",
		"claim",
		"fact",
		"community",
		"dream",
		"evidence",
		"relationship",
		"entity",
		"value",
		"hypothesis",
	}
}

func copyEvalItem(item map[string]any) map[string]any {
	copied := make(map[string]any, len(item))
	for key, value := range item {
		copied[key] = value
	}
	return copied
}
