package contract

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRecallHypothesisMatchOrderIsFixedAndIndependent(t *testing.T) {
	want := [5]RecallHypothesisMatchCategory{
		RecallHypothesisSourceOverlap, RecallHypothesisEndpointOverlap,
		RecallHypothesisStatementMatch, RecallHypothesisRationaleMatch, RecallHypothesisFallback,
	}
	got := RecallHypothesisMatchOrder()
	require.Equal(t, want, got)
	got[0] = RecallHypothesisFallback
	require.Equal(t, want, RecallHypothesisMatchOrder())
}

func TestNormalizeRecallHypothesesInputPreservesBounds(t *testing.T) {
	for _, tc := range []struct{ input, want int }{
		{-1, 5}, {0, 5}, {1, 1}, {5, 5}, {20, 20}, {21, 20},
	} {
		t.Run(fmt.Sprint(tc.input), func(t *testing.T) {
			teamID := uuid.NewString()
			got, err := NormalizeRecallHypothesesInput(RecallHypothesesInput{
				TeamID: " " + teamID + " ", Query: " \tPattern_% \n", Limit: tc.input,
			})
			require.NoError(t, err)
			require.Equal(t, teamID, got.TeamID)
			require.Equal(t, "Pattern_%", got.Query)
			require.Equal(t, tc.want, got.Limit)
			require.Equal(t, []string{}, got.EvidenceIDs)
			require.Equal(t, []string{}, got.RelationshipIDs)
			require.Equal(t, []string{}, got.EntityIDs)
			require.Equal(t, []string{}, got.ValueIDs)
		})
	}
}

func TestNormalizeRecallHypothesesInputCanonicalizesEachContextWithoutMutation(t *testing.T) {
	first, second := uuid.NewString(), uuid.NewString()
	values := []string{" ", " " + strings.ToUpper(first) + " ", first, second, ""}
	input := RecallHypothesesInput{
		TeamID: uuid.NewString(), EvidenceIDs: values, RelationshipIDs: values, EntityIDs: values, ValueIDs: values,
	}
	got, err := NormalizeRecallHypothesesInput(input)
	require.NoError(t, err)
	want := []string{first, second}
	for _, ids := range [][]string{got.EvidenceIDs, got.RelationshipIDs, got.EntityIDs, got.ValueIDs} {
		require.Equal(t, want, ids)
		ids[0] = "changed"
	}
	require.Equal(t, []string{" ", " " + strings.ToUpper(first) + " ", first, second, ""}, values)
}

func TestNormalizeRecallHypothesesInputPreservesErrorPrecedence(t *testing.T) {
	valid := uuid.NewString()
	for _, tc := range []struct {
		name   string
		input  RecallHypothesesInput
		prefix string
	}{
		{"team", RecallHypothesesInput{TeamID: "invalid", EvidenceIDs: []string{"invalid"}}, "team_id is required"},
		{"evidence", RecallHypothesesInput{TeamID: valid, EvidenceIDs: []string{"invalid"}, RelationshipIDs: []string{"invalid"}}, "recall context evidence IDs"},
		{"relationship", RecallHypothesesInput{TeamID: valid, RelationshipIDs: []string{"invalid"}, EntityIDs: []string{"invalid"}}, "recall context Relationship IDs"},
		{"entity", RecallHypothesesInput{TeamID: valid, EntityIDs: []string{"invalid"}, ValueIDs: []string{"invalid"}}, "recall context entity IDs"},
		{"value", RecallHypothesesInput{TeamID: valid, ValueIDs: []string{"invalid"}}, "recall context Value IDs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeRecallHypothesesInput(tc.input)
			_, parseErr := uuid.Parse("invalid")
			require.EqualError(t, err, tc.prefix+": "+parseErr.Error())
			require.EqualError(t, errors.Unwrap(err), parseErr.Error())
		})
	}
}

func TestNormalizeRecallHypothesesInputPreservesContextCapValidationOrder(t *testing.T) {
	ids := make([]string, 200)
	for i := range ids {
		ids[i] = uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprint(i))).String()
	}
	for _, tc := range []struct {
		name    string
		values  []string
		invalid bool
	}{
		{"at_cap", ids, false},
		{"before_cap", append(append([]string{}, ids[:199]...), "invalid"), true},
		{"invalid_after_cap", append(append([]string{}, ids...), "invalid"), true},
		{"duplicate_after_cap", append(append([]string{}, ids...), ids[0], "invalid"), true},
		{"blank_after_cap", append(append([]string{}, ids...), " ", "invalid"), true},
		{"valid_overflow_stops_validation", append(append([]string{}, ids...), uuid.NewString(), "invalid"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRecallHypothesesInput(RecallHypothesesInput{
				TeamID: uuid.NewString(), EvidenceIDs: tc.values,
				RelationshipIDs: tc.values, EntityIDs: tc.values, ValueIDs: tc.values,
			})
			if tc.invalid {
				require.ErrorContains(t, err, "recall context evidence IDs:")
				return
			}
			require.NoError(t, err)
			for _, values := range [][]string{got.EvidenceIDs, got.RelationshipIDs, got.EntityIDs, got.ValueIDs} {
				require.Equal(t, ids, values)
			}
		})
	}
}
