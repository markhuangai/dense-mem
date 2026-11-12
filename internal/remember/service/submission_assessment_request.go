package service

import (
	"context"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"sort"
	"strings"
)

func (s *assessmentEngine) buildRequest(
	ctx context.Context,
	scope RememberAssessmentScope,
	plan submissionAssessmentPlan,
) (assessor.SemanticAssessmentRequest, error) {
	knownIDs := make([]string, 0, len(plan.knownEvidenceByID))
	for evidenceID := range plan.knownEvidenceByID {
		knownIDs = append(knownIDs, evidenceID)
	}
	sort.Strings(knownIDs)
	evidence := make([]assessor.SemanticReviewEvidence, 0, len(plan.Items))
	for _, item := range plan.Items {
		prepared := semanticAssessmentEvidence(item.Fragment, item.EvidenceID)
		prepared.EvidenceIndex = len(knownIDs) + item.Fragment.EvidenceIndex
		preparedEvidence := assessor.PrepareSemanticAssessmentEvidence(prepared)
		evidence = append(evidence, preparedEvidence)
	}
	knownEvidence := make([]assessor.SemanticReviewEvidence, 0, len(plan.knownEvidenceByID))
	for knownIndex, evidenceID := range knownIDs {
		known := plan.knownEvidenceByID[evidenceID]
		knownEvidence = append(knownEvidence, assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
			EvidenceID: known.EvidenceID, FragmentID: known.FragmentID, EvidenceIndex: knownIndex, Content: known.Content,
			Authority: known.Authority, SourceID: known.SourceID, SourceRevisionID: known.SourceRevisionID,
			CurrentSourceRevisionID: known.CurrentSourceRevisionID,
		}))
	}
	equivalenceCandidates := make([]assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup, 0, len(plan.duplicateCandidatesByEvidenceID))
	equivalenceIDs := make([]string, 0, len(plan.duplicateCandidatesByEvidenceID))
	for evidenceID := range plan.duplicateCandidatesByEvidenceID {
		equivalenceIDs = append(equivalenceIDs, evidenceID)
	}
	sort.Strings(equivalenceIDs)
	for _, evidenceID := range equivalenceIDs {
		group := plan.duplicateCandidatesByEvidenceID[evidenceID]
		converted := assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup{EvidenceID: evidenceID, Candidates: []assessor.SemanticAssessmentEvidenceEquivalenceCandidate{}}
		for _, candidate := range group.Candidates {
			converted.Candidates = append(converted.Candidates, assessor.SemanticAssessmentEvidenceEquivalenceCandidate{
				EvidenceID: candidate.FragmentID,
				Content:    candidate.Content,
			})
		}
		equivalenceCandidates = append(equivalenceCandidates, converted)
	}
	requestID := strings.TrimSpace(scope.IngestID)
	if requestID == "" {
		requestID = "request"
	}
	req := assessor.SemanticAssessmentRequest{
		RequestID:                     "synchronous-remember:" + requestID,
		TeamID:                        scope.TeamID,
		OwnerProfileID:                scope.OwnerProfileID,
		Evidence:                      evidence,
		KnownEvidence:                 knownEvidence,
		EvidenceEquivalenceCandidates: equivalenceCandidates,
	}
	prepared, validationErrors := assessor.PrepareSemanticAssessmentRequest(req, s.limits)
	if len(validationErrors) == 0 {
		return prepared, nil
	}
	for _, validationError := range validationErrors {
		switch validationError.Field {
		case "candidate_context_tokens":
			limit := s.limits.MaxCandidateContextTokens
			if limit <= 0 {
				limit = assessor.DefaultSemanticAssessmentLimits().MaxCandidateContextTokens
			}
			stage := assessor.SemanticAssessmentBudgetFailureStage(prepared, validationError.Field, s.limits)
			return prepared, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
				stage,
				"submission assessment context exceeds its configured token budget",
				assessor.FailureMeasurement{Unit: "tokens", Observed: prepared.CandidateContextTokens, Limit: limit},
			)
		case "input_tokens":
			limit := assessor.SemanticAssessmentConversationInputLimit(s.limits)
			stage := assessor.SemanticAssessmentBudgetFailureStage(prepared, validationError.Field, s.limits)
			return prepared, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
				stage,
				"submission assessment input exceeds its configured token budget",
				assessor.FailureMeasurement{Unit: "tokens", Observed: prepared.InputTokens, Limit: limit},
			)
		}
	}
	return assessor.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError("trusted_context_validation", "submission assessment contract is invalid")
}
