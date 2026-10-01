//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecallHypothesesPolicyPreservesOrderedResultsAndBounds(t *testing.T) {
	f := newHypothesisRecallFixture(t)
	ctx := context.Background()
	mixed := hypothesisRecallFixtureIDs(1, 3, 5, 7, 9, 11, 13, 15, 32, 33, 2, 6, 10, 14, 4, 12, 8)
	fallback := hypothesisRecallFixtureIDs(8, 1, 0, 2, 3, 4, 5, 6, 7, 9, 10, 11, 12, 13, 14, 15, 32, 33, 34, 35)
	cases := []struct {
		name  string
		input RecallHypothesesInput
		want  []string
	}{
		{"mixed_all_categories", f.mixedInput(), mixed},
		{"evidence_source", RecallHypothesesInput{TeamID: f.teamID, EvidenceIDs: []string{f.evidenceID}, Limit: 20}, mixed[:10]},
		{"graph_relationship_source", RecallHypothesesInput{TeamID: f.teamID, RelationshipIDs: []string{f.graphRelationshipID}}, hypothesisRecallFixtureIDs(40)},
		{"graph_fragment_source", RecallHypothesesInput{TeamID: f.teamID, EvidenceIDs: []string{f.graphFragmentID}}, hypothesisRecallFixtureIDs(40)},
		{"subject_endpoint", RecallHypothesesInput{TeamID: f.teamID, EntityIDs: []string{f.entityID}, Limit: 20}, hypothesisRecallFixtureIDs(2, 3, 6, 7, 10, 11, 14, 15)},
		{"object_endpoint", RecallHypothesesInput{TeamID: f.teamID, EntityIDs: []string{f.objectID}, Limit: 20}, fallback},
		{"value_endpoint", RecallHypothesesInput{TeamID: f.teamID, ValueIDs: []string{f.valueID}}, hypothesisRecallFixtureIDs(41)},
		{"statement", RecallHypothesesInput{TeamID: f.teamID, Query: "statement needle", Limit: 20}, hypothesisRecallFixtureIDs(4, 5, 6, 7, 12, 13, 14, 15)},
		{"rationale", RecallHypothesesInput{TeamID: f.teamID, Query: "rationale needle", Limit: 20}, hypothesisRecallFixtureIDs(8, 9, 10, 11, 12, 13, 14, 15)},
		{"literal_only", RecallHypothesesInput{TeamID: f.teamID, Query: "needle", Limit: 20}, hypothesisRecallFixtureIDs(4, 5, 6, 7, 12, 13, 14, 15, 8, 9, 10, 11)},
		{"default_limit", RecallHypothesesInput{TeamID: f.teamID, Query: "needle", EvidenceIDs: []string{f.evidenceID}, EntityIDs: []string{f.entityID}}, mixed[:5]},
		{"fallback_default", RecallHypothesesInput{TeamID: f.teamID}, fallback[:5]},
		{"fallback_maximum", RecallHypothesesInput{TeamID: f.teamID, Limit: 100}, fallback},
		{"no_match", RecallHypothesesInput{TeamID: f.teamID, Query: "absent"}, []string{}},
	}
	for _, limit := range []int{1, 3, 5, 10, 17, 20} {
		input := f.mixedInput()
		input.Limit = limit
		cases = append(cases, struct {
			name  string
			input RecallHypothesesInput
			want  []string
		}{fmt.Sprintf("top_%d", limit), input, mixed[:min(limit, len(mixed))]})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.counters.reset()
			records, err := f.store.RecallHypotheses(ctx, tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.want, hypothesisIDs(records))
			require.EqualValues(t, 1, f.counters.transactions.Load())
			require.EqualValues(t, 1, f.counters.completions.Load())
			for _, record := range records {
				if record.Lane == "graph" {
					require.Len(t, record.Derivations, 2)
				} else {
					require.Len(t, record.EvidenceDerivations, 1)
					require.Len(t, record.SourceEvidenceIDs, 1)
				}
			}
			t.Logf("case=%s signature=%s sql=%d transactions=%d completions=%d providers=0",
				tc.name, hypothesisRecallSignature(records), f.counters.statements.Load(),
				f.counters.transactions.Load(), f.counters.completions.Load())
		})
	}
	input := f.mixedInput()
	input.TeamID = " " + input.TeamID + " "
	input.Query = " needle \t"
	input.EvidenceIDs = []string{" ", strings.ToUpper(f.evidenceID), " " + f.evidenceID + " "}
	records, err := f.store.RecallHypotheses(ctx, input)
	require.NoError(t, err)
	require.Equal(t, mixed, hypothesisIDs(records))
	for _, kind := range []string{"evidence", "relationship", "entity", "value"} {
		for _, overflow := range []bool{false, true} {
			input := RecallHypothesesInput{TeamID: f.teamID, Limit: 20}
			want := []string{}
			switch kind {
			case "evidence":
				input.EvidenceIDs = cappedHypothesisRecallIDs(f.evidenceID, overflow)
				want = mixed[:10]
			case "relationship":
				input.RelationshipIDs = cappedHypothesisRecallIDs(f.graphRelationshipID, overflow)
				want = hypothesisRecallFixtureIDs(40)
			case "entity":
				input.EntityIDs = cappedHypothesisRecallIDs(f.entityID, overflow)
				want = hypothesisRecallFixtureIDs(2, 3, 6, 7, 10, 11, 14, 15)
			case "value":
				input.ValueIDs = cappedHypothesisRecallIDs(f.valueID, overflow)
				want = hypothesisRecallFixtureIDs(41)
			}
			if overflow {
				want = []string{}
			}
			records, err := f.store.RecallHypotheses(ctx, input)
			require.NoError(t, err)
			require.Equal(t, want, hypothesisIDs(records), "%s overflow=%t", kind, overflow)
			t.Logf("case=capped_%s_%t signature=%s", kind, overflow, hypothesisRecallSignature(records))
		}
	}
}

