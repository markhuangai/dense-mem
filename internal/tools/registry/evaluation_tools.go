package registry

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	appservice "github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/recallquality"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

const (
	defaultEvalK = 10
	maxEvalK     = 50
)

func evalGetManifestTool(deps Dependencies) Tool {
	return Tool{
		Name:           "eval_get_manifest",
		Description:    "Evaluation mode manifest for the authenticated team, including runtime limits and available evaluation capabilities.",
		InputSchema:    map[string]any{"type": "object", "additionalProperties": false},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			runtime := domain.EvaluationRuntimeConfig{Enabled: true, ExportMaxPageSize: appservice.DefaultEvaluationExportMaxPageSize}
			if deps.EvaluationConfig != nil {
				cfg, err := deps.EvaluationConfig.EvaluationRuntimeConfig(ctx)
				if err != nil {
					return nil, fmt.Errorf("eval_get_manifest: runtime config unavailable: %w", err)
				}
				runtime = cfg
			}
			if err := auditEvaluationTool(ctx, deps, "eval_get_manifest", 0, false, nil); err != nil {
				return nil, err
			}
			actor, _ := requestctx.ActorProfileFromContext(ctx)
			return map[string]any{
				"evaluation_mode": runtime.Enabled,
				"export_limits": map[string]any{
					"max_page_size": runtime.ExportMaxPageSize,
				},
				"team": map[string]any{
					"id":   firstNonEmpty(actor.TeamID.String(), profileID),
					"name": actor.TeamName,
				},
				"capabilities": []string{
					"knowledge_refs",
					"knowledge_item",
					"recall_feedback_events",
					"dream_cycle",
					"recall_case",
					"retrieval_scoring",
				},
			}, nil
		},
	}
}

func evalListKnowledgeRefsTool(deps Dependencies) Tool {
	return Tool{
		Name:        "eval_list_knowledge_refs",
		Description: "Page through team-scoped facts, claims, fragments, communities, dreams, or graph edges for evaluation. Content is included unless metadata_only=true.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"type"},
			"properties": map[string]any{
				"type":          schemaEnum([]string{"fragment", "claim", "fact", "community", "dream", "edge"}),
				"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
				"cursor":        schemaString("Opaque cursor from a previous response.", 512),
				"status":        schemaString("Optional lifecycle status filter.", 64),
				"metadata_only": map[string]any{"type": "boolean"},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			kind, _ := input["type"].(string)
			limit := evalLimit(ctx, deps, input)
			metadataOnly, _ := input["metadata_only"].(bool)
			if err := auditEvaluationTool(ctx, deps, "eval_list_knowledge_refs", limit, !metadataOnly, map[string]any{"type": kind, "status": input["status"]}); err != nil {
				return nil, err
			}
			switch kind {
			case "fragment":
				return evalListFragments(ctx, deps, profileID, input, limit, metadataOnly)
			case "claim":
				return evalListClaims(ctx, deps, profileID, input, limit, metadataOnly)
			case "fact":
				return evalListFacts(ctx, deps, profileID, input, limit, metadataOnly)
			case "community":
				return evalListCommunities(ctx, deps, profileID, limit, metadataOnly)
			case "dream":
				return evalListDreams(ctx, deps, profileID, input, limit, metadataOnly)
			case "edge":
				return evalListEdges(ctx, deps, profileID, limit)
			default:
				return nil, fmt.Errorf("eval_list_knowledge_refs: unsupported type %q", kind)
			}
		},
	}
}

func evalGetKnowledgeItemTool(deps Dependencies) Tool {
	return Tool{
		Name:        "eval_get_knowledge_item",
		Description: "Fetch one team-scoped fact, claim, fragment, community, or dream with its evaluation metadata.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"type", "id"},
			"properties": map[string]any{
				"type":          schemaEnum([]string{"fragment", "claim", "fact", "community", "dream"}),
				"id":            schemaString("Knowledge item ID.", 256),
				"metadata_only": map[string]any{"type": "boolean"},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			kind, _ := input["type"].(string)
			id, _ := input["id"].(string)
			metadataOnly, _ := input["metadata_only"].(bool)
			if err := auditEvaluationTool(ctx, deps, "eval_get_knowledge_item", 1, !metadataOnly, map[string]any{"type": kind}); err != nil {
				return nil, err
			}
			item, err := evalGetKnowledgeItem(ctx, deps, profileID, kind, id)
			if err != nil {
				return nil, err
			}
			if metadataOnly {
				stripEvalContent(kind, item)
			}
			return map[string]any{"item": item}, nil
		},
	}
}

