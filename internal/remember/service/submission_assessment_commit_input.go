package service

import (
	"errors"
	"fmt"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"sort"
	"strings"
)

func submissionAssessmentCommitInput(
	scope repository.RememberCommitScope,
	plan submissionAssessmentPlan,
	response assessor.SemanticAssessmentResponse,
	assessment *repository.SubmissionAssessment,
	reused bool,
) (repository.CommitSubmissionAssessmentInput, error) {
	if assessment == nil || strings.TrimSpace(assessment.AssessmentID) == "" {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("persisted submission assessment is required before semantic commit")
	}
	usedKnownEvidenceIDs := make(map[string]struct{}, len(plan.knownEvidenceByID))
	items := make([]repository.SubmissionAssessmentItemInput, 0, len(plan.Items))
	for _, item := range plan.Items {
		items = append(items, repository.SubmissionAssessmentItemInput{
			FragmentID: item.Fragment.FragmentID, EvidenceID: item.EvidenceID,
		})
	}
	evidenceConflictResults := make([]repository.EvidenceConflictResultInput, 0, len(response.EvidenceConflictResults))
	for _, conflict := range response.EvidenceConflictResults {
		positions := make([]repository.EvidenceConflictPositionInput, 0, len(conflict.Positions))
		for _, position := range conflict.Positions {
			if _, known := plan.knownEvidenceByID[position.EvidenceID]; known {
				usedKnownEvidenceIDs[position.EvidenceID] = struct{}{}
			}
			positions = append(positions, repository.EvidenceConflictPositionInput{
				EvidenceID: position.EvidenceID,
				Start:      position.Start,
				End:        position.End,
			})
		}
		evidenceConflictResults = append(evidenceConflictResults, repository.EvidenceConflictResultInput{Positions: positions})
	}
	knownEvidenceIDs := make([]string, 0, len(usedKnownEvidenceIDs))
	for evidenceID := range usedKnownEvidenceIDs {
		knownEvidenceIDs = append(knownEvidenceIDs, evidenceID)
	}
	sort.Strings(knownEvidenceIDs)
	knownEvidenceSnapshot := make([]repository.SubmissionAssessmentKnownEvidence, 0, len(knownEvidenceIDs))
	for _, evidenceID := range knownEvidenceIDs {
		knownEvidenceSnapshot = append(knownEvidenceSnapshot, plan.knownEvidenceByID[evidenceID])
	}
	evidenceConflictCandidates := make(map[string][]string, len(plan.duplicateCandidatesByEvidenceID))
	for evidenceID, group := range plan.duplicateCandidatesByEvidenceID {
		ids := make([]string, 0, len(group.Candidates))
		for _, candidate := range group.Candidates {
			ids = append(ids, candidate.FragmentID)
		}
		sort.Strings(ids)
		evidenceConflictCandidates[evidenceID] = ids
	}
	return repository.CommitSubmissionAssessmentInput{
		RememberCommitScope:                  scope,
		AssessmentID:                         assessment.AssessmentID,
		Items:                                items,
		KnownEvidenceSnapshot:                knownEvidenceSnapshot,
		EvidenceConflictResults:              evidenceConflictResults,
		EvidenceConflictCandidateEvidenceIDs: evidenceConflictCandidates,
		Payload: map[string]any{
			"assessor_contract":                    domain.ContractVersion,
			"assessment_id":                        assessment.AssessmentID,
			"model":                                assessment.Model,
			"tokenizer":                            assessment.Tokenizer,
			"input_tokens":                         assessment.InputTokens,
			"output_tokens":                        assessment.OutputTokens,
			"candidate_context_tokens":             assessment.CandidateContextTokens,
			"candidate_context_truncated":          assessment.CandidateContextTruncated,
			"candidate_context_omitted_candidates": assessment.CandidateContextOmittedCandidates,
			"response_hash":                        assessment.ResponseHash,
			"request_id":                           assessment.RequestID,
			"assessment_reused":                    reused,
		},
	}, nil
}

func submissionAssessmentDuplicateResolutions(
	plan submissionAssessmentPlan,
	response assessor.SemanticAssessmentResponse,
) ([]repository.RememberDuplicateResolution, error) {
	resultsByEvidenceID := make(map[string]assessor.SemanticAssessmentEvidenceEquivalenceResult, len(response.EvidenceEquivalenceResults))
	for _, result := range response.EvidenceEquivalenceResults {
		resultsByEvidenceID[strings.TrimSpace(result.EvidenceID)] = result
	}
	groupsByEvidenceID := plan.duplicateCandidatesByEvidenceID
	resolutions := make([]repository.RememberDuplicateResolution, 0, len(plan.Items))
	for _, item := range plan.Items {
		evidenceID := item.EvidenceID
		resolution := repository.RememberDuplicateResolution{
			EvidenceIndex:   item.Fragment.EvidenceIndex,
			EvidenceID:      evidenceID,
			InputFragmentID: item.Fragment.FragmentID,
			Disposition:     "new",
		}
		if exact, ok := plan.exactDuplicateByEvidenceID[evidenceID]; ok {
			resolution.Disposition = "reuse"
			resolution.Exact = true
			resolution.CandidateFragmentID = exact.CandidateFragmentID
			resolution.CandidateOwnerID = exact.CandidateOwnerID
			resolutions = append(resolutions, resolution)
			continue
		}
		group, ok := groupsByEvidenceID[evidenceID]
		if !ok {
			if item.DuplicateAssessmentRequired {
				return nil, fmt.Errorf("submission assessor duplicate candidate group is missing for %s", evidenceID)
			}
			// Force-insert and lifecycle-bearing evidence is intentionally absent
			// from the equivalence allowlist and is always a new occurrence.
			resolutions = append(resolutions, resolution)
			continue
		}
		result, ok := resultsByEvidenceID[evidenceID]
		if !ok {
			return nil, fmt.Errorf("submission assessor duplicate result is missing for %s", evidenceID)
		}
		if result.Action != "reuse" {
			resolutions = append(resolutions, resolution)
			continue
		}
		candidateID := ""
		if result.CandidateEvidenceID != nil {
			candidateID = strings.TrimSpace(*result.CandidateEvidenceID)
		}
		for _, candidate := range group.Candidates {
			if candidate.FragmentID == candidateID {
				resolution.Disposition = "reuse"
				resolution.CandidateFragmentID = candidate.FragmentID
				resolution.CandidateOwnerID = candidate.OwnerProfileID
				break
			}
		}
		if resolution.Disposition != "reuse" {
			return nil, fmt.Errorf("submission assessor duplicate result selected an unknown candidate for %s", evidenceID)
		}
		resolutions = append(resolutions, resolution)
	}
	sort.Slice(resolutions, func(i, j int) bool { return resolutions[i].EvidenceIndex < resolutions[j].EvidenceIndex })
	return resolutions, nil
}
