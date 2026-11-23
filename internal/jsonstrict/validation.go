package jsonstrict

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

const MaxCorrectionIssues = 100

// Issue is a bounded field-level contract validation error.
type Issue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Code    string `json:"-"`
}

func (e Issue) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

type ValidationError struct {
	Errors []Issue
	Stage  string
	Cause  error `json:"-"`
}

func (e *ValidationError) Error() string {
	return joinIssues(e.Errors)
}

func (e *ValidationError) Unwrap() error { return e.Cause }

func newIssue(field, message string) Issue {
	return Issue{Field: field, Message: message}
}

// ValidateResponseArrayObjects applies the shared closed-object checks to every indexed array entry.
func ValidateResponseArrayObjects(raw json.RawMessage, path string, fields []string, nullable, optional map[string]bool) []Issue {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return []Issue{newIssue(path, "must be an array")}
	}
	var errs []Issue
	for i, value := range values {
		_, itemErrs := ValidateResponseObject(value, fmt.Sprintf("%s[%d]", path, i), fields, nullable, optional)
		errs = append(errs, itemErrs...)
	}
	return errs
}

// ValidateResponseObject checks duplicate, unknown, required and nullable fields without coercing values.
func ValidateResponseObject(raw json.RawMessage, path string, fields []string, nullable, optional map[string]bool) (map[string]json.RawMessage, []Issue) {
	if err := RejectDuplicateFields(raw); err != nil {
		return nil, []Issue{newIssue(fieldPath(path, ""), err.Error())}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, []Issue{newIssue(fieldPath(path, ""), "must be an object")}
	}
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	var errs []Issue
	for _, field := range fields {
		value, ok := object[field]
		if !ok {
			if optional[field] {
				continue
			}
			errs = append(errs, newIssue(fieldPath(path, field), "is required"))
			continue
		}
		if !nullable[field] && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			errs = append(errs, newIssue(fieldPath(path, field), "must not be null"))
		}
	}
	unknown := make([]string, 0)
	for field := range object {
		if _, ok := allowed[field]; !ok {
			unknown = append(unknown, field)
		}
	}
	sort.Strings(unknown)
	for _, field := range unknown {
		errs = append(errs, newIssue(fieldPath(path, field), "is unknown"))
	}
	return object, errs
}

func fieldPath(path string, field string) string {
	if path == "" {
		return field
	}
	if field == "" {
		return path
	}
	return path + "." + field
}

// ResponseJSONValidationError retains the shared provider-facing JSON type error contract.
func ResponseJSONValidationError(err error, path string) Issue {
	field := path
	if field == "" {
		field = "response"
	}
	message := "must be one complete JSON object matching the required field types"
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) && (typeError.Field != "" || path != "") {
		if typeError.Field != "" {
			field = fieldPath(path, typeError.Field)
		}
		message = "must match the required JSON type"
	}
	return newIssue(field, message)
}

func joinIssues(errs []Issue) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}

// BoundIssues keeps distinct repair feedback deterministic and within the shared error-count bound.
func BoundIssues(errs []Issue) []Issue {
	bounded := append([]Issue(nil), errs...)
	sort.Slice(bounded, func(i, j int) bool {
		if bounded[i].Field == bounded[j].Field {
			if bounded[i].Message == bounded[j].Message {
				return bounded[i].Code < bounded[j].Code
			}
			return bounded[i].Message < bounded[j].Message
		}
		return bounded[i].Field < bounded[j].Field
	})
	bounded = slices.CompactFunc(bounded, func(left, right Issue) bool {
		return left.Field == right.Field && left.Message == right.Message
	})
	if len(bounded) <= MaxCorrectionIssues {
		return bounded
	}
	bounded = bounded[:MaxCorrectionIssues-1]
	return append(bounded, Issue{
		Field:   "response",
		Message: "additional validation errors were omitted; return one complete response matching the schema",
	})
}