func evalListRecallFeedbackEventsTool(deps Dependencies) Tool {
	return Tool{
		Name:        EvalListRecallFeedbackEventsToolName,
		Description: "Export team-scoped recall feedback events for diagnostics and hard-case mining.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
				"offset":          map[string]any{"type": "integer", "minimum": 0},
				"quality":         schemaEnum([]string{"high", "medium", "low"}),
				"include_pending": map[string]any{"type": "boolean"},
				"missing_context": map[string]any{"type": "boolean"},
				"irrelevant":      map[string]any{"type": "boolean"},
				"from":            map[string]any{"type": "string", "format": "date-time"},
				"to":              map[string]any{"type": "string", "format": "date-time"},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{appservice.APIKeyScopeRead, appservice.APIKeyScopeFeedbackRead},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.RecallFeedbackEvents == nil {
				return nil, ErrToolUnavailable
			}
			reader, ok := deps.RecallFeedbackEvents.(interface {
				ListRecallFeedbackEvents(context.Context, domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error)
			})
			if !ok {
				return nil, ErrToolUnavailable
			}
			limit := evalLimit(ctx, deps, input)
			if err := auditEvaluationTool(ctx, deps, EvalListRecallFeedbackEventsToolName, limit, false, nil); err != nil {
				return nil, err
			}
			filter, err := evalRecallFeedbackFilter(ctx, profileID, input, limit)
			if err != nil {
				return nil, err
			}
			page, err := reader.ListRecallFeedbackEvents(ctx, filter)
			if err != nil {
				return nil, err
			}
			out, err := structToMap(page)
			if err != nil {
				return nil, err
			}
			return out, nil
		},
	}
}

func evalGetRecallFeedbackEventTool(deps Dependencies) Tool {
	return Tool{
		Name:        EvalGetRecallFeedbackEventToolName,
		Description: "Fetch one team-scoped recall feedback event with resolved current graph state.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"recall_id"},
			"properties": map[string]any{
				"recall_id": schemaString("Recall feedback event ID.", 256),
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{appservice.APIKeyScopeRead, appservice.APIKeyScopeFeedbackRead},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.RecallFeedbackEvents == nil {
				return nil, ErrToolUnavailable
			}
			reader, ok := deps.RecallFeedbackEvents.(interface {
				GetRecallFeedbackEvent(context.Context, string) (*domain.RecallFeedbackEvent, error)
			})
			if !ok {
				return nil, ErrToolUnavailable
			}
			if err := auditEvaluationTool(ctx, deps, EvalGetRecallFeedbackEventToolName, 1, false, nil); err != nil {
				return nil, err
			}
			recallID, _ := input["recall_id"].(string)
			event, err := reader.GetRecallFeedbackEvent(ctx, recallID)
			if err != nil {
				return nil, err
			}
			if !evalEventInScope(ctx, profileID, event) {
				return nil, errors.New("recall feedback event not found")
			}
			out, err := structToMap(event)
			if err != nil {
				return nil, err
			}
			return map[string]any{"event": out}, nil
		},
	}
}

