package service

import (
	"errors"
	"fmt"
	"github.com/markhuangai/dense-mem/internal/assessor"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"sort"
	"strings"
)

type submissionAssessmentItem struct {
	ItemID                      string
	Fragment                    repository.EvidenceFragment
	EvidenceID                  string
	DuplicateAssessmentRequired bool
	ExactReuseEligible          bool
}

type submissionAssessmentPlan struct {
	Items                           []submissionAssessmentItem
	itemsByEvidenceID               map[string]submissionAssessmentItem
	knownEvidenceByID               map[string]repository.SubmissionAssessmentKnownEvidence
	duplicateCandidatesByEvidenceID map[string]repository.RememberDuplicateCandidateGroup
	exactDuplicateByEvidenceID      map[string]repository.RememberDuplicateResolution
}

func buildSubmissionAssessmentPlan(snapshot RememberAssessmentSnapshot) (submissionAssessmentPlan, error) {
	fragmentsByIndex := make(map[int]repository.EvidenceFragment, len(snapshot.Evidence))
	for _, fragment := range snapshot.Evidence {
		if strings.TrimSpace(fragment.FragmentID) == "" || strings.TrimSpace(fragment.Content) == "" {
			return submissionAssessmentPlan{}, errors.New("submission assessment evidence is incomplete")
		}
		if _, exists := fragmentsByIndex[fragment.EvidenceIndex]; exists {
			return submissionAssessmentPlan{}, errors.New("submission assessment evidence index is duplicated")
		}
		fragmentsByIndex[fragment.EvidenceIndex] = fragment
	}
	if len(fragmentsByIndex) == 0 {
		return submissionAssessmentPlan{}, errors.New("submission assessment evidence is required")
	}
	maxEvidence := snapshot.MaxEvidenceItems
	if maxEvidence <= 0 {
		maxEvidence = assessor.SemanticAssessmentMaxEvidenceSpans
	}
	if maxEvidence > 100 {
		return submissionAssessmentPlan{}, errors.New("submission assessment evidence limit exceeds server bound")
	}
	if len(fragmentsByIndex) > maxEvidence {
		return submissionAssessmentPlan{}, fmt.Errorf("submission assessment evidence must contain at most %d entries", maxEvidence)
	}

	items := make([]submissionAssessmentItem, 0, len(snapshot.Items))
	itemsByEvidenceID := make(map[string]submissionAssessmentItem, len(snapshot.Items))
	for _, item := range snapshot.Items {
		fragment, ok := fragmentsByIndex[item.Fragment.EvidenceIndex]
		if !ok || item.Fragment.FragmentID != fragment.FragmentID || strings.TrimSpace(item.ItemID) == "" {
			return submissionAssessmentPlan{}, errors.New("submission assessment evidence item does not match evidence")
		}
		evidenceID := submissionAssessmentEvidenceID(item.Fragment.EvidenceIndex)
		if _, exists := itemsByEvidenceID[evidenceID]; exists {
			return submissionAssessmentPlan{}, errors.New("submission assessment evidence index is duplicated")
		}
		entry := submissionAssessmentItem{
			ItemID: item.ItemID, Fragment: fragment, EvidenceID: evidenceID,
			DuplicateAssessmentRequired: item.DuplicateAssessmentRequired,
			ExactReuseEligible:          item.ExactReuseEligible,
		}
		items = append(items, entry)
		itemsByEvidenceID[evidenceID] = entry
	}
	if len(items) != len(fragmentsByIndex) {
		return submissionAssessmentPlan{}, errors.New("submission assessment must include every staged evidence item")
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Fragment.EvidenceIndex < items[j].Fragment.EvidenceIndex
	})

	plan := submissionAssessmentPlan{
		Items:                           items,
		itemsByEvidenceID:               itemsByEvidenceID,
		knownEvidenceByID:               map[string]repository.SubmissionAssessmentKnownEvidence{},
		duplicateCandidatesByEvidenceID: map[string]repository.RememberDuplicateCandidateGroup{},
		exactDuplicateByEvidenceID:      map[string]repository.RememberDuplicateResolution{},
	}
	for index, resolution := range snapshot.ExactDuplicateEvidence {
		if index < 0 || index >= len(snapshot.Evidence) {
			return submissionAssessmentPlan{}, errors.New("submission assessment exact duplicate index is invalid")
		}
		resolution.EvidenceIndex = index
		resolution.EvidenceID = submissionAssessmentEvidenceID(index)
		plan.exactDuplicateByEvidenceID[resolution.EvidenceID] = resolution
	}
	for _, group := range snapshot.DuplicateCandidates {
		if group.EvidenceIndex < 0 || group.EvidenceIndex >= len(snapshot.Evidence) {
			return submissionAssessmentPlan{}, errors.New("submission assessment duplicate candidate index is invalid")
		}
		group.EvidenceID = submissionAssessmentEvidenceID(group.EvidenceIndex)
		if _, exists := plan.duplicateCandidatesByEvidenceID[group.EvidenceID]; exists {
			return submissionAssessmentPlan{}, errors.New("submission assessment duplicate candidate index is duplicated")
		}
		plan.duplicateCandidatesByEvidenceID[group.EvidenceID] = group
	}
	return plan, nil
}

func submissionAssessmentEvidenceID(index int) string {
	return fmt.Sprintf("evidence:%d", index)
}
