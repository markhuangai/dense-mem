package repository

import (
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func validateAssessmentDecisionAudit(
	assessmentID, submissionAssessmentID, policyVersion string,
	threshold *float64,
	gateResult string,
	suppressSupport bool,
) error {
	if err := validateAssessmentReferences(assessmentID, submissionAssessmentID); err != nil {
		return err
	}
	if !hasAssessmentReference(assessmentID, submissionAssessmentID) {
		if policyVersion != "" || threshold != nil || gateResult != "" || suppressSupport {
			return errors.New("assessment audit fields require an assessment reference")
		}
		return nil
	}
	if policyVersion == "" {
		return errors.New("assessment_policy_version is required with an assessment reference")
	}
	if threshold == nil || *threshold < 0 || *threshold > 1 {
		return errors.New("threshold_used must be between 0 and 1 with an assessment reference")
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

func validateSemanticReviewDetails(
	assessmentID, submissionAssessmentID, kind, question string,
	options []map[string]any,
	guidance string,
) error {
	if kind == "" && question == "" && len(options) == 0 && guidance == "" {
		return nil
	}
	if err := validateAssessmentReferences(assessmentID, submissionAssessmentID); err != nil {
		return err
	}
	if !hasAssessmentReference(assessmentID, submissionAssessmentID) {
		return errors.New("semantic review details require an assessment reference")
	}
	switch kind {
	case "support_confidence", "identity", "predicate", "scope", "time":
	default:
		return fmt.Errorf("unsupported semantic review kind %q", kind)
	}
	if question == "" || len([]rune(question)) > 1000 {
		return errors.New("semantic review question is required and must be at most 1000 characters")
	}
	if len(options) == 0 {
		return errors.New("semantic review options are required")
	}
	if len(options) > 100 {
		return errors.New("semantic review options exceed 100 entries")
	}
	if len([]rune(guidance)) > 2000 {
		return errors.New("semantic review guidance must be at most 2000 characters")
	}
	return nil
}

func validateAssessmentReferences(assessmentID, submissionAssessmentID string) error {
	if assessmentID != "" && submissionAssessmentID != "" {
		return errors.New("assessment and submission_assessment_id are mutually exclusive")
	}
	if assessmentID != "" {
		if _, err := uuid.Parse(assessmentID); err != nil {
			return fmt.Errorf("assessment_id is invalid: %w", err)
		}
	}
	if submissionAssessmentID != "" {
		if _, err := uuid.Parse(submissionAssessmentID); err != nil {
			return fmt.Errorf("submission_assessment_id is invalid: %w", err)
		}
	}
	return nil
}

func hasAssessmentReference(assessmentID, submissionAssessmentID string) bool {
	return assessmentID != "" || submissionAssessmentID != ""
}

func statusForVerdict(verdict string) string {
	switch verdict {
	case string(domain.VerificationContradicted):
		return string(domain.RelationshipStatusRejected)
	case string(domain.VerificationInsufficient):
		return string(domain.RelationshipStatusPendingEvidence)
	default:
		return string(domain.RelationshipStatusActive)
	}
}

func statusForRelationshipDecision(input ApplyRelationshipDecisionInput) string {
	if input.SuppressSupport && input.EvidenceVerdict == string(domain.VerificationEntailed) {
		return string(domain.RelationshipStatusPendingEvidence)
	}
	return statusForVerdict(input.EvidenceVerdict)
}