func evalRunRecallCaseTool(deps Dependencies) Tool {
	return Tool{
		Name:        "eval_run_recall_case",
		Description: "Run one recall/context evaluation case through the current Dense-Mem logic and return ranked refs plus context refs.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"case_id", "query"},
			"properties": map[string]any{
				"case_id":           schemaString("Evaluation case ID.", 256),
				"query":             schemaString("Recall query.", 512),
				"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
				"valid_at":          map[string]any{"type": "string", "format": "date-time"},
				"known_at":          map[string]any{"type": "string", "format": "date-time"},
				"include_evidence":  map[string]any{"type": "boolean"},
				"use_communities":   map[string]any{"type": "boolean"},
				"include_dreams":    map[string]any{"type": "boolean"},
				"max_context_chars": map[string]any{"type": "integer", "minimum": 1, "maximum": 20000},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Recall == nil {
				return nil, ErrToolUnavailable
			}
			limit := intInputOrDefault(input["limit"], recallservice.DefaultLimit)
			if err := auditEvaluationTool(ctx, deps, "eval_run_recall_case", limit, false, map[string]any{"case_id": input["case_id"]}); err != nil {
				return nil, err
			}
			req, err := evalRecallRequest(input)
			if err != nil {
				return nil, err
			}
			started := time.Now()
			hits, err := deps.Recall.Recall(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			ranked := make([]map[string]any, 0, len(hits))
			for i, hit := range hits {
				ranked = append(ranked, recallHitRef(i+1, hit))
			}
			out := map[string]any{
				"case_id":     input["case_id"],
				"query":       req.Query,
				"ranked_refs": ranked,
				"latency_ms":  time.Since(started).Milliseconds(),
			}
			if deps.Context != nil {
				assembled, err := deps.Context.Assemble(ctx, profileID, evalAssembleRequest(input, req))
				if err != nil {
					return nil, err
				}
				out["context_refs"] = contextItemRefs(assembled.Items)
				out["context_evidence_refs"] = contextEvidenceRefs(assembled.Items)
				out["context_block_chars"] = len(assembled.ContextBlock)
			}
			if boolInput(input["include_dreams"]) && deps.Dreams != nil {
				dreams, err := deps.Dreams.Recall(ctx, profileID, req.Query, limit)
				if err != nil {
					return nil, err
				}
				out["dream_refs"] = dreamRefs(dreams)
			}
			return out, nil
		},
	}
}

func evalScoreRetrievalCaseTool(deps Dependencies) Tool {
	return Tool{
		Name:        "eval_score_retrieval_case",
		Description: "Score one ranked retrieval case and optional context refs against required and bad refs using deterministic recallquality metrics.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"ranked_refs", "required_refs"},
			"properties": map[string]any{
				"k":                      map[string]any{"type": "integer", "minimum": 1, "maximum": maxEvalK},
				"ranked_refs":            evalRefArraySchema(false),
				"context_refs":           evalRefArraySchema(false),
				"evidence_refs":          evalRefArraySchema(false),
				"dream_refs":             evalRefArraySchema(false),
				"required_refs":          evalRefArraySchema(true),
				"bad_refs":               evalRefArraySchema(false),
				"required_evidence_refs": evalRefArraySchema(true),
				"bad_evidence_refs":      evalRefArraySchema(false),
				"required_dream_refs":    evalRefArraySchema(true),
				"bad_dream_refs":         evalRefArraySchema(false),
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			k := intInputOrDefault(input["k"], defaultEvalK)
			if err := auditEvaluationTool(ctx, deps, "eval_score_retrieval_case", k, false, nil); err != nil {
				return nil, err
			}
			ranked := evalResultRefs(input["ranked_refs"])
			required := evalJudgments(input["required_refs"])
			bad := evalResultRefs(input["bad_refs"])
			metrics := recallquality.ScoreAtK(ranked, required, bad, k)
			out, err := structToMap(metrics)
			if err != nil {
				return nil, err
			}
			if _, ok := input["context_refs"]; ok {
				contextMetrics := recallquality.ScoreAtK(evalResultRefs(input["context_refs"]), required, bad, k)
				out["context_scored"] = true
				out["context_relevant_at_k"] = contextMetrics.RelevantAtK
				out["context_relevant_total"] = contextMetrics.RelevantTotal
				out["context_bad_at_k"] = contextMetrics.BadAtK
				out["context_recall_at_k"] = contextMetrics.RecallAtK
				out["context_mrr"] = contextMetrics.MRR
				out["context_ndcg_at_k"] = contextMetrics.NDCGAtK
			}
			if _, ok := input["evidence_refs"]; ok {
				evidenceMetrics := recallquality.ScoreAtK(
					evalResultRefs(input["evidence_refs"]),
					evalJudgments(input["required_evidence_refs"]),
					evalResultRefs(input["bad_evidence_refs"]),
					k,
				)
				out["evidence_scored"] = true
				out["evidence_relevant_at_k"] = evidenceMetrics.RelevantAtK
				out["evidence_relevant_total"] = evidenceMetrics.RelevantTotal
				out["evidence_bad_at_k"] = evidenceMetrics.BadAtK
				out["evidence_recall_at_k"] = evidenceMetrics.RecallAtK
				out["evidence_mrr"] = evidenceMetrics.MRR
				out["evidence_ndcg_at_k"] = evidenceMetrics.NDCGAtK
			}
			if _, ok := input["dream_refs"]; ok {
				dreamMetrics := recallquality.ScoreAtK(
					evalResultRefs(input["dream_refs"]),
					evalJudgments(input["required_dream_refs"]),
					evalResultRefs(input["bad_dream_refs"]),
					k,
				)
				out["dream_scored"] = true
				out["dream_relevant_at_k"] = dreamMetrics.RelevantAtK
				out["dream_relevant_total"] = dreamMetrics.RelevantTotal
				out["dream_bad_at_k"] = dreamMetrics.BadAtK
				out["dream_recall_at_k"] = dreamMetrics.RecallAtK
				out["dream_mrr"] = dreamMetrics.MRR
				out["dream_ndcg_at_k"] = dreamMetrics.NDCGAtK
			}
			return out, nil
		},
	}
}

func evalListFragments(ctx context.Context, deps Dependencies, profileID string, input map[string]any, limit int, metadataOnly bool) (map[string]any, error) {
	if deps.FragmentList == nil {
		return nil, ErrToolUnavailable
	}
	opts := fragmentservice.ListOptions{Limit: limit}
	if cursor, ok := input["cursor"].(string); ok {
		opts.Cursor = cursor
	}
	fragments, nextCursor, err := deps.FragmentList.List(ctx, profileID, opts)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(fragments))
	for i := range fragments {
		item, err := structToMap(&fragments[i])
		if err != nil {
			return nil, err
		}
		if metadataOnly {
			stripEvalContent("fragment", item)
		}
		items = append(items, item)
	}
	return evalPage(items, nextCursor), nil
}

