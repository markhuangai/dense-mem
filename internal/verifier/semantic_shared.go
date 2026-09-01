package verifier

import (
	"errors"
	"strings"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

// SemanticReviewEvidence is the assessor-owned immutable evidence envelope.
type SemanticReviewEvidence = assessor.SemanticReviewEvidence

// RelationshipCorrectionTarget carries server-trusted correction context
// through the in-process assessor request without exposing it to providers.
type RelationshipCorrectionTarget struct {
	RelationshipID  string
	ExpectedVersion int
}

// RelationshipConflictContext carries server-trusted conflict context through
// the in-process assessor request without exposing it to providers.
type RelationshipConflictContext struct {
	ConflictID      string
	ExpectedVersion int
}

// SemanticValueObservation is the typed-value shape shared by provider
// proposals and the closed submission contract.
type SemanticValueObservation struct {
	Ref     string `json:"ref"`
	Type    string `json:"type"`
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

// SemanticValidationError is the assessor-owned bounded field-level contract
// validation error kept as a verifier compatibility alias.
type SemanticValidationError = assessor.SemanticValidationError

// SemanticEvidenceSpan resolves rune-based evidence offsets and is used after
// provider boundary references have been converted to canonical offsets.
func SemanticEvidenceSpan(content string, start int, end int) (string, error) {
	runes := []rune(content)
	if start < 0 || end <= start || end > len(runes) {
		return "", errors.New("span is invalid")
	}
	return string(runes[start:end]), nil
}

func semanticExactSpanQuote(content string, start int, end int, quote string) (string, error) {
	exact, err := SemanticEvidenceSpan(content, start, end)
	if err != nil {
		return "", err
	}
	quote = strings.TrimSpace(quote)
	if quote == "" || quote == exact || semanticWhitespaceEquivalent(exact, quote) {
		return exact, nil
	}
	return "", errors.New("quote does not match the original evidence span")
}

func semanticWhitespaceEquivalent(a string, b string) bool {
	return strings.Join(strings.FieldsFunc(a, unicode.IsSpace), " ") == strings.Join(strings.FieldsFunc(b, unicode.IsSpace), " ")
}

func semanticOneOf(value string, allowed ...string) bool {
	value = strings.TrimSpace(value)
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func semanticErr(field string, message string) SemanticValidationError {
	return SemanticValidationError{Field: field, Message: message}
}
