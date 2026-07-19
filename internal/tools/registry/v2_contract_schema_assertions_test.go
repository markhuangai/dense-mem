package registry

import (
	"errors"
	"fmt"
)

func assertV2ProviderProposalSchema(schema map[string]any) error {
	props := schemaProperties(schema)
	if len(props) == 0 {
		return errors.New("provider proposal schema has no properties")
	}
	for _, forbidden := range []string{"team_id", "profile_id", "tier", "status", "predicate_definitions"} {
		if _, ok := props[forbidden]; ok {
			return fmt.Errorf("provider proposal schema allows %s", forbidden)
		}
	}
	if _, ok := props["predicate_options"]; !ok {
		return errors.New("provider proposal schema has no predicate_options")
	}
	relationshipProposals, ok := props["relationship_proposals"]
	if !ok {
		return errors.New("provider proposal schema has no relationship_proposals")
	}
	items, ok := relationshipProposals["items"].(map[string]any)
	if !ok {
		return errors.New("provider relationship_proposals schema has no item schema")
	}
	oneOf, ok := items["oneOf"].([]any)
	if !ok || len(oneOf) != 2 {
		return errors.New("provider relationship proposal schema must require exactly one object form")
	}
	return nil
}

func assertV2VerifierResponseSchema(schema map[string]any) error {
	if !schemaDisallowsAdditionalProperties(schema) {
		return errors.New("verifier response schema is not closed")
	}
	props := schemaProperties(schema)
	for _, required := range []string{"request_id", "security_signals", "entity_results", "relationship_results"} {
		if _, ok := props[required]; !ok {
			return fmt.Errorf("verifier response schema has no %s", required)
		}
	}
	for _, forbidden := range []string{"tier", "status", "support_count", "predicate_definitions"} {
		if _, ok := props[forbidden]; ok {
			return fmt.Errorf("verifier response schema allows %s", forbidden)
		}
	}
	return nil
}
