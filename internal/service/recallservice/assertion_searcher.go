package recallservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/fulltextquery"
	"github.com/markhuangai/dense-mem/internal/service/graphrow"
	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const assertionSearchCypher = `
CALL {
  CALL db.index.vector.queryNodes('assertion_embedding_idx', $candidateLimit, $embedding)
  YIELD node, score
  WHERE node.team_id = $profileId
  RETURN node, score AS branch_score
  UNION ALL
  CALL db.index.fulltext.queryNodes('assertion_recall_idx', $searchQuery, {limit: $candidateLimit})
  YIELD node, score
  WHERE node.team_id = $profileId
  RETURN node, score / (score + 1.0) AS branch_score
}
WITH node, max(branch_score) AS base_score
WHERE node.status = 'active'
  AND node.tier IN ['candidate', 'validated_claim', 'fact']
  AND ($validAt IS NULL OR (
    (node.valid_from IS NULL OR node.valid_from <= $validAt) AND
    (node.valid_to IS NULL OR node.valid_to > $validAt)
  ))
  AND ($knownAt IS NULL OR (
    node.recorded_at <= $knownAt AND
    (node.recorded_to IS NULL OR node.recorded_to > $knownAt)
  ))
MATCH (subject:Entity {team_id: $profileId, entity_id: node.subject_entity_id})
MATCH (object {team_id: $profileId})
WHERE object.graph_key = CASE
  WHEN coalesce(node.object_entity_id, '') <> '' THEN 'entity:' + node.object_entity_id
  ELSE 'value:' + node.object_value_id
END
MATCH (subject)-[relationship]->(object)
WHERE relationship.team_id = $profileId
  AND relationship.assertion_id = node.assertion_id
  AND relationship.semantic_projection = true
  AND relationship.status = 'active'
OPTIONAL MATCH (node)-[:SUPPORTED_BY {team_id: $profileId}]->(fragment:SourceFragment {team_id: $profileId})
WITH node, subject, object, relationship, base_score,
     collect(DISTINCT fragment.fragment_id) AS evidence_ids
RETURN node.assertion_id AS assertion_id,
       node.owner_profile_id AS owner_profile_id,
       node.subject_entity_id AS subject_entity_id,
       node.predicate_key AS predicate_key,
       type(relationship) AS relationship_type,
       node.object_entity_id AS object_entity_id,
       node.object_value_id AS object_value_id,
       node.tier AS tier,
       node.status AS status,
       node.policy_family AS policy_family,
       node.polarity AS polarity,
       node.modality AS modality,
       node.valid_from AS valid_from,
       node.valid_to AS valid_to,
       node.recorded_at AS recorded_at,
       node.recorded_to AS recorded_to,
       node.extract_conf AS extract_conf,
       node.resolution_conf AS resolution_conf,
       node.source_quality AS source_quality,
       node.support_count AS support_count,
       node.source_group_count AS source_group_count,
       node.evidence_json AS evidence_json,
       node.embedding_model AS embedding_model,
       node.extraction_model AS extraction_model,
       node.extraction_version AS extraction_version,
       node.verifier_model AS verifier_model,
       node.pipeline_run_id AS pipeline_run_id,
       node.projection_version AS projection_version,
       node.created_at AS created_at,
       node.updated_at AS updated_at,
       subject.entity_id AS subject_id,
       subject.canonical_name AS subject_name,
       subject.entity_type AS subject_type,
       object.graph_key AS object_key,
       CASE WHEN object:Entity THEN object.entity_id ELSE object.value_id END AS object_id,
       CASE WHEN object:Entity THEN object.canonical_name ELSE coalesce(object.display, object.value) END AS object_name,
       CASE WHEN object:Entity THEN object.entity_type ELSE 'value:' + object.value_type END AS object_type,
       CASE WHEN object:Value THEN object.value ELSE NULL END AS object_value,
       CASE WHEN object:Value THEN object.display ELSE NULL END AS object_display,
       CASE WHEN object:Value THEN object.unit ELSE NULL END AS object_unit,
       evidence_ids,
       base_score * CASE node.tier WHEN 'fact' THEN 1.0 WHEN 'validated_claim' THEN 0.75 ELSE 0.4 END AS score
ORDER BY score DESC, node.recorded_at DESC, node.assertion_id ASC
LIMIT $limit`