func TestRecallHypothesesPolicyPreservesValidationBeforeTransactions(t *testing.T) {
	f := newHypothesisRecallFixture(t)
	for _, tc := range []struct {
		name   string
		input  RecallHypothesesInput
		prefix string
	}{
		{"team", RecallHypothesesInput{TeamID: "invalid", EvidenceIDs: []string{"invalid"}}, "team_id is required:"},
		{"evidence", RecallHypothesesInput{TeamID: f.teamID, EvidenceIDs: []string{"invalid"}}, "recall context evidence IDs:"},
		{"relationship", RecallHypothesesInput{TeamID: f.teamID, RelationshipIDs: []string{"invalid"}}, "recall context Relationship IDs:"},
		{"entity", RecallHypothesesInput{TeamID: f.teamID, EntityIDs: []string{"invalid"}}, "recall context entity IDs:"},
		{"value", RecallHypothesesInput{TeamID: f.teamID, ValueIDs: []string{"invalid"}}, "recall context Value IDs:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.counters.reset()
			_, err := f.store.RecallHypotheses(context.Background(), tc.input)
			require.ErrorContains(t, err, tc.prefix)
			require.Zero(t, f.counters.statements.Load())
			require.Zero(t, f.counters.transactions.Load())
			t.Logf("case=invalid_%s error=%s sql=0 transactions=0 providers=0", tc.name, err)
		})
	}
}

func TestRecallHypothesesPolicyExcludesAliasedAndStaleGraphSources(t *testing.T) {
	f := newHypothesisRecallFixture(t)
	ctx := context.Background()
	input := RecallHypothesesInput{TeamID: f.teamID, RelationshipIDs: []string{f.graphRelationshipID}}
	before, err := f.store.RecallHypotheses(ctx, input)
	require.NoError(t, err)
	require.Equal(t, hypothesisRecallFixtureIDs(40), hypothesisIDs(before))
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE relationship_records SET identity_alias_of_relationship_id = ?::uuid
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid`,
			f.graphOtherRelationshipID, f.teamID, f.graphRelationshipID).Error
	}))
	aliased, err := f.store.RecallHypotheses(ctx, input)
	require.NoError(t, err)
	require.Empty(t, aliased)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE relationship_records SET identity_alias_of_relationship_id = NULL, version = version + 1
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, f.teamID, f.graphRelationshipID).Error
	}))
	stale, err := f.store.RecallHypotheses(ctx, input)
	require.NoError(t, err)
	require.Empty(t, stale)
	t.Log("case=graph_source_drift alias=excluded stale=excluded")
}

func hypothesisRecallSignature(records []HypothesisRecord) string {
	var signature strings.Builder
	for _, record := range records {
		fmt.Fprintf(&signature, "%s:%s:%s:%d:%d;", record.HypothesisID, record.Lane, record.Status,
			len(record.Derivations), len(record.EvidenceDerivations))
	}
	return signature.String()
}
