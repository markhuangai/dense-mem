package dreamservice

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestHeuristicGeneratorProducesReviewableDreams(t *testing.T) {
	generator := NewHeuristicGenerator("")

	dreams, err := generator.Generate(context.Background(), "team-1", GenerateRequest{
		MaxOutputs: 2,
		Inputs: []DreamInput{
			{Type: "fact", ID: "fact-1", Subject: "assistant", Predicate: "uses", Object: "dense-mem"},
			{Type: "claim", ID: "claim-1", Subject: "user", Predicate: "prefers", Object: "nightly review"},
			{Type: "fragment", ID: "fragment-1", Content: "The team wants speculative memory to stay unapproved."},
		},
	})
	require.NoError(t, err)
	require.Len(t, dreams, 2)
	for _, dream := range dreams {
		require.NotEmpty(t, dream.Hypothesis)
		require.NotEmpty(t, dream.WhatIf)
		require.NotEmpty(t, dream.PossibleOutcome)
		require.NotEmpty(t, dream.Rationale)
		require.GreaterOrEqual(t, dream.Likelihood, 0.0)
		require.LessOrEqual(t, dream.Likelihood, 1.0)
		require.GreaterOrEqual(t, dream.Confidence, 0.0)
		require.LessOrEqual(t, dream.Confidence, 1.0)
		require.Len(t, dream.SourceRefs, 2)
	}
}

func TestHeuristicGeneratorSkipsSamePredicatePairs(t *testing.T) {
	generator := NewHeuristicGenerator("model-x")
	require.Equal(t, "model-x", generator.Model())

	dreams, err := generator.Generate(context.Background(), "team-1", GenerateRequest{
		MaxOutputs: 5,
		Inputs: []DreamInput{
			{Type: "fact", ID: "fact-1", Subject: "a", Predicate: "likes", Object: "x"},
			{Type: "fact", ID: "fact-2", Subject: "b", Predicate: "likes", Object: "y"},
			{Type: "claim", ID: "claim-1", Subject: "c", Predicate: "uses", Object: "z"},
		},
	})
	require.NoError(t, err)
	require.Len(t, dreams, 2)
	for _, dream := range dreams {
		require.NotEqual(t, dream.SourceRefs[0].ID, dream.SourceRefs[1].ID)
	}
}

func TestInputSummaryTruncatesContentAtRuneBoundary(t *testing.T) {
	content := strings.Repeat("a", 95) + "🙂tail"

	summary := inputSummary(DreamInput{Type: "fragment", ID: "fragment-1", Content: content})

	require.True(t, utf8.ValidString(summary))
	require.Equal(t, 96, utf8.RuneCountInString(summary))
	require.True(t, strings.HasSuffix(summary, "🙂"))
}
