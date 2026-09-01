package verifier

import (
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

var semanticHiddenControlMarkupPattern = regexp.MustCompile(`(?is)<!--|<\s*(?:script|iframe|object|embed|meta|svg)\b|\bon[a-z]{3,32}\s*=`)

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

func semanticEvidenceByID(evidence []SemanticReviewEvidence) map[string]SemanticReviewEvidence {
	out := make(map[string]SemanticReviewEvidence, len(evidence))
	for _, item := range evidence {
		out[strings.TrimSpace(item.EvidenceID)] = item
	}
	return out
}

func semanticSecuritySignalSpanMatchesKind(kind, quote string) bool {
	if strings.TrimSpace(kind) != "hidden_control_markup" {
		return true
	}
	if semanticHiddenControlMarkupPattern.MatchString(quote) {
		return true
	}
	for _, value := range quote {
		if semanticHiddenControlRune(value) {
			return true
		}
	}
	return false
}

func semanticHiddenControlRune(value rune) bool {
	return value == '\u200b' || value == '\u200c' || value == '\u200d' || value == '\u200e' || value == '\u200f' ||
		value == '\u2060' || value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069' ||
		unicode.IsControl(value) && value != '\n' && value != '\r' && value != '\t'
}

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

func semanticKindAllowed(kind string, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.TrimSpace(candidate) == kind {
			return true
		}
	}
	return false
}

func semanticSecurityKindAllowed(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "role_control_spoofing", "instruction_override", "prompt_secret_extraction", "tool_exfiltration", "obfuscated_instruction", "hidden_control_markup":
		return true
	default:
		return false
	}
}

func semanticSecurityKinds() []string {
	return []string{
		"role_control_spoofing",
		"instruction_override",
		"prompt_secret_extraction",
		"tool_exfiltration",
		"obfuscated_instruction",
		"hidden_control_markup",
	}
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
