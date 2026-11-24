package assessment

import (
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/assessor"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
)

func validationIssue(field, code, message string, args ...any) assessor.SemanticValidationError {
	return assessor.SemanticValidationError{Field: field, Message: fmt.Sprintf(message, args...), Code: code}
}

func validationResult(errs []assessor.SemanticValidationError, cause error) error {
	if len(errs) == 0 {
		return nil
	}
	return &assessor.SemanticAssessmentResponseValidationError{Errors: errs, Cause: cause}
}

func validationDiagnostic(stage string, err error, request Request, response Response) *topic.AssessmentValidation {
	var rejected *assessor.SemanticAssessmentResponseValidationError
	if !errors.As(err, &rejected) || len(rejected.Errors) == 0 {
		return nil
	}
	issue := rejected.Errors[0]
	family := issue.Field
	if stage == "decode" && issue.Code == "strict_json" {
		family = "response"
	} else {
		if start := strings.IndexByte(family, '['); start >= 0 {
			end := strings.IndexByte(family[start:], ']')
			if end >= 0 {
				family = family[:start] + family[start+end+1:]
			}
		}
		if stage == "validate" {
			family, _, _ = strings.Cut(family, ".")
		}
	}
	diagnostic := &topic.AssessmentValidation{Stage: stage, Code: issue.Code, FieldFamily: family}
	var expected, actual int
	switch issue.Code {
	case "definition_limit":
		expected, actual = topic.MaxVocabularyCandidates, len(response.Definitions)
	case "item_count_mismatch":
		expected, actual = len(request.Items), len(response.Items)
	case "equivalence_coverage":
		expected, actual = len(request.Pairs), len(response.Equivalence)
	default:
		return diagnostic
	}
	diagnostic.ExpectedCount, diagnostic.ActualCount = &expected, &actual
	return diagnostic
}
