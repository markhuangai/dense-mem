package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

const submissionAssessmentMaxHoldIssues = 50

type submissionAssessmentReviewRequiredError struct {
	Issues    []SubmissionHoldIssue
	Truncated bool
}

func (e *submissionAssessmentReviewRequiredError) Error() string {
	return errSubmissionAssessmentRequiresReview.Error()
}

func (e *submissionAssessmentReviewRequiredError) Unwrap() error {
	return errSubmissionAssessmentRequiresReview
}

func (s *submissionAssessmentPlacementWorkerService) completeReview(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	stage string,
	issues []SubmissionHoldIssue,
	truncated bool,
) error {
	if len(issues) == 0 {
		issues = []SubmissionHoldIssue{{
			Code:      "commit_review_required",
			Component: "relationship",
			Message:   "submission requires a corrected complete replacement before semantic commit",
		}}
	}
	if len(issues) > submissionAssessmentMaxHoldIssues {
		issues = append([]SubmissionHoldIssue(nil), issues[:submissionAssessmentMaxHoldIssues]...)
		truncated = true
	}
	holdIssues := make([]map[string]any, 0, len(issues))
	for _, issue := range issues {
		holdIssues = append(holdIssues, map[string]any{
			"code":             issue.Code,
			"relationship_ref": issue.RelationshipRef,
			"component":        issue.Component,
			"message":          issue.Message,
		})
	}
	payload := map[string]any{
		"assessor_contract": domain.ContractVersion,
		"review_stage":      boundedPlacementFailureStage(stage),
	}
	payload["hold_issues"] = holdIssues
	payload["hold_issues_truncated"] = truncated
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_terminal",
		Status:                       string(domain.SemanticReviewReviewRequired),
		Category:                     "candidate",
		Payload:                      payload,
	})
	if err == nil && completed == nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, errors.New("submission assessment worker: nil review result"))
	}
	if err != nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, err)
	}
	if err == nil {
		s.logLifecycle(scope, "submission_held", "awaiting_review", stage, strings.TrimSpace(stage), nil)
	}
	return err
}

func submissionAssessmentReviewIssues(
	plan submissionAssessmentPlan,
	response verifier.SemanticAssessmentResponse,
	threshold float64,
) ([]SubmissionHoldIssue, bool) {
	issues := make([]SubmissionHoldIssue, 0)
	seen := map[string]struct{}{}
	truncated := false
	appendIssue := func(issue SubmissionHoldIssue) {
		key := issue.Code + "\x00" + issue.RelationshipRef + "\x00" + issue.Component
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		if len(issues) == submissionAssessmentMaxHoldIssues {
			truncated = true
			return
		}
		issues = append(issues, issue)
	}
	for _, result := range response.EntityResults {
		relationshipRef, component := submissionAssessmentEntityIssueTarget(plan, result.Ref)
		if result.GroundingRef == nil {
			appendIssue(SubmissionHoldIssue{
				Code: "entity_grounding_missing", RelationshipRef: relationshipRef, Component: component,
				Message: "submitted Entity name or active same-team alias was not grounded in eligible evidence",
			})
			continue
		}
		if result.Action == string(domain.EntityResolutionAmbiguous) {
			appendIssue(SubmissionHoldIssue{
				Code: "entity_resolution_ambiguous", RelationshipRef: relationshipRef, Component: component,
				Message: "grounded Entity could not be resolved to one safe identity action",
			})
		}
		if result.Confidence < threshold {
			appendIssue(SubmissionHoldIssue{
				Code: "grounding_low_confidence", RelationshipRef: relationshipRef, Component: component,
				Message: "Entity grounding confidence is below the effective write threshold",
			})
		}
	}
	for _, result := range response.RelationshipResults {
		if result.Modality != "statement" {
			appendIssue(SubmissionHoldIssue{Code: "unsupported_modality", RelationshipRef: result.Ref, Component: "relationship", Message: "only statement relationships are eligible for automatic semantic commit"})
		}
		if result.PredicateStatus == "needs_review" {
			appendIssue(SubmissionHoldIssue{Code: "predicate_needs_review", RelationshipRef: result.Ref, Component: "predicate", Message: "predicate could not be resolved or registered safely"})
		}
		if result.ScopeStatus == "needs_review" {
			appendIssue(SubmissionHoldIssue{Code: "scope_needs_review", RelationshipRef: result.Ref, Component: "relationship", Message: "relationship scope requires review"})
		}
		if result.TemporalVerdict == "ambiguous" || result.TemporalVerdict == "contradicted" {
			appendIssue(SubmissionHoldIssue{Code: "temporal_uncertain", RelationshipRef: result.Ref, Component: "relationship", Message: "relationship time bounds are ambiguous or contradicted"})
		}
		if result.EvidenceVerdict != string(domain.VerificationEntailed) {
			appendIssue(SubmissionHoldIssue{Code: "evidence_not_entailed", RelationshipRef: result.Ref, Component: "support", Message: "eligible evidence does not entail the proposed relationship"})
		}
		if result.Confidence < threshold {
			appendIssue(SubmissionHoldIssue{Code: "grounding_low_confidence", RelationshipRef: result.Ref, Component: "relationship", Message: "relationship confidence is below the effective write threshold"})
		}
		if result.PredicateRange.Confidence < threshold {
			appendIssue(SubmissionHoldIssue{Code: "grounding_low_confidence", RelationshipRef: result.Ref, Component: "predicate", Message: "predicate grounding confidence is below the effective write threshold"})
		}
		if result.ValueRange != nil && result.ValueRange.Confidence < threshold {
			appendIssue(SubmissionHoldIssue{Code: "grounding_low_confidence", RelationshipRef: result.Ref, Component: "object", Message: "Value grounding confidence is below the effective write threshold"})
		}
		for _, support := range result.SupportRanges {
			if support.Confidence < threshold {
				appendIssue(SubmissionHoldIssue{Code: "grounding_low_confidence", RelationshipRef: result.Ref, Component: "support", Message: "support grounding confidence is below the effective write threshold"})
				break
			}
		}
	}
	return issues, truncated
}

