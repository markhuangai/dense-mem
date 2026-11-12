package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/assessor"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

var errSubmissionAssessmentStaleInput = errors.New("submission assessment exact input is stale")

func isRememberStaleInputError(err error) bool {
	return errors.Is(err, errSubmissionAssessmentStaleInput) ||
		errors.Is(err, repository.ErrSourceRevisionConflict) ||
		errors.Is(err, repository.ErrEvidenceLifecycleConflict) ||
		errors.Is(err, repository.ErrEvidenceConflictStaleInput) ||
		errors.Is(err, repository.ErrRememberExactReferenceStale) ||
		errors.Is(err, repository.ErrSubmissionAssessmentKnownEvidenceStale)
}

// IsRememberStaleInputError exposes the narrow stale-input classification to
// the request-scoped Remember processor.
func IsRememberStaleInputError(err error) bool {
	return isRememberStaleInputError(err)
}

func validateSubmissionAssessmentEvidenceConflictCanonicalization(
	plan submissionAssessmentPlan,
	response assessor.SemanticAssessmentResponse,
) []assessor.SemanticValidationError {
	canonicalByEvidenceID := submissionAssessmentConflictCanonicalEvidence(plan, response)
	seenCases := make(map[string]struct{}, len(response.EvidenceConflictResults))
	var errs []assessor.SemanticValidationError
	for conflictIndex, conflict := range response.EvidenceConflictResults {
		field := fmt.Sprintf("evidence_conflict_results[%d]", conflictIndex)
		seenPositions := make(map[string]struct{}, len(conflict.Positions))
		caseParts := make([]string, 0, len(conflict.Positions))
		for positionIndex, position := range conflict.Positions {
			canonical, ok := canonicalByEvidenceID[position.EvidenceID]
			if !ok {
				continue
			}
			key := fmt.Sprintf("%s:%d:%d", canonical, position.Start, position.End)
			positionField := fmt.Sprintf("%s.positions[%d]", field, positionIndex)
			if _, duplicate := seenPositions[key]; duplicate {
				errs = append(errs, assessor.SemanticValidationError{Field: positionField, Message: "duplicates another canonical position"})
			}
			seenPositions[key] = struct{}{}
			caseParts = append(caseParts, key)
		}
		sort.Strings(caseParts)
		caseKey := strings.Join(caseParts, "|")
		if caseKey == "" {
			continue
		}
		if _, duplicate := seenCases[caseKey]; duplicate {
			errs = append(errs, assessor.SemanticValidationError{Field: field, Message: "duplicates another canonical conflict result"})
		}
		seenCases[caseKey] = struct{}{}
	}
	return errs
}

func submissionAssessmentConflictCanonicalEvidence(
	plan submissionAssessmentPlan,
	response assessor.SemanticAssessmentResponse,
) map[string]string {
	canonical := make(map[string]string, len(plan.Items)+len(plan.knownEvidenceByID))
	for _, item := range plan.Items {
		identity := "submitted:" + item.Fragment.FragmentID
		if exact, ok := plan.exactDuplicateByEvidenceID[item.EvidenceID]; ok && strings.TrimSpace(exact.CandidateFragmentID) != "" {
			identity = "canonical:" + exact.CandidateFragmentID
		} else if item.DuplicateAssessmentRequired {
			identity = "batch:" + item.Fragment.ContentHash + "\x00" + item.Fragment.Content
		}
		canonical[item.EvidenceID] = identity
	}
	for evidenceID, known := range plan.knownEvidenceByID {
		fragmentID := strings.TrimSpace(known.FragmentID)
		if fragmentID == "" {
			fragmentID = evidenceID
		}
		canonical[evidenceID] = "canonical:" + fragmentID
	}
	for _, group := range plan.duplicateCandidatesByEvidenceID {
		for _, candidate := range group.Candidates {
			if _, exists := canonical[candidate.FragmentID]; !exists {
				canonical[candidate.FragmentID] = "canonical:" + candidate.FragmentID
			}
		}
	}
	for _, result := range response.EvidenceEquivalenceResults {
		if result.Action != "reuse" || result.CandidateEvidenceID == nil {
			continue
		}
		candidateID := strings.TrimSpace(*result.CandidateEvidenceID)
		if candidateID == "" {
			continue
		}
		if _, exists := canonical[result.EvidenceID]; exists {
			canonical[result.EvidenceID] = "canonical:" + candidateID
		}
	}
	return canonical
}
