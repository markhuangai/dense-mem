package contract

import (
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInputDoesNotExposeDerivedSpaceScope(t *testing.T) {
	if _, ok := reflect.TypeOf(Input{}).FieldByName("spaceID"); ok {
		t.Fatal("trace input must not expose adapter-derived space scope")
	}
}

func TestNormalizeInputBounds(t *testing.T) {
	for _, field := range []struct {
		name          string
		fallback, max int
		set           func(*Input, int)
		get           func(Input) int
	}{
		{"depth", 1, 4, func(in *Input, v int) { in.MaxDepth = v }, func(in Input) int { return in.MaxDepth }},
		{"edges", 24, 100, func(in *Input, v int) { in.MaxEdges = v }, func(in Input) int { return in.MaxEdges }},
		{"events", 100, 500, func(in *Input, v int) { in.MaxEvents = v }, func(in Input) int { return in.MaxEvents }},
		{"content", 2000, 8000, func(in *Input, v int) { in.MaxFragmentContentRunes = v }, func(in Input) int { return in.MaxFragmentContentRunes }},
	} {
		for _, bound := range []struct{ value, want int }{
			{-1, field.fallback}, {0, field.fallback}, {1, 1},
			{field.fallback, field.fallback}, {field.max - 1, field.max - 1},
			{field.max, field.max}, {field.max + 1, field.max},
		} {
			t.Run(fmt.Sprintf("%s/%d", field.name, bound.value), func(t *testing.T) {
				input := Input{}
				field.set(&input, bound.value)
				actual := NormalizeInput(input)
				require.Equal(t, bound.want, field.get(actual))
				require.Equal(t, actual, NormalizeInput(actual))
			})
		}
	}
}

func TestNormalizeInputPreservesCallerFieldsAndIncludeFlags(t *testing.T) {
	for _, flag := range []*bool{nil, new(false), new(true)} {
		input := Input{
			TeamID: " team ", RelationshipID: " relationship ", Topic: " Topic ",
			IncludeEvidenceContent: flag, IncludeVerification: flag, IncludeTransitions: flag,
			PredicateKeys: []string{" uses ", "", "uses", "Uses", " works_on "},
		}
		actual := NormalizeInput(input)
		require.Equal(t, "team", actual.TeamID)
		require.Equal(t, "relationship", actual.RelationshipID)
		require.Equal(t, "Topic", actual.Topic)
		require.Equal(t, []string{"uses", "Uses", "works_on"}, actual.PredicateKeys)
		require.True(t, actual.IncludeEvidenceContent == flag)
		require.True(t, actual.IncludeVerification == flag)
		require.True(t, actual.IncludeTransitions == flag)
		require.Nil(t, actual.MinRelevance)
		require.Equal(t, []string{" uses ", "", "uses", "Uses", " works_on "}, input.PredicateKeys)
	}
}

func TestNormalizeInputCapsDistinctPredicates(t *testing.T) {
	var predicates, want []string
	for i := 0; i < 35; i++ {
		key := fmt.Sprintf("key-%02d", i)
		predicates = append(predicates, "", " "+key+" ", key)
		if i < 30 {
			want = append(want, key)
		}
	}
	require.Equal(t, want, NormalizeInput(Input{PredicateKeys: predicates}).PredicateKeys)
}

func TestNormalizeInputRelevance(t *testing.T) {
	for _, test := range []struct {
		name        string
		value, want float64
	}{
		{"negative", -1, 0}, {"zero", 0, 0}, {"fraction", 0.5, 0.5},
		{"one", 1, 1}, {"over", 2, 1}, {"nan", math.NaN(), 0},
		{"positive infinity", math.Inf(1), 0}, {"negative infinity", math.Inf(-1), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := test.value
			actual := NormalizeInput(Input{MinRelevance: &test.value})
			require.NotNil(t, actual.MinRelevance)
			require.Equal(t, test.want, *actual.MinRelevance)
			require.NotSame(t, &test.value, actual.MinRelevance)
			if math.IsNaN(original) {
				require.True(t, math.IsNaN(test.value))
			} else {
				require.Equal(t, original, test.value)
			}
		})
	}
}
