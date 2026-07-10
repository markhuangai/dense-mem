package registry

import (
	"context"
	"errors"
	"fmt"
)

func evalListAssertions(ctx context.Context, deps Dependencies, profileID string, input map[string]any, limit int, metadataOnly bool) (map[string]any, error) {
	if deps.GraphQuery == nil {
		return nil, ErrToolUnavailable
	}
	status, _ := input["status"].(string)
	where := ""
	params := map[string]any{}
	if status != "" {
		where = "\nWHERE assertion.status = $status"
		params["status"] = status
	}
	query := fmt.Sprintf(`
MATCH (assertion:Assertion {team_id: $profileId})%s
RETURN assertion.assertion_id AS assertion_id,
       assertion.subject_entity_id AS subject_entity_id,
       assertion.predicate_key AS predicate_key,
       assertion.relationship_type AS relationship_type,
       assertion.object_entity_id AS object_entity_id,
       assertion.object_value_id AS object_value_id,
       assertion.tier AS tier,
       assertion.status AS status,
       assertion.policy_family AS policy_family,
       assertion.polarity AS polarity,
       assertion.modality AS modality,
       assertion.valid_from AS valid_from,
       assertion.valid_to AS valid_to,
       assertion.recorded_at AS recorded_at,
       assertion.recorded_to AS recorded_to,
       assertion.support_count AS support_count,
       assertion.source_group_count AS source_group_count,
       assertion.search_text AS search_text,
       assertion.evidence_json AS evidence_json
ORDER BY assertion.recorded_at DESC, assertion.assertion_id ASC
LIMIT %d`, where, limit)
	res, err := deps.GraphQuery.Execute(ctx, profileID, query, params)
	if err != nil {
		return nil, err
	}
	items := res.Rows
	if metadataOnly {
		for i := range items {
			stripEvalContent("assertion", items[i])
		}
	}
	return map[string]any{"items": items, "next_cursor": "", "has_more": false}, nil
}

func evalGetAssertion(ctx context.Context, deps Dependencies, profileID, assertionID string) (map[string]any, error) {
	if deps.GraphQuery == nil {
		return nil, ErrToolUnavailable
	}
	query := `
MATCH (assertion:Assertion {team_id: $profileId, assertion_id: $assertionId})
RETURN assertion.assertion_id AS assertion_id,
       assertion.subject_entity_id AS subject_entity_id,
       assertion.predicate_key AS predicate_key,
       assertion.relationship_type AS relationship_type,
       assertion.object_entity_id AS object_entity_id,
       assertion.object_value_id AS object_value_id,
       assertion.tier AS tier,
       assertion.status AS status,
       assertion.policy_family AS policy_family,
       assertion.polarity AS polarity,
       assertion.modality AS modality,
       assertion.valid_from AS valid_from,
       assertion.valid_to AS valid_to,
       assertion.recorded_at AS recorded_at,
       assertion.recorded_to AS recorded_to,
       assertion.support_count AS support_count,
       assertion.source_group_count AS source_group_count,
       assertion.search_text AS search_text,
       assertion.evidence_json AS evidence_json
LIMIT 1`
	res, err := deps.GraphQuery.Execute(ctx, profileID, query, map[string]any{"assertionId": assertionID})
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, errors.New("eval_get_knowledge_item: assertion not found")
	}
	return res.Rows[0], nil
}
