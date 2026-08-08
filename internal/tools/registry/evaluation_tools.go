//go:build evaluation

package registry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/requestctx"
	appservice "github.com/markhuangai/dense-mem/internal/service"
)

const (
	defaultEvaluationPageSize = 100
	maxEvaluationPageSize     = 500
)

func evalListKnowledgeRefsTool(deps Dependencies) Tool {
	return Tool{
		Name:        "eval_list_knowledge_refs",
		Description: "Page through authenticated-team knowledge references for evaluation. Content is included unless metadata_only=true.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"type"},
			"properties": map[string]any{
				"type":          schemaEnum(evalListKnowledgeRefTypes(deps)),
				"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": maxEvaluationPageSize},
				"cursor":        schemaString("Opaque cursor from a previous response.", 512),
				"status":        schemaString("Optional lifecycle status filter.", 64),
				"metadata_only": map[string]any{"type": "boolean"},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			kind := strings.ToLower(stringInput(input["type"]))
			limit := evalLimit(input)
			metadataOnly, _ := input["metadata_only"].(bool)
			if err := auditEvaluationTool(ctx, deps, "eval_list_knowledge_refs", limit, !metadataOnly, map[string]any{"type": kind, "status": input["status"]}); err != nil {
				return nil, err
			}
			switch kind {
			case "dream":
				return evalListDreams(ctx, deps, profileID, input, limit, metadataOnly)
			case "evidence", "relationship", "entity", "value", "hypothesis":
				return evalListKnowledgeRefs(ctx, deps, profileID, input, limit, metadataOnly)
			default:
				return nil, fmt.Errorf("eval_list_knowledge_refs: unsupported type %q", kind)
			}
		},
	}
}

func auditEvaluationTool(ctx context.Context, deps Dependencies, tool string, pageSize int, contentReturned bool, filters map[string]any) error {
	if deps.EvaluationAudit == nil {
		return ErrToolUnavailable
	}
	var profileID *string
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		teamID := actor.TeamID.String()
		profileID = &teamID
	}
	var keyID *string
	actorRole := "system"
	if credential, ok := requestctx.ActorCredentialFromContext(ctx); ok {
		if credential.KeyID != uuid.Nil {
			id := credential.KeyID.String()
			keyID = &id
		}
		if credential.Role != "" {
			actorRole = credential.Role
		}
	}
	return deps.EvaluationAudit.Append(ctx, appservice.AuditLogEntry{
		ProfileID:  profileID,
		Operation:  "EVALUATION_TOOL_CALL",
		EntityType: "evaluation_tool",
		EntityID:   tool,
		ActorKeyID: keyID,
		ActorRole:  actorRole,
		Metadata: map[string]any{
			"tool":             tool,
			"page_size":        pageSize,
			"content_returned": contentReturned,
			"filters":          filters,
		},
	})
}

func evalLimit(input map[string]any) int {
	limit := defaultEvaluationPageSize
	if requested, ok := intInput(input["limit"]); ok && requested > 0 {
		limit = requested
	}
	if limit > maxEvaluationPageSize {
		return maxEvaluationPageSize
	}
	return limit
}

func evalPage(items []map[string]any, nextCursor string) map[string]any {
	return map[string]any{
		"items":       items,
		"next_cursor": nextCursor,
		"has_more":    nextCursor != "",
	}
}

func stripEvalContent(kind string, item map[string]any) {
	switch kind {
	case "dream":
		delete(item, "hypothesis")
		delete(item, "what_if")
		delete(item, "possible_outcome")
		delete(item, "rationale")
	case "evidence":
		delete(item, "content")
	case "entity":
		delete(item, "canonical_name")
		delete(item, "identity_context")
	case "value":
		delete(item, "canonical_value")
		delete(item, "display")
	case "hypothesis":
		delete(item, "payload")
	}
}

func optionalTime(value any) (*time.Time, error) {
	raw := stringInput(value)
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("time value must be RFC3339")
	}
	return &parsed, nil
}

func intInputOrDefault(value any, fallback int) int {
	if parsed, ok := intInput(value); ok && parsed > 0 {
		return parsed
	}
	return fallback
}

func boolInputOrDefault(value any, fallback bool) bool {
	parsed, ok := value.(bool)
	if !ok {
		return fallback
	}
	return parsed
}
