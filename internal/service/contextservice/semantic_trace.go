package contextservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/assertionservice"
	"github.com/markhuangai/dense-mem/internal/service/graphrow"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

func (s *service) traceAssertion(ctx context.Context, profileID, assertionID string, req TraceRequest, includeFragments bool) (*TraceResult, error) {
	if s.deps.Assertions == nil {
		return nil, errors.New("context trace: assertion service is required")
	}
	if s.deps.Reader == nil {
		return nil, errors.New("context trace: graph reader is required")
	}
	assertion, err := s.deps.Assertions.GetAssertion(ctx, profileID, assertionID)
	if err != nil {
		return nil, err
	}
	if assertion == nil {
		return nil, errors.New("context trace: assertion not found")
	}
	if assertion.Status == domain.AssertionStatusQuarantined {
		return nil, errors.New("context trace: quarantined assertions cannot be traversed")
	}
	result := &TraceResult{Anchor: TraceAnchor{Type: AnchorAssertion, Assertion: assertion}}
	if includeFragments {
		ids := make([]string, 0, len(assertion.Evidence))
		for _, evidence := range assertion.Evidence {
			ids = append(ids, evidence.FragmentID)
		}
		result.SupportingFragments, result.MissingFragmentIDs, err = s.loadFragments(ctx, profileID, ids)
		if err != nil {
			return nil, err
		}
	}

	maxDepth := clampInt(req.MaxDepth, defaultTraceDepth, maxTraceDepth)
	maxEdges := clampInt(req.MaxEdges, defaultTraceEdges, maxTraceEdges)
	relationshipTypes, err := normalizeRelationshipFilters(req.RelationshipTypes)
	if err != nil {
		return nil, err
	}
	minRelevance := req.MinRelevance
	if minRelevance < 0 || minRelevance > 1 {
		return nil, errors.New("context trace: min_relevance must be between 0 and 1")
	}
	if minRelevance == 0 {
		minRelevance = 0.15
	}
	frontier := []string{assertion.SubjectEntityID}
	if assertion.ObjectEntityID != "" {
		frontier = append(frontier, assertion.ObjectEntityID)
	}
	visitedEntities := map[string]struct{}{}
	visitedAssertions := map[string]struct{}{assertion.AssertionID: {}}
	for _, id := range frontier {
		if id != "" {
			visitedEntities[id] = struct{}{}
		}
	}
	nodeByKey := map[string]recallservice.SemanticNode{}
	edges := []recallservice.SemanticEdge{}
	stopped := "depth_budget"

	for depth := 0; depth < maxDepth && len(frontier) > 0 && len(edges) < maxEdges; depth++ {
		assertionIDs := make([]string, 0, len(visitedAssertions))
		for id := range visitedAssertions {
			assertionIDs = append(assertionIDs, id)
		}
		_, rows, err := s.deps.Reader.ScopedRead(ctx, profileID, semanticTraceStepCypher, map[string]any{
			"frontier":          frontier,
			"relationshipTypes": relationshipTypes,
			"visitedAssertions": assertionIDs,
			"topic":             strings.ToLower(strings.TrimSpace(req.Topic)),
			"minRelevance":      minRelevance,
			"limit":             int64(maxEdges - len(edges)),
		})
		if err != nil {
			return nil, err
		}
		next := []string{}
		for _, row := range rows {
			edge, source, target := semanticTraceRow(row)
			if edge.AssertionID == "" {
				continue
			}
			if _, exists := visitedAssertions[edge.AssertionID]; exists {
				continue
			}
			visitedAssertions[edge.AssertionID] = struct{}{}
			edges = append(edges, edge)
			nodeByKey[source.Key] = source
			nodeByKey[target.Key] = target
			for _, node := range []recallservice.SemanticNode{source, target} {
				if strings.HasPrefix(node.Type, "value:") || node.ID == "" {
					continue
				}
				if _, exists := visitedEntities[node.ID]; exists {
					continue
				}
				visitedEntities[node.ID] = struct{}{}
				next = append(next, node.ID)
			}
		}
		frontier = uniqueStrings(next)
		if len(frontier) == 0 {
			stopped = "frontier_exhausted"
			break
		}
	}
	if len(edges) >= maxEdges {
		stopped = "edge_budget"
	}
	result.SemanticEdges = edges
	for _, node := range nodeByKey {
		result.SemanticNodes = append(result.SemanticNodes, node)
	}
	sort.Slice(result.SemanticNodes, func(i, j int) bool { return result.SemanticNodes[i].Key < result.SemanticNodes[j].Key })
	for id := range visitedEntities {
		result.VisitedEntityIDs = append(result.VisitedEntityIDs, id)
	}
	sort.Strings(result.VisitedEntityIDs)
	result.StoppedReason = stopped
	if len(frontier) > 0 {
		assertionIDs := make([]string, 0, len(visitedAssertions))
		for id := range visitedAssertions {
			assertionIDs = append(assertionIDs, id)
		}
		result.Frontier, err = s.loadTraceFrontier(ctx, profileID, frontier, assertionIDs, relationshipTypes, req.Topic, minRelevance)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func normalizeRelationshipFilters(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := assertionservice.RelationshipType(value)
		if normalized == "" || len(normalized) > 64 {
			return nil, fmt.Errorf("context trace: invalid relationship type %q", value)
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func semanticTraceRow(row map[string]any) (recallservice.SemanticEdge, recallservice.SemanticNode, recallservice.SemanticNode) {
	source := recallservice.SemanticNode{
		Key:  graphrow.String(row, "source_key"),
		ID:   graphrow.String(row, "source_id"),
		Type: graphrow.String(row, "source_type"),
		Name: graphrow.String(row, "source_name"),
	}
	target := recallservice.SemanticNode{
		Key:  graphrow.String(row, "target_key"),
		ID:   graphrow.String(row, "target_id"),
		Type: graphrow.String(row, "target_type"),
		Name: graphrow.String(row, "target_name"),
	}
	return recallservice.SemanticEdge{
		AssertionID:  graphrow.String(row, "assertion_id"),
		Source:       source.Key,
		Target:       target.Key,
		Relationship: graphrow.String(row, "relationship_type"),
		Predicate:    graphrow.String(row, "predicate"),
		Tier:         domain.AssertionTier(graphrow.String(row, "tier")),
		Status:       domain.AssertionStatus(graphrow.String(row, "status")),
		Polarity:     domain.ClaimPolarity(graphrow.String(row, "polarity")),
		ValidFrom:    graphrow.TimePtr(row, "valid_from"),
		ValidTo:      graphrow.TimePtr(row, "valid_to"),
		EvidenceIDs:  graphrow.StringSlice(row, "evidence_ids"),
	}, source, target
}

func (s *service) loadTraceFrontier(ctx context.Context, profileID string, frontier, visitedAssertions, relationshipTypes []string, topic string, minRelevance float64) ([]recallservice.FrontierHint, error) {
	_, rows, err := s.deps.Reader.ScopedRead(ctx, profileID, semanticTraceStepCypher, map[string]any{
		"frontier":          frontier,
		"relationshipTypes": relationshipTypes,
		"visitedAssertions": visitedAssertions,
		"topic":             strings.ToLower(strings.TrimSpace(topic)),
		"minRelevance":      minRelevance,
		"limit":             int64(12),
	})
	if err != nil {
		return nil, err
	}
	frontierSet := map[string]struct{}{}
	for _, id := range frontier {
		frontierSet[id] = struct{}{}
	}
	hints := make([]recallservice.FrontierHint, 0, len(rows))
	for _, row := range rows {
		edge, source, target := semanticTraceRow(row)
		from := target
		neighbor := source
		direction := "incoming"
		if _, exists := frontierSet[source.ID]; exists {
			from = source
			neighbor = target
			direction = "outgoing"
		}
		hints = append(hints, recallservice.FrontierHint{
			FromEntityID: from.ID,
			Direction:    direction,
			Relationship: edge.Relationship,
			AssertionID:  edge.AssertionID,
			Neighbor:     neighbor,
			Tier:         edge.Tier,
		})
	}
	return hints, nil
}

const semanticTraceStepCypher = `
MATCH (source)-[relationship]->(target)
WHERE source.team_id = $profileId
  AND target.team_id = $profileId
  AND relationship.team_id = $profileId
  AND relationship.semantic_projection = true
  AND relationship.status = 'active'
  AND relationship.assertion_id IS NOT NULL
  AND NOT (relationship.assertion_id IN $visitedAssertions)
  AND (
    (source:Entity AND source.entity_id IN $frontier) OR
    (target:Entity AND target.entity_id IN $frontier)
  )
  AND (size($relationshipTypes) = 0 OR type(relationship) IN $relationshipTypes)
MATCH (assertion:Assertion {team_id: $profileId, assertion_id: relationship.assertion_id, status: 'active'})
OPTIONAL MATCH (assertion)-[:SUPPORTED_BY {team_id: $profileId}]->(fragment:SourceFragment {team_id: $profileId})
WITH source, target, relationship, assertion,
     collect(DISTINCT fragment.fragment_id) AS evidence_ids,
     CASE
       WHEN $topic = '' THEN coalesce(relationship.source_quality, 0)
       WHEN toLower(
         coalesce(source.canonical_name, source.display, source.value, '') + ' ' +
         type(relationship) + ' ' +
         coalesce(target.canonical_name, target.display, target.value, '')
       ) CONTAINS $topic THEN 1.0
       ELSE coalesce(relationship.source_quality, 0) * 0.5
     END AS relevance
WHERE relevance >= $minRelevance
RETURN source.graph_key AS source_key,
       CASE WHEN source:Entity THEN source.entity_id ELSE source.value_id END AS source_id,
       CASE WHEN source:Entity THEN source.entity_type ELSE 'value:' + source.value_type END AS source_type,
       CASE WHEN source:Entity THEN source.canonical_name ELSE coalesce(source.display, source.value) END AS source_name,
       target.graph_key AS target_key,
       CASE WHEN target:Entity THEN target.entity_id ELSE target.value_id END AS target_id,
       CASE WHEN target:Entity THEN target.entity_type ELSE 'value:' + target.value_type END AS target_type,
       CASE WHEN target:Entity THEN target.canonical_name ELSE coalesce(target.display, target.value) END AS target_name,
       assertion.assertion_id AS assertion_id,
       type(relationship) AS relationship_type,
       assertion.predicate_key AS predicate,
       assertion.tier AS tier,
       assertion.status AS status,
       assertion.polarity AS polarity,
       assertion.valid_from AS valid_from,
       assertion.valid_to AS valid_to,
       evidence_ids,
       relevance
ORDER BY relevance DESC,
         CASE assertion.tier WHEN 'fact' THEN 0 WHEN 'validated_claim' THEN 1 ELSE 2 END,
         assertion.recorded_at DESC,
         assertion.assertion_id ASC
LIMIT $limit`