func evalListClaims(ctx context.Context, deps Dependencies, profileID string, input map[string]any, limit int, metadataOnly bool) (map[string]any, error) {
	status, _ := input["status"].(string)
	if deps.ClaimListFiltered != nil {
		opts := claimservice.ListClaimOptions{Limit: limit, Status: status}
		if cursor, ok := input["cursor"].(string); ok {
			opts.Cursor = cursor
		}
		result, err := deps.ClaimListFiltered.List(ctx, profileID, opts)
		if err != nil {
			return nil, err
		}
		items, err := claimItems(result.Items, metadataOnly)
		if err != nil {
			return nil, err
		}
		return evalPage(items, result.NextCursor), nil
	}
	if deps.ClaimList == nil {
		return nil, ErrToolUnavailable
	}
	offset := cursorOffset(input["cursor"])
	claims, total, err := deps.ClaimList.List(ctx, profileID, limit, offset)
	if err != nil {
		return nil, err
	}
	items, err := claimItems(claims, metadataOnly)
	if err != nil {
		return nil, err
	}
	nextCursor := ""
	if offset+len(claims) < total {
		nextCursor = strconv.Itoa(offset + len(claims))
	}
	return evalPage(items, nextCursor), nil
}

func evalListFacts(ctx context.Context, deps Dependencies, profileID string, input map[string]any, limit int, metadataOnly bool) (map[string]any, error) {
	if deps.FactList == nil {
		return nil, ErrToolUnavailable
	}
	filters := factservice.FactListFilters{}
	if status, ok := input["status"].(string); ok && status != "" {
		filters.Status = domain.FactStatus(status)
	}
	cursor, _ := input["cursor"].(string)
	facts, nextCursor, err := deps.FactList.List(ctx, profileID, filters, limit, cursor)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(facts))
	for _, fact := range facts {
		item, err := structToMap(fact)
		if err != nil {
			return nil, err
		}
		if metadataOnly {
			stripEvalContent("fact", item)
		}
		items = append(items, item)
	}
	return evalPage(items, nextCursor), nil
}

