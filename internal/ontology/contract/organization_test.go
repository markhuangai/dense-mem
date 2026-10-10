package contract

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOrganizationInputAndFingerprints(t *testing.T) {
	a, b := policySnapshot("a", EvidenceSource, "one"), policySnapshot("b", EvidenceSource, "one")
	input, err := PrepareOrganizationInput(OrganizationInput{OperationKey: "one", Sources: []SourceHandle{b.SourceHandle, a.SourceHandle}})
	require.NoError(t, err)
	first, err := OrganizationInputHash(input, "provider-v1")
	require.NoError(t, err)
	input.OperationKey = "two"
	second, err := OrganizationInputHash(input, "provider-v1")
	require.NoError(t, err)
	require.Equal(t, first, second)
	third, err := OrganizationInputHash(input, "provider-v2")
	require.NoError(t, err)
	require.NotEqual(t, first, third)
	input.Sources = append(input.Sources, input.Sources[0])
	_, err = PrepareOrganizationInput(input)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = PrepareOrganizationInput(OrganizationInput{OperationKey: "empty"})
	require.ErrorIs(t, err, ErrInvalid)
	deps := []SourceDependency{sourceDependency(t, a), sourceDependency(t, b)}
	hash, err := OrganizationBatchHash(deps, "model")
	require.NoError(t, err)
	deps[0], deps[1] = deps[1], deps[0]
	reordered, err := OrganizationBatchHash(deps, "model")
	require.NoError(t, err)
	require.Equal(t, hash, reordered)
	deps[0].Fingerprint = first
	changed, err := OrganizationBatchHash(deps, "model")
	require.NoError(t, err)
	require.NotEqual(t, hash, changed)
}

func TestOrganizationPredicateSourceOrderDoesNotChangeItsHash(t *testing.T) {
	a := SourceHandle{Kind: PredicateSource, ID: "a", Version: 12}
	b := SourceHandle{Kind: PredicateSource, ID: "a1", Version: 2}
	forward, err := PrepareOrganizationInput(OrganizationInput{OperationKey: "same", Sources: []SourceHandle{a, b}})
	require.NoError(t, err)
	reverse, err := PrepareOrganizationInput(OrganizationInput{OperationKey: "same", Sources: []SourceHandle{b, a}})
	require.NoError(t, err)
	require.Equal(t, forward.Sources, reverse.Sources)
	one, err := OrganizationInputHash(forward, "provider")
	require.NoError(t, err)
	two, err := OrganizationInputHash(reverse, "provider")
	require.NoError(t, err)
	require.Equal(t, one, two)
	a.Version = 2
	changed, err := OrganizationInputHash(OrganizationInput{Sources: []SourceHandle{a, b}}, "provider")
	require.NoError(t, err)
	require.NotEqual(t, one, changed)
}

func TestExactMeaningRequiresMatchingEvidenceCreationContext(t *testing.T) {
	for _, content := range []string{"Atlas releases next week.", "Atlas uses PostgreSQL."} {
		a, b := policySnapshot("a", EvidenceSource, "same"), policySnapshot("b", EvidenceSource, "same")
		b.OwnerID = a.OwnerID
		a.State = map[string]string{"content": content, "created_at": "2026-01-05T12:00:00Z"}
		b.State = map[string]string{"content": content, "created_at": "2026-01-12T12:00:00Z"}
		require.True(t, CompatibleMeaningContext(a, b))
		require.False(t, ExactMeaning(a, b))
		b.State["created_at"] = a.State["created_at"]
		require.True(t, ExactMeaning(a, b))
	}
}