const assertionFrontierCypher = `
MATCH (source:Entity {team_id: $profileId})-[relationship]->(target)
WHERE source.entity_id IN $entityIds
  AND target.team_id = $profileId
  AND relationship.team_id = $profileId
  AND relationship.semantic_projection = true
  AND relationship.status = 'active'
  AND NOT relationship.assertion_id IN $assertionIds
RETURN source.entity_id AS from_entity_id,
       'outgoing' AS direction,
       type(relationship) AS relationship_type,
       relationship.assertion_id AS assertion_id,
       relationship.tier AS tier,
       target.graph_key AS neighbor_key,
       CASE WHEN target:Entity THEN target.entity_id ELSE target.value_id END AS neighbor_id,
       CASE WHEN target:Entity THEN target.canonical_name ELSE coalesce(target.display, target.value) END AS neighbor_name,
       CASE WHEN target:Entity THEN target.entity_type ELSE 'value:' + target.value_type END AS neighbor_type,
       coalesce(relationship.source_quality, 0) AS relevance
UNION ALL
MATCH (source)-[relationship]->(target:Entity {team_id: $profileId})
WHERE target.entity_id IN $entityIds
  AND source.team_id = $profileId
  AND relationship.team_id = $profileId
  AND relationship.semantic_projection = true
  AND relationship.status = 'active'
  AND NOT relationship.assertion_id IN $assertionIds
RETURN target.entity_id AS from_entity_id,
       'incoming' AS direction,
       type(relationship) AS relationship_type,
       relationship.assertion_id AS assertion_id,
       relationship.tier AS tier,
       source.graph_key AS neighbor_key,
       CASE WHEN source:Entity THEN source.entity_id ELSE source.value_id END AS neighbor_id,
       CASE WHEN source:Entity THEN source.canonical_name ELSE coalesce(source.display, source.value) END AS neighbor_name,
       CASE WHEN source:Entity THEN source.entity_type ELSE 'value:' + source.value_type END AS neighbor_type,
       coalesce(relationship.source_quality, 0) AS relevance
ORDER BY relevance DESC, relationship_type ASC, assertion_id ASC
LIMIT $frontierLimit`

type AssertionGraphReader interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4jdriver.ResultSummary, []map[string]any, error)
}

type assertionSearcher struct {
	reader AssertionGraphReader
}

var _ AssertionSearcher = (*assertionSearcher)(nil)

func NewAssertionSearcher(reader AssertionGraphReader) AssertionSearcher {
	return &assertionSearcher{reader: reader}
}

func (s *assertionSearcher) SearchActive(ctx context.Context, profileID, query string, embedding []float32, limit int, validAt, knownAt *time.Time) ([]AssertionRecallResult, error) {
	if s == nil || s.reader == nil {
		return nil, errors.New("assertion search: reader is required")
	}
	searchQuery := fulltextquery.PlainText(query)
	if strings.TrimSpace(profileID) == "" || searchQuery == "" || len(embedding) == 0 {
		return []AssertionRecallResult{}, nil
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	_, rows, err := s.reader.ScopedRead(ctx, profileID, assertionSearchCypher, map[string]any{
		"candidateLimit": int64(limit * 2),
		"limit":          int64(limit),
		"embedding":      embedding,
		"searchQuery":    searchQuery,
		"validAt":        validAt,
		"knownAt":        knownAt,
	})
	if err != nil {
		return nil, err
	}
	results := make([]AssertionRecallResult, 0, len(rows))
	entityIDs := map[string]struct{}{}
	assertionIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		result, err := assertionRecallResultFromRow(profileID, row)
		if err != nil {
			return nil, err
		}
		if result.Assertion.AssertionID == "" {
			continue
		}
		results = append(results, result)
		assertionIDs = append(assertionIDs, result.Assertion.AssertionID)
		for _, node := range result.Path.Nodes {
			if !strings.HasPrefix(node.Type, "value:") && node.ID != "" {
				entityIDs[node.ID] = struct{}{}
			}
		}
	}
	if len(results) == 0 {
		return results, nil
	}
	ids := make([]string, 0, len(entityIDs))
	for id := range entityIDs {
		ids = append(ids, id)
	}
	_, frontierRows, err := s.reader.ScopedRead(ctx, profileID, assertionFrontierCypher, map[string]any{
		"entityIds":     ids,
		"assertionIds":  assertionIDs,
		"frontierLimit": int64(minInt(limit*4, 100)),
	})
	if err != nil {
		return nil, err
	}
	frontierByEntity := frontierFromRows(frontierRows)
	for i := range results {
		seen := map[string]struct{}{}
		for _, node := range results[i].Path.Nodes {
			for _, hint := range frontierByEntity[node.ID] {
				key := hint.AssertionID + ":" + hint.Direction
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				results[i].Frontier = append(results[i].Frontier, hint)
			}
		}
	}
	return results, nil
}

