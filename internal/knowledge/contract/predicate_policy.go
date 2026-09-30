package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func NormalizeEnsureSemanticPredicateCandidateInput(input EnsureSemanticPredicateCandidateInput) EnsureSemanticPredicateCandidateInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Predicate = strings.TrimSpace(input.Predicate)
	input.RelationshipKind = strings.TrimSpace(input.RelationshipKind)
	input.SubjectKind = strings.TrimSpace(input.SubjectKind)
	input.ObjectKind = strings.TrimSpace(input.ObjectKind)
	input.Origin = strings.TrimSpace(input.Origin)
	if input.Origin == "" {
		input.Origin = "provider_generated"
	}
	if input.SubjectKind == "" {
		input.SubjectKind = string(domain.EntityKindOther)
	}
	if input.ObjectKind == "" {
		input.ObjectKind = string(domain.EntityKindOther)
	}
	return input
}

func ValidateEnsureSemanticPredicateCandidateInput(input EnsureSemanticPredicateCandidateInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.Predicate == "" {
		return errors.New("predicate is required")
	}
	if !slices.Contains(domain.RelationshipKinds(), input.RelationshipKind) {
		return fmt.Errorf("relationship_kind is unsupported %q", input.RelationshipKind)
	}
	if !slices.Contains(domain.EntityKinds(), input.SubjectKind) {
		return fmt.Errorf("subject_kind is unsupported %q", input.SubjectKind)
	}
	if !slices.Contains(append(domain.EntityKinds(), domain.ValueTypes()...), input.ObjectKind) {
		return fmt.Errorf("object_kind is unsupported %q", input.ObjectKind)
	}
	return nil
}

func NormalizeSubmissionPredicateRegistration(input SubmissionPredicateRegistrationInput) SubmissionPredicateRegistrationInput {
	input.PredicateKey = strings.TrimSpace(input.PredicateKey)
	input.SubjectKind = strings.TrimSpace(input.SubjectKind)
	input.ObjectKind = strings.TrimSpace(input.ObjectKind)
	input.RelationshipKind = strings.TrimSpace(input.RelationshipKind)
	input.CurrentCardinality = strings.TrimSpace(input.CurrentCardinality)
	return input
}

func ValidateSubmissionPredicateRegistrationFields(index int, registration SubmissionPredicateRegistrationInput) []SubmissionPredicateRegistrationIssue {
	var issues []SubmissionPredicateRegistrationIssue
	add := func(field, message string) {
		issues = append(issues, SubmissionPredicateRegistrationIssue{index, field, message})
	}
	if registration.PredicateKey == "" || len([]rune(registration.PredicateKey)) > 128 {
		add("predicate_key", "is required and must be bounded")
	}
	if !slices.Contains(domain.EntityKinds(), registration.SubjectKind) {
		add("subject_kind", "is unsupported")
	}
	if !slices.Contains(append(domain.EntityKinds(), domain.ValueTypes()...), registration.ObjectKind) {
		add("object_kind", "is unsupported")
	}
	if !slices.Contains(domain.RelationshipKinds(), registration.RelationshipKind) {
		add("relationship_kind", "is unsupported")
	}
	if !slices.Contains(domain.CurrentCardinalities(), registration.CurrentCardinality) {
		add("current_cardinality", "is unsupported")
	}
	return issues
}

func ValidateCommitSubmissionPredicateRegistration(registration SubmissionPredicateRegistrationInput) error {
	if registration.RelationshipRef == "" || registration.PredicateKey == "" {
		return errors.New("submission predicate registration ref and key are required")
	}
	for _, issue := range ValidateSubmissionPredicateRegistrationFields(0, registration) {
		switch issue.Field {
		case "predicate_key":
			return errors.New("submission predicate registration key must be at most 128 characters")
		case "subject_kind", "object_kind":
			return errors.New("submission predicate registration endpoint kinds are unsupported")
		case "relationship_kind", "current_cardinality":
			return errors.New("submission predicate registration policy is unsupported")
		}
	}
	return nil
}

func SubmissionPredicateRegistrationCompatibility(
	loaded SemanticReviewPredicateCandidate,
	registration SubmissionPredicateRegistrationInput,
) (string, string) {
	if loaded.LifecycleState != string(domain.PredicateLifecycleActive) {
		return "predicate_key", "resolves to a predicate that is not active"
	}
	if !SemanticPredicateKindAllowed(loaded.AllowedSubjectKinds, registration.SubjectKind) {
		return "subject_kind", "is incompatible with the existing predicate"
	}
	if !SemanticPredicateKindAllowed(loaded.AllowedObjectKinds, registration.ObjectKind) {
		return "object_kind", "is incompatible with the existing predicate"
	}
	if loaded.RelationshipKind != registration.RelationshipKind {
		return "relationship_kind", "is incompatible with the existing predicate"
	}
	if loaded.CurrentCardinality != registration.CurrentCardinality {
		return "current_cardinality", "is incompatible with the existing predicate"
	}
	return "", ""
}

func SemanticPredicateKindAllowed(allowed []string, actual string) bool {
	return len(allowed) == 0 || slices.Contains(allowed, actual)
}

func SelectSubmissionPredicateCandidate(
	candidates []SemanticReviewPredicateCandidate,
	requestedKey, canonicalKey string,
) (*SemanticReviewPredicateCandidate, error) {
	for _, candidate := range candidates {
		if candidate.PredicateKey == requestedKey {
			return &candidate, nil
		}
	}
	for _, candidate := range candidates {
		if candidate.PredicateKey == canonicalKey {
			return &candidate, nil
		}
	}
	var alias *SemanticReviewPredicateCandidate
	for _, candidate := range candidates {
		if slices.Contains(candidate.Aliases, requestedKey) || slices.Contains(candidate.Aliases, canonicalKey) {
			if alias != nil {
				return nil, ErrSubmissionPredicateRegistrationHeld
			}
			matched := candidate
			alias = &matched
		}
	}
	return alias, nil
}

func CanonicalGeneratedPredicateKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out []rune
	lastUnderscore := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, r)
			lastUnderscore = false
			continue
		}
		if len(out) == 0 || lastUnderscore {
			continue
		}
		out = append(out, '_')
		lastUnderscore = true
	}
	for len(out) > 0 && out[len(out)-1] == '_' {
		out = out[:len(out)-1]
	}
	if len(out) > 64 {
		out = out[:64]
		for len(out) > 0 && out[len(out)-1] == '_' {
			out = out[:len(out)-1]
		}
	}
	if len(out) == 0 {
		return "predicate_" + shortPredicateHash(value, 12)
	}
	return string(out)
}

func CollisionGeneratedPredicateKey(base string, relationshipKind string, original string) string {
	// The suffix uses a trimmed kind, but the hash uses the original kind bytes for durable identity compatibility.
	suffix := "__" + strings.TrimSpace(relationshipKind) + "_" + shortPredicateHash(original+":"+relationshipKind, 8)
	runes := []rune(base)
	maxBase := 64 - len([]rune(suffix))
	if maxBase < 1 {
		maxBase = 1
	}
	if len(runes) > maxBase {
		runes = runes[:maxBase]
	}
	for len(runes) > 0 && runes[len(runes)-1] == '_' {
		runes = runes[:len(runes)-1]
	}
	if len(runes) == 0 {
		runes = []rune("predicate")
	}
	return string(runes) + suffix
}

func shortPredicateHash(value string, n int) string {
	sum := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(sum[:])
	if n <= 0 || n > len(encoded) {
		return encoded
	}
	return encoded[:n]
}
