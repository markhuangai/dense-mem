package postgres

import (
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
)

func validateAssessmentDecisionAudit(
	assessmentID, policyVersion string,
	threshold *float64,
	gateResult string,
	suppressSupport bool,
) error {
	if assessmentID == "" {
		if policyVersion != "" || threshold != nil || gateResult != "" || suppressSupport {
			return errors.New("assessment audit fields require assessment_id")
		}
		return nil
	}
	if _, err := uuid.Parse(assessmentID); err != nil {
		return fmt.Errorf("assessment_id is invalid: %w", err)
	}
	if policyVersion == "" {
		return errors.New("assessment_policy_version is required with assessment_id")
	}
	if threshold == nil || math.IsNaN(*threshold) || math.IsInf(*threshold, 0) || *threshold < 0 || *threshold > 1 {
		return errors.New("threshold_used must be between 0 and 1 with assessment_id")
	}
	switch gateResult {
	case "meets_write_threshold", "below_write_threshold", "not_applicable":
	default:
		return fmt.Errorf("unsupported assessment gate_result %q", gateResult)
	}
	if suppressSupport && gateResult != "below_write_threshold" {
		return errors.New("support suppression requires below_write_threshold")
	}
	return nil
}
