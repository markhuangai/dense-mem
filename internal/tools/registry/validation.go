package registry

import (
	"fmt"
	"math"
	"reflect"
	"sort"
)

func ValidateInput(tool Tool, args map[string]any) error {
	schema := tool.InputSchema
	if len(schema) == 0 {
		return nil
	}

	for _, name := range schemaRequiredFields(schema) {
		if _, ok := args[name]; !ok {
			return fmt.Errorf("%s is required", name)
		}
	}

	properties := schemaProperties(schema)
	if schemaDisallowsAdditionalProperties(schema) {
		for key := range args {
			if _, ok := properties[key]; !ok {
				return fmt.Errorf("unknown field: %s", key)
			}
		}
	}

	for name, value := range args {
		prop, ok := properties[name]
		if !ok {
			continue
		}
		if err := validateSchemaValue(name, value, prop); err != nil {
			return err
		}
	}
	return nil
}

func validateSchemaValue(name string, value any, schema map[string]any) error {
	expected, _ := schema["type"].(string)
	switch expected {
	case "string":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", name)
		}
		if maxLen, ok := schemaNumber(schema["maxLength"]); ok && len(s) > int(maxLen) {
			return fmt.Errorf("%s exceeds maximum length of %d", name, int(maxLen))
		}
	case "integer":
		number, ok := schemaNumber(value)
		if !ok || math.Trunc(number) != number {
			return fmt.Errorf("%s must be an integer", name)
		}
		if min, ok := schemaNumber(schema["minimum"]); ok && number < min {
			return fmt.Errorf("%s must be greater than or equal to %d", name, int(min))
		}
		if max, ok := schemaNumber(schema["maximum"]); ok && number > max {
			return fmt.Errorf("%s must be less than or equal to %d", name, int(max))
		}
	case "number":
		number, ok := schemaNumber(value)
		if !ok {
			return fmt.Errorf("%s must be a number", name)
		}
		if min, ok := schemaNumber(schema["minimum"]); ok && number < min {
			return fmt.Errorf("%s must be greater than or equal to %g", name, min)
		}
		if max, ok := schemaNumber(schema["maximum"]); ok && number > max {
			return fmt.Errorf("%s must be less than or equal to %g", name, max)
		}
	case "object":
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() || reflected.Kind() != reflect.Map {
			return fmt.Errorf("%s must be an object", name)
		}
	case "array":
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) {
			return fmt.Errorf("%s must be an array", name)
		}
		if maxItems, ok := schemaNumber(schema["maxItems"]); ok && reflected.Len() > int(maxItems) {
			return fmt.Errorf("%s exceeds maximum item count of %d", name, int(maxItems))
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", name)
		}
	}
	return nil
}

func schemaNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case float32:
		return float64(v), true
	default:
		return 0, false
	}
}

func schemaRequiredFields(schema map[string]any) []string {
	raw, ok := schema["required"]
	if !ok {
		return nil
	}

	switch v := raw.(type) {
	case []string:
		required := append([]string(nil), v...)
		sort.Strings(required)
		return required
	case []any:
		required := make([]string, 0, len(v))
		for _, item := range v {
			if name, ok := item.(string); ok && name != "" {
				required = append(required, name)
			}
		}
		sort.Strings(required)
		return required
	default:
		return nil
	}
}

func schemaDisallowsAdditionalProperties(schema map[string]any) bool {
	v, ok := schema["additionalProperties"].(bool)
	return ok && !v
}

func schemaProperties(schema map[string]any) map[string]map[string]any {
	props := make(map[string]map[string]any)
	raw, ok := schema["properties"]
	if !ok {
		return props
	}

	switch v := raw.(type) {
	case map[string]any:
		for key, rawProp := range v {
			if prop, ok := rawProp.(map[string]any); ok {
				props[key] = prop
			}
		}
	}
	return props
}
