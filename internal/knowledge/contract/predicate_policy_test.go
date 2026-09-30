package contract

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPredicateKeyGolden(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"  Works  On  ", "works_on"},
		{"École — Déjà", "école_déjà"},
		{"!!!", "predicate_e84c538e7fe2"},
		{"", "predicate_e3b0c44298fc"},
		{strings.Repeat("a", 70), strings.Repeat("a", 64)},
		{strings.Repeat("a", 63) + "!", strings.Repeat("a", 63)},
	} {
		t.Run(test.input, func(t *testing.T) {
			require.Equal(t, test.want, CanonicalGeneratedPredicateKey(test.input))
		})
	}
	require.Equal(t, "caused_by__event_a3afd823", CollisionGeneratedPredicateKey("caused_by", "event", "caused by"))
	require.Equal(t, "caused_by__event_171796e7", CollisionGeneratedPredicateKey("caused_by", " event ", "caused by"))
}

func TestPredicateCandidateSelectionPrecedenceAndAmbiguity(t *testing.T) {
	requested, canonical := "Foo Bar", "foo_bar"
	alias := SemanticReviewPredicateCandidate{PredicateKey: "alias", Aliases: []string{requested}}
	otherAlias := SemanticReviewPredicateCandidate{PredicateKey: "other_alias", Aliases: []string{canonical}}
	canonicalCandidate := SemanticReviewPredicateCandidate{PredicateKey: canonical}
	exact := SemanticReviewPredicateCandidate{PredicateKey: requested}
	for _, test := range []struct {
		name       string
		candidates []SemanticReviewPredicateCandidate
		want       string
		wantErr    error
	}{
		{"exact", []SemanticReviewPredicateCandidate{alias, canonicalCandidate, exact}, requested, nil},
		{"canonical", []SemanticReviewPredicateCandidate{alias, canonicalCandidate}, canonical, nil},
		{"alias", []SemanticReviewPredicateCandidate{alias}, "alias", nil},
		{"ambiguous aliases", []SemanticReviewPredicateCandidate{alias, otherAlias}, "", ErrSubmissionPredicateRegistrationHeld},
		{"none", nil, "", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := SelectSubmissionPredicateCandidate(test.candidates, requested, canonical)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if test.want == "" {
				require.Nil(t, got)
			} else {
				require.Equal(t, test.want, got.PredicateKey)
			}
		})
	}
}

func TestPredicateRegistrationValidationAndCompatibility(t *testing.T) {
	registration := SubmissionPredicateRegistrationInput{
		PredicateKey: "  Works On  ", SubjectKind: " person ", ObjectKind: "project",
		RelationshipKind: "state", CurrentCardinality: "many",
	}
	registration = NormalizeSubmissionPredicateRegistration(registration)
	require.Equal(t, "Works On", registration.PredicateKey)
	require.Equal(t, "person", registration.SubjectKind)
	require.Empty(t, ValidateSubmissionPredicateRegistrationFields(3, registration))

	invalid := SubmissionPredicateRegistrationInput{
		PredicateKey: strings.Repeat("x", 129), SubjectKind: "wrong", ObjectKind: "wrong",
		RelationshipKind: "wrong", CurrentCardinality: "wrong",
	}
	require.Equal(t, []SubmissionPredicateRegistrationIssue{
		{RegistrationIndex: 2, Field: "predicate_key", Message: "is required and must be bounded"},
		{RegistrationIndex: 2, Field: "subject_kind", Message: "is unsupported"},
		{RegistrationIndex: 2, Field: "object_kind", Message: "is unsupported"},
		{RegistrationIndex: 2, Field: "relationship_kind", Message: "is unsupported"},
		{RegistrationIndex: 2, Field: "current_cardinality", Message: "is unsupported"},
	}, ValidateSubmissionPredicateRegistrationFields(2, invalid))

	registration.RelationshipRef = "r1"
	require.NoError(t, ValidateCommitSubmissionPredicateRegistration(registration))
	invalid.RelationshipRef = "r2"
	require.EqualError(t, ValidateCommitSubmissionPredicateRegistration(invalid), "submission predicate registration key must be at most 128 characters")
	invalid.PredicateKey = "valid"
	require.EqualError(t, ValidateCommitSubmissionPredicateRegistration(invalid), "submission predicate registration endpoint kinds are unsupported")
	invalid.SubjectKind, invalid.ObjectKind = "person", "project"
	require.EqualError(t, ValidateCommitSubmissionPredicateRegistration(invalid), "submission predicate registration policy is unsupported")
	invalid.RelationshipRef = ""
	require.EqualError(t, ValidateCommitSubmissionPredicateRegistration(invalid), "submission predicate registration ref and key are required")

	loaded := SemanticReviewPredicateCandidate{
		PredicateKey: "works_on", AllowedSubjectKinds: []string{"person"}, AllowedObjectKinds: []string{"project"},
		RelationshipKind: "state", CurrentCardinality: "many", LifecycleState: "active",
	}
	field, message := SubmissionPredicateRegistrationCompatibility(loaded, registration)
	require.Empty(t, field)
	require.Empty(t, message)
	loaded.AllowedSubjectKinds = nil
	loaded.AllowedObjectKinds = nil
	field, message = SubmissionPredicateRegistrationCompatibility(loaded, registration)
	require.Empty(t, field)
	require.Empty(t, message)
	loaded.LifecycleState = "retired"
	field, message = SubmissionPredicateRegistrationCompatibility(loaded, registration)
	require.Equal(t, "predicate_key", field)
	require.Equal(t, "resolves to a predicate that is not active", message)
	loaded.LifecycleState = "active"
	loaded.AllowedSubjectKinds = []string{"organization"}
	field, message = SubmissionPredicateRegistrationCompatibility(loaded, registration)
	require.Equal(t, "subject_kind", field)
	require.Equal(t, "is incompatible with the existing predicate", message)
}

func TestGeneratedPredicateInputDefaultsAndValidation(t *testing.T) {
	input := NormalizeEnsureSemanticPredicateCandidateInput(EnsureSemanticPredicateCandidateInput{
		TeamID:         " 00000000-0000-4000-8000-000000000001 ",
		OwnerProfileID: " 00000000-0000-4000-8000-000000000002 ",
		Predicate:      "  Works On  ", RelationshipKind: " state ",
	})
	require.Equal(t, "Works On", input.Predicate)
	require.Equal(t, "provider_generated", input.Origin)
	require.Equal(t, "other", input.SubjectKind)
	require.Equal(t, "other", input.ObjectKind)
	require.NoError(t, ValidateEnsureSemanticPredicateCandidateInput(input))
	input.RelationshipKind = "unsupported"
	require.EqualError(t, ValidateEnsureSemanticPredicateCandidateInput(input), "relationship_kind is unsupported \"unsupported\"")
	input.TeamID = "invalid"
	require.ErrorContains(t, ValidateEnsureSemanticPredicateCandidateInput(input), "team_id is required")
	require.False(t, errors.Is(ValidateEnsureSemanticPredicateCandidateInput(input), ErrSubmissionPredicateRegistrationHeld))
}