func assertionRecallResultFromRow(profileID string, row map[string]any) (AssertionRecallResult, error) {
	assertion := domain.Assertion{
		AssertionID:       graphrow.String(row, "assertion_id"),
		ProfileID:         profileID,
		OwnerProfileID:    graphrow.String(row, "owner_profile_id"),
		SubjectEntityID:   graphrow.String(row, "subject_entity_id"),
		PredicateKey:      graphrow.String(row, "predicate_key"),
		RelationshipType:  graphrow.String(row, "relationship_type"),
		ObjectEntityID:    graphrow.String(row, "object_entity_id"),
		Tier:              domain.AssertionTier(graphrow.String(row, "tier")),
		Status:            domain.AssertionStatus(graphrow.String(row, "status")),
		PolicyFamily:      domain.AssertionPolicyFamily(graphrow.String(row, "policy_family")),
		Polarity:          domain.ClaimPolarity(graphrow.String(row, "polarity")),
		Modality:          domain.ClaimModality(graphrow.String(row, "modality")),
		ValidFrom:         graphrow.TimePtr(row, "valid_from"),
		ValidTo:           graphrow.TimePtr(row, "valid_to"),
		RecordedTo:        graphrow.TimePtr(row, "recorded_to"),
		ExtractConf:       graphrow.Float64(row, "extract_conf"),
		ResolutionConf:    graphrow.Float64(row, "resolution_conf"),
		SourceQuality:     graphrow.Float64(row, "source_quality"),
		SupportCount:      graphrow.Int(row, "support_count"),
		SourceGroupCount:  graphrow.Int(row, "source_group_count"),
		EmbeddingModel:    graphrow.String(row, "embedding_model"),
		ExtractionModel:   graphrow.String(row, "extraction_model"),
		ExtractionVersion: graphrow.String(row, "extraction_version"),
		VerifierModel:     graphrow.String(row, "verifier_model"),
		PipelineRunID:     graphrow.String(row, "pipeline_run_id"),
		ProjectionVersion: graphrow.String(row, "projection_version"),
	}
	if value := graphrow.TimePtr(row, "recorded_at"); value != nil {
		assertion.RecordedAt = *value
	}
	if value := graphrow.TimePtr(row, "created_at"); value != nil {
		assertion.CreatedAt = *value
	}
	if value := graphrow.TimePtr(row, "updated_at"); value != nil {
		assertion.UpdatedAt = *value
	}
	if raw := graphrow.String(row, "evidence_json"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &assertion.Evidence); err != nil {
			return AssertionRecallResult{}, err
		}
	}
	objectKey := graphrow.String(row, "object_key")
	objectID := graphrow.String(row, "object_id")
	objectType := graphrow.String(row, "object_type")
	if strings.HasPrefix(objectType, "value:") {
		assertion.ObjectValue = &domain.TypedValue{
			ValueID:   objectID,
			ValueType: domain.ValueType(strings.TrimPrefix(objectType, "value:")),
			Value:     graphrow.String(row, "object_value"),
			Display:   graphrow.String(row, "object_display"),
			Unit:      graphrow.String(row, "object_unit"),
		}
	}
	subjectNode := SemanticNode{Key: "entity:" + graphrow.String(row, "subject_id"), ID: graphrow.String(row, "subject_id"), Type: graphrow.String(row, "subject_type"), Name: graphrow.String(row, "subject_name")}
	objectNode := SemanticNode{Key: objectKey, ID: objectID, Type: objectType, Name: graphrow.String(row, "object_name")}
	evidenceIDs := graphrow.StringSlice(row, "evidence_ids")
	edge := SemanticEdge{
		AssertionID:  assertion.AssertionID,
		Source:       subjectNode.Key,
		Target:       objectNode.Key,
		Relationship: assertion.RelationshipType,
		Predicate:    assertion.PredicateKey,
		Tier:         assertion.Tier,
		Status:       assertion.Status,
		Polarity:     assertion.Polarity,
		ValidFrom:    assertion.ValidFrom,
		ValidTo:      assertion.ValidTo,
		EvidenceIDs:  evidenceIDs,
	}
	return AssertionRecallResult{
		Assertion: assertion,
		Score:     graphrow.Float64(row, "score"),
		Path:      SemanticPath{Nodes: []SemanticNode{subjectNode, objectNode}, Edges: []SemanticEdge{edge}},
		Frontier:  []FrontierHint{},
	}, nil
}

func frontierFromRows(rows []map[string]any) map[string][]FrontierHint {
	out := map[string][]FrontierHint{}
	for _, row := range rows {
		from := graphrow.String(row, "from_entity_id")
		assertionID := graphrow.String(row, "assertion_id")
		if from == "" || assertionID == "" {
			continue
		}
		out[from] = append(out[from], FrontierHint{
			FromEntityID: from,
			Direction:    graphrow.String(row, "direction"),
			Relationship: graphrow.String(row, "relationship_type"),
			AssertionID:  assertionID,
			Neighbor: SemanticNode{
				Key:  graphrow.String(row, "neighbor_key"),
				ID:   graphrow.String(row, "neighbor_id"),
				Type: graphrow.String(row, "neighbor_type"),
				Name: graphrow.String(row, "neighbor_name"),
			},
			Tier: domain.AssertionTier(graphrow.String(row, "tier")),
		})
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