func evalListCommunities(ctx context.Context, deps Dependencies, profileID string, limit int, metadataOnly bool) (map[string]any, error) {
	if deps.CommunityList == nil {
		return nil, ErrToolUnavailable
	}
	communities, err := deps.CommunityList.List(ctx, profileID, limit)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(communities))
	for _, community := range communities {
		item, err := structToMap(community)
		if err != nil {
			return nil, err
		}
		if metadataOnly {
			stripEvalContent("community", item)
		}
		items = append(items, item)
	}
	return evalPage(items, ""), nil
}

func evalListEdges(ctx context.Context, deps Dependencies, profileID string, limit int) (map[string]any, error) {
	if deps.GraphQuery == nil {
		return nil, ErrToolUnavailable
	}
	query := fmt.Sprintf(`
MATCH (a {team_id: $profileId})-[rel]->(b {team_id: $profileId})
WHERE rel.team_id = $profileId
RETURN type(rel) AS edge_type,
       labels(a) AS from_labels,
       coalesce(a.fact_id, a.claim_id, a.fragment_id, a.community_id, '') AS from_id,
       labels(b) AS to_labels,
       coalesce(b.fact_id, b.claim_id, b.fragment_id, b.community_id, '') AS to_id
LIMIT %d`, limit)
	res, err := deps.GraphQuery.Execute(ctx, profileID, query, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": res.Rows, "next_cursor": "", "has_more": false}, nil
}

func evalGetKnowledgeItem(ctx context.Context, deps Dependencies, profileID, kind, id string) (map[string]any, error) {
	switch kind {
	case "fragment":
		if deps.FragmentGet == nil {
			return nil, ErrToolUnavailable
		}
		fragment, err := deps.FragmentGet.GetByID(ctx, profileID, id)
		if err != nil {
			return nil, err
		}
		return structToMap(fragment)
	case "claim":
		if deps.ClaimGet == nil {
			return nil, ErrToolUnavailable
		}
		claim, err := deps.ClaimGet.Get(ctx, profileID, id)
		if err != nil {
			return nil, err
		}
		return structToMap(claim)
	case "fact":
		if deps.FactGet == nil {
			return nil, ErrToolUnavailable
		}
		fact, err := deps.FactGet.Get(ctx, profileID, id)
		if err != nil {
			return nil, err
		}
		return structToMap(fact)
	case "community":
		if deps.CommunityGet == nil {
			return nil, ErrToolUnavailable
		}
		community, err := deps.CommunityGet.Get(ctx, profileID, id)
		if err != nil {
			return nil, err
		}
		return structToMap(community)
	case "dream":
		if deps.Dreams == nil {
			return nil, ErrToolUnavailable
		}
		dream, err := deps.Dreams.Get(ctx, profileID, id)
		if err != nil {
			return nil, err
		}
		return structToMap(dream)
	default:
		return nil, fmt.Errorf("eval_get_knowledge_item: unsupported type %q", kind)
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

func evalLimit(ctx context.Context, deps Dependencies, input map[string]any) int {
	limit := appservice.DefaultEvaluationExportMaxPageSize
	if deps.EvaluationConfig != nil {
		if cfg, err := deps.EvaluationConfig.EvaluationRuntimeConfig(ctx); err == nil && cfg.ExportMaxPageSize > 0 {
			limit = cfg.ExportMaxPageSize
		}
	}
	if requested, ok := intInput(input["limit"]); ok && requested > 0 && requested < limit {
		limit = requested
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func evalRecallFeedbackFilter(ctx context.Context, profileID string, input map[string]any, limit int) (domain.RecallFeedbackEventFilter, error) {
	filter := domain.RecallFeedbackEventFilter{Limit: limit}
	if offset, ok := intInput(input["offset"]); ok {
		filter.Offset = offset
	}
	if quality, ok := input["quality"].(string); ok {
		filter.Quality = quality
	}
	if includePending, ok := input["include_pending"].(bool); ok {
		filter.IncludePending = includePending
	}
	if missing, ok := input["missing_context"].(bool); ok {
		filter.MissingContext = &missing
	}
	if irrelevant, ok := input["irrelevant"].(bool); ok {
		filter.Irrelevant = &irrelevant
	}
	if from, ok := input["from"].(string); ok && strings.TrimSpace(from) != "" {
		parsed, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return filter, fmt.Errorf("from must be RFC3339")
		}
		filter.From = &parsed
	}
	if to, ok := input["to"].(string); ok && strings.TrimSpace(to) != "" {
		parsed, err := time.Parse(time.RFC3339, to)
		if err != nil {
			return filter, fmt.Errorf("to must be RFC3339")
		}
		filter.To = &parsed
	}
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		filter.TeamID = &actor.TeamID
		return filter, nil
	}
	if parsed, err := uuid.Parse(profileID); err == nil {
		filter.TeamID = &parsed
	}
	return filter, nil
}

func evalEventInScope(ctx context.Context, profileID string, event *domain.RecallFeedbackEvent) bool {
	if event == nil {
		return false
	}
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		return event.TeamID != nil && *event.TeamID == actor.TeamID
	}
	parsed, err := uuid.Parse(profileID)
	if err != nil {
		return false
	}
	return event.TeamID != nil && *event.TeamID == parsed
}

func evalRecallRequest(input map[string]any) (recallservice.RecallRequest, error) {
	req := recallservice.RecallRequest{
		Query:           stringInput(input["query"]),
		Limit:           intInputOrDefault(input["limit"], recallservice.DefaultLimit),
		IncludeEvidence: boolInputOrDefault(input["include_evidence"], true),
		UseCommunities:  boolInput(input["use_communities"]),
	}
	if validAt, err := optionalTime(input["valid_at"]); err != nil {
		return req, err
	} else {
		req.ValidAt = validAt
	}
	if knownAt, err := optionalTime(input["known_at"]); err != nil {
		return req, err
	} else {
		req.KnownAt = knownAt
	}
	return req, nil
}

func evalAssembleRequest(input map[string]any, recallReq recallservice.RecallRequest) contextservice.AssembleRequest {
	includeEvidence := boolInputOrDefault(input["include_evidence"], recallReq.IncludeEvidence)
	return contextservice.AssembleRequest{
		Query:           recallReq.Query,
		Limit:           recallReq.Limit,
		IncludeEvidence: &includeEvidence,
		ValidAt:         recallReq.ValidAt,
		KnownAt:         recallReq.KnownAt,
		MaxChars:        intInputOrDefault(input["max_context_chars"], 4000),
	}
}

func evalPage(items []map[string]any, nextCursor string) map[string]any {
	return map[string]any{
		"items":       items,
		"next_cursor": nextCursor,
		"has_more":    nextCursor != "",
	}
}

func claimItems(claims []*domain.Claim, metadataOnly bool) ([]map[string]any, error) {
	items := make([]map[string]any, 0, len(claims))
	for _, claim := range claims {
		item, err := structToMap(claim)
		if err != nil {
			return nil, err
		}
		if metadataOnly {
			stripEvalContent("claim", item)
		}
		items = append(items, item)
	}
	return items, nil
}

func stripEvalContent(kind string, item map[string]any) {
	switch kind {
	case "fragment":
		delete(item, "content")
	case "claim", "fact":
		delete(item, "object")
	case "community":
		delete(item, "summary")
	case "dream":
		delete(item, "hypothesis")
		delete(item, "what_if")
		delete(item, "possible_outcome")
		delete(item, "rationale")
	}
}

func recallHitRef(rank int, hit recallservice.RecallHit) map[string]any {
	ref := map[string]any{
		"rank":          rank,
		"tier":          hit.Tier,
		"score":         hit.Score,
		"final_score":   hit.FinalScore,
		"semantic_rank": hit.SemanticRank,
		"keyword_rank":  hit.KeywordRank,
	}
	switch {
	case hit.Fact != nil:
		ref["type"] = "fact"
		ref["id"] = hit.Fact.FactID
		ref["status"] = string(hit.Fact.Status)
	case hit.Claim != nil:
		ref["type"] = "claim"
		ref["id"] = hit.Claim.ClaimID
		ref["status"] = string(hit.Claim.Status)
	case hit.Fragment != nil:
		ref["type"] = "fragment"
		ref["id"] = hit.Fragment.FragmentID
		ref["status"] = string(hit.Fragment.Status)
	}
	return ref
}

func contextItemRefs(items []contextservice.ContextItem) []map[string]any {
	refs := make([]map[string]any, 0, len(items))
	for i, item := range items {
		refs = append(refs, map[string]any{
			"rank":  i + 1,
			"type":  item.Type,
			"id":    item.ID,
			"score": item.Score,
		})
	}
	return refs
}

func contextEvidenceRefs(items []contextservice.ContextItem) []map[string]any {
	refs := []map[string]any{}
	seen := map[string]struct{}{}
	rank := 1
	for _, item := range items {
		for _, fragment := range item.EvidenceFragments {
			if fragment == nil || strings.TrimSpace(fragment.FragmentID) == "" {
				continue
			}
			key := "fragment:" + fragment.FragmentID + "|parent:" + item.Type + ":" + item.ID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			refs = append(refs, map[string]any{
				"rank":        rank,
				"type":        "fragment",
				"id":          fragment.FragmentID,
				"parent_type": item.Type,
				"parent_id":   item.ID,
			})
			rank++
		}
	}
	return refs
}

func evalRefArraySchema(withGrade bool) map[string]any {
	properties := map[string]any{
		"type": schemaEnum([]string{"fragment", "claim", "fact", "community", "dream"}),
		"id":   schemaString("Reference ID.", 256),
	}
	required := []string{"type", "id"}
	if withGrade {
		properties["grade"] = map[string]any{"type": "number", "minimum": 0}
	}
	return map[string]any{
		"type":     "array",
		"maxItems": 500,
		"items": map[string]any{
			"type":                 "object",
			"required":             required,
			"properties":           properties,
			"additionalProperties": true,
		},
	}
}

func evalResultRefs(value any) []recallquality.ResultRef {
	maps := evalRefMaps(value)
	if maps == nil {
		return nil
	}
	refs := make([]recallquality.ResultRef, 0, len(maps))
	for _, m := range maps {
		refs = append(refs, recallquality.ResultRef{
			Type: stringInput(m["type"]),
			ID:   stringInput(m["id"]),
		})
	}
	return refs
}

func evalJudgments(value any) []recallquality.Judgment {
	maps := evalRefMaps(value)
	if maps == nil {
		return nil
	}
	refs := make([]recallquality.Judgment, 0, len(maps))
	for _, m := range maps {
		grade := 1.0
		if parsed, ok := schemaNumber(m["grade"]); ok {
			grade = parsed
		}
		refs = append(refs, recallquality.Judgment{
			Type:  stringInput(m["type"]),
			ID:    stringInput(m["id"]),
			Grade: grade,
		})
	}
	return refs
}

func evalRefMaps(value any) []map[string]any {
	switch raw := value.(type) {
	case []any:
		maps := make([]map[string]any, 0, len(raw))
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				maps = append(maps, m)
			}
		}
		return maps
	case []map[string]any:
		return raw
	default:
		return nil
	}
}

func optionalTime(value any) (*time.Time, error) {
	raw := strings.TrimSpace(stringInput(value))
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

func boolInput(value any) bool {
	parsed, _ := value.(bool)
	return parsed
}

func boolInputOrDefault(value any, fallback bool) bool {
	parsed, ok := value.(bool)
	if !ok {
		return fallback
	}
	return parsed
}

func stringInput(value any) string {
	parsed, _ := value.(string)
	return strings.TrimSpace(parsed)
}

func cursorOffset(value any) int {
	offset, err := strconv.Atoi(stringInput(value))
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" && value != uuid.Nil.String() {
			return value
		}
	}
	return ""
}
