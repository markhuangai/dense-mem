//go:build integration

package postgres

import (
	"context"
	"fmt"
	"slices"
	"testing"
)

func BenchmarkHypothesisRecallOwnership(b *testing.B) {
	fixture := newHypothesisRecallFixture(b)
	mixed := hypothesisRecallFixtureIDs(1, 3, 5, 7, 9, 11, 13, 15, 32, 33, 2, 6, 10, 14, 4, 12, 8)
	workloads := []struct {
		name  string
		input RecallHypothesesInput
		want  []string
	}{
		{"evidence_source", RecallHypothesesInput{TeamID: fixture.teamID, EvidenceIDs: []string{fixture.evidenceID}, Limit: 20}, mixed[:10]},
		{"graph_source", RecallHypothesesInput{TeamID: fixture.teamID, RelationshipIDs: []string{fixture.graphRelationshipID}}, hypothesisRecallFixtureIDs(40)},
		{"endpoint", RecallHypothesesInput{TeamID: fixture.teamID, EntityIDs: []string{fixture.entityID}, Limit: 20}, hypothesisRecallFixtureIDs(2, 3, 6, 7, 10, 11, 14, 15)},
		{"typed_value", RecallHypothesesInput{TeamID: fixture.teamID, ValueIDs: []string{fixture.valueID}}, hypothesisRecallFixtureIDs(41)},
		{"statement", RecallHypothesesInput{TeamID: fixture.teamID, Query: "statement needle", Limit: 20}, hypothesisRecallFixtureIDs(4, 5, 6, 7, 12, 13, 14, 15)},
		{"rationale", RecallHypothesesInput{TeamID: fixture.teamID, Query: "rationale needle", Limit: 20}, hypothesisRecallFixtureIDs(8, 9, 10, 11, 12, 13, 14, 15)},
		{"fallback", RecallHypothesesInput{TeamID: fixture.teamID}, hypothesisRecallFixtureIDs(8, 1, 0, 2, 3)},
		{"mixed_ties", fixture.mixedInput(), mixed},
		{"capped_context", RecallHypothesesInput{
			TeamID: fixture.teamID, Limit: 20, EvidenceIDs: cappedHypothesisRecallIDs(fixture.evidenceID, false),
		}, mixed[:10]},
	}
	for _, workload := range workloads {
		b.Run(workload.name, func(b *testing.B) {
			benchmarkDreamPolicyWorkload(b, fixture.counters, func(_ int) (string, error) {
				records, err := fixture.store.RecallHypotheses(context.Background(), workload.input)
				if err != nil {
					return "", err
				}
				if !slices.Equal(workload.want, hypothesisIDs(records)) {
					return "", fmt.Errorf("unexpected ordered hypothesis IDs: %v", hypothesisIDs(records))
				}
				return hypothesisRecallSignature(records), nil
			})
		})
	}
}