func submissionAssessmentEntityIssueTarget(plan submissionAssessmentPlan, entityRef string) (string, string) {
	for _, relationship := range plan.RelationshipTargets {
		if relationship.Target.SubjectRef == entityRef {
			return relationship.Target.ProposalID, "subject"
		}
		if relationship.Target.ObjectRef != nil && *relationship.Target.ObjectRef == entityRef {
			return relationship.Target.ProposalID, "object"
		}
	}
	return "", "relationship"
}

func submissionAssessmentSupports(
	plan submissionAssessmentPlan,
	assessmentID string,
	spans []verifier.SemanticAssessmentEvidenceSpan,
) ([]repository.EvidenceSupportInput, error) {
	if len(spans) == 0 {
		return nil, errors.New("submission assessor relationship has no evidence span")
	}
	supports := make([]repository.EvidenceSupportInput, 0, len(spans))
	for _, span := range spans {
		item, ok := plan.itemsByEvidenceID[span.EvidenceID]
		if !ok {
			return nil, errors.New("submission assessor evidence span is outside the run")
		}
		quote, err := verifier.SemanticEvidenceSpan(item.Fragment.Content, span.Start, span.End)
		if err != nil {
			return nil, err
		}
		authority, err := semanticSupportAuthority(item.Fragment.Authority)
		if err != nil {
			return nil, err
		}
		supports = append(supports, repository.EvidenceSupportInput{
			FragmentID:       item.Fragment.FragmentID,
			SourceGroupKey:   fmt.Sprintf("semantic_assessment:%s:%s:%d:%d", assessmentID, span.EvidenceID, span.Start, span.End),
			SourceID:         item.Fragment.SourceID,
			SourceRevisionID: item.Fragment.SourceRevisionID,
			SpanStart:        span.Start,
			SpanEnd:          span.End,
			Quote:            quote,
			Authority:        authority,
			Metadata: map[string]any{
				"semantic_contract": domain.ContractVersion,
				"assessment_id":     assessmentID,
				"evidence_id":       span.EvidenceID,
			},
		})
	}
	return supports, nil
}

func submissionAssessmentItemForFragment(plan submissionAssessmentPlan, fragmentID string) (submissionAssessmentItem, bool) {
	for _, item := range plan.Items {
		if item.Fragment.FragmentID == fragmentID {
			return item, true
		}
	}
	return submissionAssessmentItem{}, false
}