func TestAssessedEquivalencePreservesContext(t *testing.T) {
	a, b := policySnapshot("a", EvidenceSource, "first wording"), policySnapshot("b", EvidenceSource, "second wording")
	a.State = map[string]string{"content": "Atlas stores data in PostgreSQL.", "metadata": "{}"}
	b.State = map[string]string{"content": "PostgreSQL is Atlas's data store.", "metadata": "{}"}
	b.OwnerID = policyID("other-owner")
	require.True(t, CompatibleMeaningContext(a, b))
	require.False(t, ExactMeaning(a, b))
	group := Record{ID: policyID("group"), Kind: EvidenceGroup, Group: &Group{Members: []SourceHandle{a.SourceHandle, b.SourceHandle}, AssessmentID: policyID("assessment")}}
	snapshots := map[string]SourceSnapshot{SourceKey(a.SourceHandle): a, SourceKey(b.SourceHandle): b}
	require.NoError(t, AssessedGroupCompatible(group, snapshots))
	b.State["content"] = "I use PostgreSQL."
	require.False(t, CompatibleMeaningContext(a, b))
	b.State["content"] = "Atlas uses PostgreSQL."
	b.State["metadata"] = `{"time":"2025"}`
	require.False(t, CompatibleMeaningContext(a, b))
	a = policySnapshot("relation-a", RelationshipSource, "tuple-a")
	b = policySnapshot("relation-b", RelationshipSource, "tuple-b")
	a.State = map[string]string{"subject": "Atlas", "object_value": "10", "polarity": "+", "valid_to": "2026", "predicate_contract": "state:one"}
	b.State = map[string]string{"subject": "Atlas", "object_value": "10", "polarity": "+", "valid_to": "2026", "predicate_contract": "state:one"}
	require.True(t, CompatibleMeaningContext(a, b))
	for _, field := range []string{"subject", "object_value", "polarity", "valid_to", "predicate_contract"} {
		before := b.State[field]
		b.State[field] = "different"
		require.False(t, CompatibleMeaningContext(a, b), field)
		b.State[field] = before
	}
}

func TestCrossOwnerRelativeTimeRequiresResolvedCalendarContext(t *testing.T) {
	for _, testCase := range []struct {
		name, left, right, created, metadata string
		compatible                           bool
	}{
		{"resolved", "Atlas launched today.", "Atlas was launched today.", "2026-10-08 12:00:00+00", `{"timezone":"UTC"}`, true},
		{"repeated day word", "Today Atlas launched today.", "Atlas launched today.", "2026-10-08 12:00:00+00", `{"timezone":"UTC"}`, true},
		{"multiple day words", "Atlas launched yesterday and expanded today.", "Atlas launched yesterday and expanded today.", "2026-10-08 12:00:00+00", `{"timezone":"UTC"}`, true},
		{"reordered day words", "Yesterday Atlas launched and today Atlas expanded.", "TODAY Atlas expanded after launching yesterday.", "2026-10-08 12:00:00+00", `{"timezone":"UTC"}`, true},
		{"missing day word", "Atlas launched yesterday and expanded today.", "Atlas launched yesterday.", "2026-10-08 12:00:00+00", `{"timezone":"UTC"}`, false},
		{"extra day word", "Atlas launched yesterday and expanded today.", "Atlas launched yesterday and expanded tomorrow.", "2026-10-08 12:00:00+00", `{"timezone":"UTC"}`, false},
		{"first person", "I launched today.", "I launched today.", "2026-10-08 12:00:00+00", `{"timezone":"UTC"}`, false},
		{"spatial", "Atlas launched here today.", "Atlas launched here today.", "2026-10-08 12:00:00+00", `{"timezone":"UTC"}`, false},
		{"different relative days", "Atlas launched today.", "Atlas launched yesterday.", "2026-10-08 12:00:00+00", `{"timezone":"UTC"}`, false},
		{"unresolved date", "Atlas launched today.", "Atlas launched today.", "", `{"timezone":"UTC"}`, false},
		{"missing zone", "Atlas launched today.", "Atlas launched today.", "2026-10-08 12:00:00+00", `{}`, false},
		{"invalid zone", "Atlas launched today.", "Atlas launched today.", "2026-10-08 12:00:00+00", `{"timezone":"not/a/zone"}`, false},
		{"owner-local zone", "Atlas launched today.", "Atlas launched today.", "2026-10-08 12:00:00+00", `{"timezone":"Local"}`, false},
		{"malformed metadata", "Atlas launched today.", "Atlas launched today.", "2026-10-08 12:00:00+00", `{`, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			a, b := policySnapshot("a", EvidenceSource, "one"), policySnapshot("b", EvidenceSource, "two")
			b.OwnerID = policyID("other-owner")
			a.State = map[string]string{"content": testCase.left, "created_at": testCase.created, "metadata": testCase.metadata}
			b.State = map[string]string{"content": testCase.right, "created_at": testCase.created, "metadata": testCase.metadata}
			require.Equal(t, testCase.compatible, CompatibleMeaningContext(a, b))
			if testCase.compatible {
				b.State["created_at"] = "2026-10-09 12:00:00+00"
				require.False(t, CompatibleMeaningContext(a, b))
			}
		})
	}
}
