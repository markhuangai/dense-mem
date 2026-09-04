package verifier

import (
	"fmt"
	"testing"
)

func assertClosedProviderObjects(t *testing.T, schema map[string]any, path string) {
	t.Helper()
	if schema["type"] == "object" {
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			t.Errorf("%s is not closed", path)
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range props {
			if child, ok := raw.(map[string]any); ok {
				assertClosedProviderObjects(t, child, path+"."+name)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		assertClosedProviderObjects(t, items, path+"[]")
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

func assessmentMessageRoles(messages []openAIVerifierMessage) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	return roles
}
