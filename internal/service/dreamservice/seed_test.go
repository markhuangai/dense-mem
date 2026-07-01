package dreamservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRunCycleMaterializesSeedDreams(t *testing.T) {
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	graph := &cycleRunGraphStub{executeWrites: true}
	generator := &dreamGeneratorStub{err: errors.New("generator should not run for seeded dreams")}
	dreamEnabled := true
	svc := New(Dependencies{
		Graph:     graph,
		Generator: generator,
		Now:       func() time.Time { return now },
	})

	result, err := svc.RunCycle(context.Background(), "profile-1", RunCycleRequest{
		Manual:       true,
		DreamEnabled: &dreamEnabled,
		MaxOutputs:   1,
		SeedDreams: []SeedDream{
			{
				Hypothesis: "",
				SourceRefs: []domain.DreamSourceRef{
					{Type: "fact", ID: "fact-skipped"},
				},
			},
			{
				Hypothesis:      "Employment may explain the location period.",
				WhatIf:          "What if SAP employment overlaps the location evidence?",
				PossibleOutcome: "Recall should surface both source facts together.",
				Rationale:       "Imported relational eval seed.",
				Likelihood:      1.4,
				Confidence:      -0.2,
				SourceRefs: []domain.DreamSourceRef{
					{Type: "fact", ID: "fact-employer"},
					{Type: "fact", ID: "fact-location"},
				},
			},
			{
				Hypothesis: "Second valid seed should be capped.",
				SourceRefs: []domain.DreamSourceRef{
					{Type: "fact", ID: "fact-second"},
				},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.True(t, result.DreamRan)
	require.Equal(t, 1, result.CreatedDreams)
	require.Zero(t, generator.calls)
	require.True(t, hasDreamWriteQuery(graph.writeQueries, "MERGE (d:Dream"))
	require.Equal(t, 1, countDreamWriteQuery(graph.writeQueries, "MERGE (d:Dream"))
	require.True(t, hasDreamWriteQuery(graph.writeQueries, "DREAMS_FROM"))
}
