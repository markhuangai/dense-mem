package registry

import (
	"errors"
	"fmt"
)

func assertProviderProposalSchema(schema map[string]any) error {
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
	if _, ok := props["evidence"]; ok {
		return errors.New("provider proposal schema must not require immutable evidence echo")
	}
	relationshipProposals, ok := props["relationship_proposals"]
	if !ok {
		return errors.New("provider proposal schema has no relationship_proposals")
	}
	items, ok := relationshipProposals["items"].(map[string]any)
	if !ok {
		return errors.New("provider relationship_proposals schema has no item schema")
	}
	for _, unsupported := range []string{"oneOf", "allOf", "not", "if", "then", "else"} {
		if _, ok := items[unsupported]; ok {
			return fmt.Errorf("provider relationship proposal schema uses unsupported structured-output keyword %s", unsupported)
		}
	}
	itemProps := schemaProperties(items)
	objectRef, ok := itemProps["object_ref"]
	if !ok || schemaAllowsNull(objectRef) {
		return errors.New("provider relationship proposal schema must expose string object_ref")
	}
	objectValue, ok := itemProps["object_value"]
	if !ok || !schemaAllowsNull(objectValue) {
		return errors.New("provider relationship proposal schema must expose nullable object_value")
	}
	return nil
}

func assertVerifierResponseSchema(schema map[string]any) error {
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

func schemaAllowsNull(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	if values, ok := schema["type"].([]any); ok {
		for _, value := range values {
			if value == nil || value == "null" {
				return true
			}
		}
	}
	if values, ok := schema["type"].([]string); ok {
		for _, value := range values {
			if value == "null" {
				return true
			}
		}
	}
	if variants, ok := schema["anyOf"].([]any); ok {
		for _, raw := range variants {
			if child, ok := raw.(map[string]any); ok && child["type"] == "null" {
				return true
			}
		}
	}
	return false
}
