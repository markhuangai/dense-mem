package contract

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateAssessmentDecisionAudit(t *testing.T) {
	assessmentID := uuid.NewString()
	threshold := 0.75

	tests := []struct {
		name          string
		assessmentID  string
		policyVersion string
		threshold     *float64
		gateResult    string
		suppress      bool
		wantErr       string
	}{
		{name: "empty audit is valid"},
		{name: "empty assessment rejects policy", policyVersion: "v1", wantErr: "assessment audit fields require assessment_id"},
		{name: "empty assessment rejects threshold", threshold: &threshold, wantErr: "assessment audit fields require assessment_id"},
		{name: "invalid assessment id", assessmentID: "not-a-uuid", wantErr: "assessment_id is invalid"},
		{name: "policy version required", assessmentID: assessmentID, wantErr: "assessment_policy_version is required"},
		{name: "threshold required", assessmentID: assessmentID, policyVersion: "v1", wantErr: "threshold_used must be between"},
		{name: "threshold below range", assessmentID: assessmentID, policyVersion: "v1", threshold: floatPointer(-0.1), wantErr: "threshold_used must be between"},
		{name: "threshold above range", assessmentID: assessmentID, policyVersion: "v1", threshold: floatPointer(1.1), wantErr: "threshold_used must be between"},
		{name: "threshold NaN", assessmentID: assessmentID, policyVersion: "v1", threshold: floatPointer(math.NaN()), wantErr: "threshold_used must be between"},
		{name: "threshold positive infinity", assessmentID: assessmentID, policyVersion: "v1", threshold: floatPointer(math.Inf(1)), wantErr: "threshold_used must be between"},
		{name: "unsupported gate", assessmentID: assessmentID, policyVersion: "v1", threshold: &threshold, gateResult: "unknown", wantErr: "unsupported assessment gate_result"},
		{name: "suppression requires below threshold", assessmentID: assessmentID, policyVersion: "v1", threshold: &threshold, gateResult: "meets_write_threshold", suppress: true, wantErr: "support suppression requires below_write_threshold"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAssessmentDecisionAudit(test.assessmentID, test.policyVersion, test.threshold, test.gateResult, test.suppress)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestValidateAssessmentDecisionAuditAcceptsEveryGateResult(t *testing.T) {
	threshold := 0.5
	for _, gateResult := range []string{"meets_write_threshold", "below_write_threshold", "not_applicable"} {
		t.Run(gateResult, func(t *testing.T) {
			require.NoError(t, ValidateAssessmentDecisionAudit(uuid.NewString(), "v1", &threshold, gateResult, gateResult == "below_write_threshold"))
		})
	}
}

func floatPointer(value float64) *float64 {
	return &value
}
