// Package evaluation owns the offline evaluation application workflows.
//
// The package depends on capability contracts and application APIs only. Its
// PostgreSQL reader and provider-backed services are supplied at composition
// time, so the production server does not construct this graph.
package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

const (
	DefaultPageSize       = 100
	MaxPageSize           = 500
	DefaultRecallCaseSize = 10
)

var ErrToolUnavailable = errors.New("evaluation: required dependency is unavailable")

// Repository is the narrow read port used by the evaluation application.
type Repository = dreamcontract.EvaluationRepository

// AuditAppender is the only audit operation required by evaluation tools.
type AuditAppender interface {
	Append(context.Context, accessservice.AuditLogEntry) error
}

type Dependencies struct {
	Repository Repository
	Recall     contract.Service

	Audit AuditAppender
}

// Service owns evaluation orchestration and result shaping. Transport and
// registry packages bind request schemas and call these methods.
type Service struct {
	deps Dependencies
}

func New(deps Dependencies) *Service {
	return &Service{deps: deps}
}

func (s *Service) ListKnowledgeRefs(ctx context.Context, fallbackTeamID, kind string, limit int, cursor, status string, metadataOnly bool) (map[string]any, error) {
	kind = normalizeListType(kind)
	limit = normalizePageLimit(limit)
	if err := s.audit(ctx, "eval_list_knowledge_refs", limit, !metadataOnly, map[string]any{"type": kind, "status": status}); err != nil {
		return nil, err
	}
	teamID, err := actorTeamID(ctx, fallbackTeamID)
	if err != nil {
		return nil, err
	}
	if s.deps.Repository == nil {
		return nil, ErrToolUnavailable
	}
	page, err := s.deps.Repository.ListEvaluationRefs(ctx, dreamcontract.EvaluationListInput{
		TeamID: teamID,
		Type:   kind,
		Limit:  limit,
		Cursor: cursor,
		Status: status,
	})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		copied := copyItem(item)
		if metadataOnly {
			stripContent(kind, copied)
		}
		items = append(items, copied)
	}
	return pageResult(items, page.NextCursor, page.HasMore), nil
}

func (s *Service) RunRecallCase(ctx context.Context, caseID string, req contract.Request, validAt, knownAt string, includeEvidence bool) (map[string]any, error) {
	if s.deps.Recall == nil {
		return nil, ErrToolUnavailable
	}
	limit := positiveOrDefault(req.Limit, DefaultRecallCaseSize)
	req.Limit = limit
	if err := s.audit(ctx, "eval_run_recall_case", limit, false, map[string]any{"case_id": caseID}); err != nil {
		return nil, err
	}
	if validAt != "" {
		parsed, err := parseOptionalTime(validAt)
		if err != nil {
			return nil, err
		}
		req.ValidAt = parsed
	}
	if knownAt != "" {
		parsed, err := parseOptionalTime(knownAt)
		if err != nil {
			return nil, err
		}
		req.KnownAt = parsed
	}
	started := time.Now()
	result, err := s.deps.Recall.Recall(ctx, req)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"case_id":     caseID,
		"query":       req.Query,
		"ranked_refs": resultRefs(result),
		"latency_ms":  time.Since(started).Milliseconds(),
	}
	if result != nil {
		out["recall_id"] = result.RecallID
		out["search_state"] = result.SearchState
		if result.Degradation != nil {
			out["degradation"] = result.Degradation
		}
	}
	if includeEvidence {
		out["context_evidence_refs"] = resultRefs(result)
	}
	return out, nil
}

func (s *Service) audit(ctx context.Context, tool string, pageSize int, contentReturned bool, filters map[string]any) error {
	if s.deps.Audit == nil {
		return ErrToolUnavailable
	}
	var profileID *string
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		teamID := actor.TeamID.String()
		profileID = &teamID
	}
	var keyID *string
	actorRole := "system"
	if actor, ok := requestctx.ActorFromContext(ctx); ok {
		if actor.CredentialID != nil {
			id := actor.CredentialID.String()
			keyID = &id
		}
		if actor.Role != "" {
			actorRole = actor.Role
		}
	}
	return s.deps.Audit.Append(ctx, accessservice.AuditLogEntry{
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

func actorTeamID(ctx context.Context, fallback string) (string, error) {
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		return actor.TeamID.String(), nil
	}
	parsed, err := uuid.Parse(fallback)
	if err != nil {
		return "", errors.New("evaluation tool requires authenticated team context")
	}
	return parsed.String(), nil
}

func positiveOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func normalizeListType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "fragment":
		return "evidence"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizePageLimit(value int) int {
	if value <= 0 {
		return DefaultPageSize
	}
	if value > MaxPageSize {
		return MaxPageSize
	}
	return value
}

func parseOptionalTime(value string) (*time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("time value must be RFC3339")
	}
	return &parsed, nil
}

func pageResult(items []map[string]any, nextCursor string, hasMore bool) map[string]any {
	return map[string]any{"items": items, "next_cursor": nextCursor, "has_more": hasMore}
}

func copyItem(item map[string]any) map[string]any {
	copied := make(map[string]any, len(item))
	for key, value := range item {
		copied[key] = value
	}
	return copied
}

func stripContent(kind string, item map[string]any) {
	switch kind {
	case "evidence":
		delete(item, "content")
	case "entity":
		delete(item, "canonical_name")
		delete(item, "identity_context")
	case "value":
		delete(item, "canonical_value")
		delete(item, "display")
	}
}

func structMap(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("evaluation: encode result: %w", err)
	}
	var mapped map[string]any
	if err := json.Unmarshal(encoded, &mapped); err != nil {
		return nil, fmt.Errorf("evaluation: decode result: %w", err)
	}
	return mapped, nil
}

func resultRefs(result *contract.RecallResult) []map[string]any {
	if result == nil {
		return []map[string]any{}
	}
	refs := make([]map[string]any, 0, len(result.Results))
	for _, item := range result.Results {
		if strings.TrimSpace(item.EvidenceID) == "" {
			continue
		}
		rank := item.Rank
		if rank <= 0 {
			rank = len(refs) + 1
		}
		ref := map[string]any{"rank": rank, "type": "evidence", "id": item.EvidenceID, "equivalent_evidence_ids": append([]string{}, item.EquivalentEvidenceIDs...), "equivalents_truncated": item.EquivalentsTruncated}
		if len(item.RelationshipIDs) > 0 {
			ref["relationship_ids"] = append([]string(nil), item.RelationshipIDs...)
		}
		refs = append(refs, ref)
	}
	return refs
}
