package contract

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycleStatusAndValidationShareOneDecisionTable(t *testing.T) {
	for _, tc := range []struct{ decision, status string }{
		{"reject", "rejected"}, {"stale", "stale"}, {"reinforce", "reinforced"},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			require.Equal(t, tc.status, LifecycleStatus(tc.decision))
			input := NormalizeUpdateHypothesisStatusInput(UpdateHypothesisStatusInput{
				TeamID: " " + policyTeamID + " ", ActorProfileID: policySubjectID,
				HypothesisID: policyObjectID, Decision: tc.decision, Status: " " + tc.status + " ",
			})
			require.NoError(t, ValidateUpdateHypothesisStatusInput(input))
			input.Status = "proposed"
			require.ErrorContains(t, ValidateUpdateHypothesisStatusInput(input), "requires status "+`"`+tc.status+`"`)
		})
	}
	require.Equal(t, "", LifecycleStatus("ignore"))
	require.Equal(t, "", LifecycleStatus("unknown"))
	input := UpdateHypothesisStatusInput{TeamID: policyTeamID, ActorProfileID: policySubjectID,
		HypothesisID: policyObjectID, Status: "proposed", Decision: "unknown"}
	require.ErrorContains(t, ValidateUpdateHypothesisStatusInput(input), `unsupported feedback decision "unknown"`)
	input.Status = "submitted"
	require.ErrorContains(t, ValidateUpdateHypothesisStatusInput(input), `unsupported hypothesis status "submitted"`)
}
