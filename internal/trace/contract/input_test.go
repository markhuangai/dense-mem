package contract

import (
	"fmt"

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
		{"events", 100, 500, func(in *Input, v int) { in.MaxEvents = v }, func(in Input) int { return in.MaxEvents }},
		{"content", 2000, 16384, func(in *Input, v int) { in.MaxFragmentContentRunes = v }, func(in Input) int { return in.MaxFragmentContentRunes }},
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
		input := Input{TeamID: " team ", EvidenceID: " evidence ", IncludeEvidenceContent: flag, IncludeTransitions: flag}
		actual := NormalizeInput(input)
		require.Equal(t, "team", actual.TeamID)
		require.Equal(t, "evidence", actual.EvidenceID)
		require.True(t, flag == actual.IncludeEvidenceContent)
		require.True(t, flag == actual.IncludeTransitions)
	}
}
