package registry

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"unicode/utf8"
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
		charLen := utf8.RuneCountInString(s)
		if minLen, ok := schemaNumber(schema["minLength"]); ok && charLen < int(minLen) {
			return fmt.Errorf("%s must be at least %d characters", name, int(minLen))
		}
		if maxLen, ok := schemaNumber(schema["maxLength"]); ok && charLen > int(maxLen) {
			message := fmt.Sprintf("%s exceeds maximum length of %d", name, int(maxLen))
			if hint, ok := schema["x-validation-hint"].(string); ok && hint != "" {
				message = message + ": " + hint
			}
			return fmt.Errorf("%s", message)
		}
	case "integer":
		number, ok := schemaNumber(value)
		if !ok || !isFiniteNumber(number) || math.Trunc(number) != number {
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
		if !ok || !isFiniteNumber(number) {
			return fmt.Errorf("%s must be a number", name)
		}
		if min, ok := schemaNumber(schema["minimum"]); ok && number < min {
			return fmt.Errorf("%s must be greater than or equal to %g", name, min)
		}
		if max, ok := schemaNumber(schema["maximum"]); ok && number > max {
			return fmt.Errorf("%s must be less than or equal to %g", name, max)
		}
	case "object":
		fields, ok := objectFields(value)
		if !ok {
			return fmt.Errorf("%s must be an object", name)
		}
		for _, fieldName := range schemaRequiredFields(schema) {
			if _, ok := fields[fieldName]; !ok {
				return fmt.Errorf("%s.%s is required", name, fieldName)
			}
		}
		properties := schemaProperties(schema)
		if schemaDisallowsAdditionalProperties(schema) {
			for key := range fields {
				if _, ok := properties[key]; !ok {
					return fmt.Errorf("%s.%s is not allowed", name, key)
				}
			}
		}
		for key, fieldValue := range fields {
			prop, ok := properties[key]
			if !ok {
				continue
			}
			if err := validateSchemaValue(name+"."+key, fieldValue, prop); err != nil {
				return err
			}
		}
	case "array":
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) {
			return fmt.Errorf("%s must be an array", name)
		}
		if minItems, ok := schemaNumber(schema["minItems"]); ok && reflected.Len() < int(minItems) {
			return fmt.Errorf("%s must contain at least %d items", name, int(minItems))
		}
		if maxItems, ok := schemaNumber(schema["maxItems"]); ok && reflected.Len() > int(maxItems) {
			return fmt.Errorf("%s exceeds maximum item count of %d", name, int(maxItems))
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i := 0; i < reflected.Len(); i++ {
				if err := validateSchemaValue(fmt.Sprintf("%s[%d]", name, i), reflected.Index(i).Interface(), itemSchema); err != nil {
					return err
				}
			}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", name)
		}
	}
	if err := validateSchemaEnum(name, value, schema); err != nil {
		return err
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
	case json.Number:
		number, err := v.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

func isFiniteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func objectFields(value any) (map[string]any, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Map || reflected.Type().Key().Kind() != reflect.String {
		return nil, false
	}

	fields := make(map[string]any, reflected.Len())
	iter := reflected.MapRange()
	for iter.Next() {
		fields[iter.Key().String()] = iter.Value().Interface()
	}
	return fields, true
}

func validateSchemaEnum(name string, value any, schema map[string]any) error {
	raw, ok := schema["enum"]
	if !ok {
		return nil
	}

	switch values := raw.(type) {
	case []string:
		s, ok := value.(string)
		if !ok {
			return nil
		}
		for _, allowed := range values {
			if s == allowed {
				return nil
			}
		}
	case []any:
		for _, allowed := range values {
			if schemaValuesEqual(value, allowed) {
				return nil
			}
		}
	default:
		return nil
	}

	return fmt.Errorf("%s must be one of %s", name, schemaEnumDescription(raw))
}

func schemaValuesEqual(left, right any) bool {
	leftNumber, leftOK := schemaNumber(left)
	rightNumber, rightOK := schemaNumber(right)
	if leftOK && rightOK {
		return leftNumber == rightNumber
	}
	return reflect.DeepEqual(left, right)
}

func schemaEnumDescription(raw any) string {
	switch values := raw.(type) {
	case []string:
		return fmt.Sprintf("%v", values)
	case []any:
		return fmt.Sprintf("%v", values)
	default:
		return "the allowed values"
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
