package validation

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-playground/validator/v10"
)

// Validator is the global validator instance with custom validators registered.
var Validator *validator.Validate

func init() {
	Validator = validator.New()

	// Register custom validators
	Validator.RegisterValidation("notblank", notBlankValidator)
	Validator.RegisterValidation("maxbytes", maxBytesValidator)
}

// notBlankValidator validates that a string is not blank (not empty and not only whitespace).
func notBlankValidator(fl validator.FieldLevel) bool {
	field := fl.Field()

	if field.Kind() != reflect.String {
		return true // Only applies to strings
	}

	value := field.String()
	return strings.TrimSpace(value) != ""
}

// maxBytesValidator validates that the serialized JSON size of a field is within the limit.
// The parameter is the maximum number of bytes allowed.
func maxBytesValidator(fl validator.FieldLevel) bool {
	field := fl.Field()

	// Only apply to maps and slices
	if field.Kind() != reflect.Map && field.Kind() != reflect.Slice && field.Kind() != reflect.Array {
		return true
	}

	// Get the max bytes from the tag parameter
	maxBytes := fl.Param()
	if maxBytes == "" {
		return true // No limit specified
	}

	max, err := strconv.ParseInt(maxBytes, 10, 64)
	if err != nil || max < 0 {
		return false
	}

	// Serialize to JSON to check size
	data, err := json.Marshal(field.Interface())
	if err != nil {
		return false // Cannot serialize, fail validation
	}

	return int64(len(data)) <= max
}

// ValidateStruct validates a struct using the registered validators.
func ValidateStruct(s interface{}) error {
	return Validator.Struct(s)
}

// ValidateVar validates a single variable using the given tag.
func ValidateVar(field interface{}, tag string) error {
	return Validator.Var(field, tag)
}
