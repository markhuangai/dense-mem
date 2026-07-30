package memoryservice

import "github.com/markhuangai/dense-mem/internal/verifier"

func trimSemanticAssessmentCandidateContext(
	req verifier.SemanticAssessmentRequest,
	limits verifier.SemanticAssessmentLimits,
) (verifier.SemanticAssessmentRequest, error) {
	prepared, validationErrors := verifier.PrepareSemanticAssessmentRequest(req, limits)
	if len(validationErrors) == 0 {
		return prepared, nil
	}
	if !semanticAssessmentLimitFailure(validationErrors) {
		return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError(
			"candidate_context_validation",
			"semantic assessment request candidate context is invalid",
		)
	}
	removable := semanticAssessmentRemovableCandidateCount(req)
	if removable == 0 {
		return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError(
			"candidate_context_limit",
			"semantic assessment request exceeds configured token limits",
		)
	}

	low, high := 1, removable
	var best *verifier.SemanticAssessmentRequest
	for low <= high {
		removeCount := low + (high-low)/2
		candidate := cloneSemanticAssessmentRequestForTrim(req)
		if !trimSemanticAssessmentCandidates(&candidate, removeCount) {
			return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError(
				"candidate_context_limit",
				"semantic assessment candidate context could not be bounded",
			)
		}
		prepared, validationErrors = verifier.PrepareSemanticAssessmentRequest(candidate, limits)
		if len(validationErrors) == 0 {
			bestPrepared := prepared
			best = &bestPrepared
			high = removeCount - 1
			continue
		}
		if !semanticAssessmentLimitFailure(validationErrors) {
			return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError(
				"candidate_context_validation",
				"semantic assessment request candidate context is invalid",
			)
		}
		low = removeCount + 1
	}
	if best != nil {
		return *best, nil
	}
	return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError(
		"candidate_context_limit",
		"semantic assessment request exceeds configured token limits",
	)
}

func semanticAssessmentLimitFailure(validationErrors []verifier.SemanticValidationError) bool {
	if len(validationErrors) == 0 {
		return false
	}
	for _, validationError := range validationErrors {
		switch validationError.Field {
		case "candidate_context_tokens", "input_tokens":
		default:
			return false
		}
	}
	return true
}

func trimOneSemanticAssessmentCandidate(req *verifier.SemanticAssessmentRequest) bool {
	if req == nil {
		return false
	}
	if len(req.PredicateOptions) > 0 {
		req.PredicateOptions = req.PredicateOptions[:len(req.PredicateOptions)-1]
		req.CandidateContextTruncated = true
		return true
	}
	for index := len(req.EntityCandidateGroups) - 1; index >= 0; index-- {
		group := &req.EntityCandidateGroups[index]
		if len(group.Candidates) > 0 {
			group.Candidates = group.Candidates[:len(group.Candidates)-1]
			group.CandidateContextTruncated = true
			req.CandidateContextTruncated = true
			return true
		}
	}
	return false
}

func semanticAssessmentRemovableCandidateCount(req verifier.SemanticAssessmentRequest) int {
	count := len(req.PredicateOptions)
	for _, group := range req.EntityCandidateGroups {
		count += len(group.Candidates)
	}
	return count
}

func trimSemanticAssessmentCandidates(req *verifier.SemanticAssessmentRequest, count int) bool {
	if count <= 0 {
		return true
	}
	for range count {
		if !trimOneSemanticAssessmentCandidate(req) {
			return false
		}
	}
	return true
}

func cloneSemanticAssessmentRequestForTrim(req verifier.SemanticAssessmentRequest) verifier.SemanticAssessmentRequest {
	cloned := req
	cloned.Evidence = append([]verifier.SemanticReviewEvidence(nil), req.Evidence...)
	cloned.EntityCandidateGroups = make([]verifier.SemanticAssessmentEntityCandidateGroup, len(req.EntityCandidateGroups))
	for i, group := range req.EntityCandidateGroups {
		cloned.EntityCandidateGroups[i] = group
		cloned.EntityCandidateGroups[i].Candidates = append([]verifier.SemanticAssessmentEntityCandidate(nil), group.Candidates...)
	}
	cloned.PredicateOptions = make([]verifier.SemanticAssessmentPredicateOption, len(req.PredicateOptions))
	for i, option := range req.PredicateOptions {
		cloned.PredicateOptions[i] = option
		cloned.PredicateOptions[i].Aliases = append([]string(nil), option.Aliases...)
		cloned.PredicateOptions[i].AllowedSubjectKinds = append([]string(nil), option.AllowedSubjectKinds...)
		cloned.PredicateOptions[i].AllowedObjectKinds = append([]string(nil), option.AllowedObjectKinds...)
	}
	cloned.RequiredRelationshipRefs = make([]verifier.SemanticAssessmentRequiredRelationshipRef, len(req.RequiredRelationshipRefs))
	for i, required := range req.RequiredRelationshipRefs {
		cloned.RequiredRelationshipRefs[i] = required
		cloned.RequiredRelationshipRefs[i].Evidence = append([]verifier.SemanticAssessmentEvidenceSpan(nil), required.Evidence...)
	}
	return cloned
}
