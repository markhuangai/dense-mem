package verifier

import (
	"fmt"
	"reflect"
	"testing"
)

func TestV2ProviderProposalSchemaIsClosedAndCanonical(t *testing.T) {
	schema := V2ProviderProposalSchema()
	assertClosedV2ProviderObjects(t, schema, "proposal")
	props := schemaPropertiesForTest(t, schema)
	for _, field := range []string{"entity_proposals", "relationship_proposals", "predicate_options"} {
		if _, ok := props[field]; !ok {
			t.Fatalf("provider proposal schema missing %s", field)
		}
	}
	if _, ok := props["evidence"]; ok {
		t.Fatal("provider proposal schema should not require immutable evidence echo")
	}
	predicateOptions, ok := props["predicate_options"].(map[string]any)
	if !ok {
		t.Fatalf("predicate_options schema = %#v", props["predicate_options"])
	}
	predicateOptionItems, ok := predicateOptions["items"].(map[string]any)
	if !ok || predicateOptionItems["type"] != "string" {
		t.Fatalf("predicate_options items = %#v", predicateOptions["items"])
	}
	relationships := itemSchemaForTest(t, props["relationship_proposals"])
	relProps := schemaPropertiesForTest(t, relationships)
	for _, field := range []string{"proposal_id", "subject_ref", "original_predicate", "evidence"} {
		if _, ok := relProps[field]; !ok {
			t.Fatalf("provider relationship proposal missing %s", field)
		}
	}
	if _, ok := relProps["ref"]; ok {
		t.Fatal("provider relationship proposal exposes legacy ref field")
	}
	span := itemSchemaForTest(t, relProps["evidence"])
	spanProps := schemaPropertiesForTest(t, span)
	for _, field := range []string{"evidence_index", "start", "end"} {
		if _, ok := spanProps[field]; !ok {
			t.Fatalf("provider evidence span missing %s", field)
		}
	}
}

func TestV2VerifierResponseSchemaMatchesStrictWikiContract(t *testing.T) {
	schema := V2VerifierResponseSchema()
	assertClosedV2ProviderObjects(t, schema, "verifier response")
	wantRequired := []string{"request_id", "security_signals", "entity_results", "relationship_results"}
	if got := schema["required"]; !reflect.DeepEqual(got, wantRequired) {
		t.Fatalf("required = %#v, want %#v", got, wantRequired)
	}
	props := schemaPropertiesForTest(t, schema)
	entities := schemaPropertiesForTest(t, itemSchemaForTest(t, props["entity_results"]))
	assertEnumForTest(t, entities["action"], []string{"reuse", "create", "ambiguous"})
	relationships := schemaPropertiesForTest(t, itemSchemaForTest(t, props["relationship_results"]))
	assertEnumForTest(t, relationships["predicate_status"], []string{"resolved", "needs_review"})
	assertEnumForTest(t, relationships["evidence_verdict"], []string{"entailed", "contradicted", "insufficient"})
	signals := schemaPropertiesForTest(t, itemSchemaForTest(t, props["security_signals"]))
	assertEnumForTest(t, signals["kind"], []string{
		"role_control_spoofing",
		"instruction_override",
		"prompt_secret_extraction",
		"tool_exfiltration",
		"obfuscated_instruction",
		"hidden_control_markup",
	})
}

func TestPredicateOptionArraySchemaStaysClosed(t *testing.T) {
	schema := predicateOptionArraySchema()
	items := itemSchemaForTest(t, schema)
	props := schemaPropertiesForTest(t, items)
	for _, field := range []string{"predicate_key", "aliases", "allowed_subject_kinds", "allowed_object_kinds", "relationship_kind", "current_cardinality", "version"} {
		if _, ok := props[field]; !ok {
			t.Fatalf("predicate option schema missing %s", field)
		}
	}
	assertEnumForTest(t, props["relationship_kind"], []string{"state", "event"})
	assertEnumForTest(t, props["current_cardinality"], []string{"one", "many"})
	assertClosedV2ProviderObjects(t, schema, "predicate options")
}

func TestV2StructuredOutputSchemasUseOpenAISupportedStrictSubset(t *testing.T) {
	for name, schema := range map[string]map[string]any{
		V2ProviderProposalSchemaName: V2ProviderProposalSchema(),
		V2VerifierResponseSchemaName: V2VerifierResponseSchema(),
	} {
		assertOpenAIStrictSchemaSubset(t, schema, name)
	}
}

func assertClosedV2ProviderObjects(t *testing.T, schema map[string]any, path string) {
	t.Helper()
	if schema["type"] == "object" {
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			t.Errorf("%s is not closed", path)
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range props {
			if child, ok := raw.(map[string]any); ok {
				assertClosedV2ProviderObjects(t, child, path+"."+name)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		assertClosedV2ProviderObjects(t, items, path+"[]")
	}
}

func assertOpenAIStrictSchemaSubset(t *testing.T, schema map[string]any, path string) {
	t.Helper()
	for _, keyword := range []string{"$schema", "maxProperties", "oneOf", "allOf", "not", "if", "then", "else", "dependentRequired", "dependentSchemas", "patternProperties"} {
		if _, ok := schema[keyword]; ok {
			t.Fatalf("%s uses unsupported OpenAI structured-output keyword %q", path, keyword)
		}
	}
	if schema["type"] == "object" {
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("%s must set additionalProperties:false", path)
		}
		props := map[string]any{}
		if raw, ok := schema["properties"].(map[string]any); ok {
			props = raw
		}
		requiredRaw, ok := schema["required"].([]string)
		if !ok {
			t.Fatalf("%s must list required properties", path)
		}
		required := map[string]struct{}{}
		for _, field := range requiredRaw {
			required[field] = struct{}{}
		}
		for field := range props {
			if _, ok := required[field]; !ok {
				t.Fatalf("%s.%s must be required for OpenAI strict structured outputs", path, field)
			}
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range props {
			if child, ok := raw.(map[string]any); ok {
				assertOpenAIStrictSchemaSubset(t, child, path+"."+name)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		assertOpenAIStrictSchemaSubset(t, items, path+"[]")
	}
	if variants, ok := schema["anyOf"].([]any); ok {
		for i, raw := range variants {
			if child, ok := raw.(map[string]any); ok {
				assertOpenAIStrictSchemaSubset(t, child, fmt.Sprintf("%s.anyOf[%d]", path, i))
			}
		}
	}
}

func schemaPropertiesForTest(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %#v", schema)
	}
	return props
}

func itemSchemaForTest(t *testing.T, schema any) map[string]any {
	t.Helper()
	field, ok := schema.(map[string]any)
	if !ok {
		t.Fatalf("field is not a schema: %#v", schema)
	}
	items, ok := field["items"].(map[string]any)
	if !ok {
		t.Fatalf("field has no item schema: %#v", field)
	}
	return items
}

func assertEnumForTest(t *testing.T, schema any, want []string) {
	t.Helper()
	field, ok := schema.(map[string]any)
	if !ok {
		t.Fatalf("enum field is not a schema: %#v", schema)
	}
	if got := field["enum"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("enum = %#v, want %#v", got, want)
	}
}
