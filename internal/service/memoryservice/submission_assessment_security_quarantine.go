package memoryservice

import (
	"errors"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func submissionAssessmentDeterministicQuarantines(
	plan submissionAssessmentPlan,
	scan SubmissionSecurityBatchScan,
) ([]repository.SubmissionAssessmentSecurityQuarantineInput, error) {
	if len(plan.Items) == 0 {
		return nil, errors.New("submission assessment security quarantine requires evidence")
	}
	type group struct {
		signals []SubmissionSecurityBatchSignal
	}
	byFragmentID := map[string]*group{}
	proposalSignals := make([]SubmissionSecurityBatchSignal, 0)
	for _, signal := range scan.Signals {
		switch signal.Source {
		case submissionSecuritySourceEvidence:
			item, ok := plan.itemsByEvidenceID[submissionAssessmentEvidenceID(signal.EvidenceIndex)]
			if !ok {
				return nil, errors.New("submission assessment security signal references unknown evidence")
			}
			entry := byFragmentID[item.Fragment.FragmentID]
			if entry == nil {
				entry = &group{}
				byFragmentID[item.Fragment.FragmentID] = entry
			}
			entry.signals = append(entry.signals, signal)
		case submissionSecuritySourceProposal:
			proposalSignals = append(proposalSignals, signal)
		default:
			return nil, errors.New("submission assessment security signal source is unsupported")
		}
	}
	if len(proposalSignals) > 0 {
		for _, item := range plan.Items {
			entry := byFragmentID[item.Fragment.FragmentID]
			if entry == nil {
				entry = &group{}
				byFragmentID[item.Fragment.FragmentID] = entry
			}
			entry.signals = append(entry.signals, proposalSignals...)
		}
	}
	if len(byFragmentID) == 0 {
		byFragmentID[plan.Items[0].Fragment.FragmentID] = &group{}
	}
	quarantines := make([]repository.SubmissionAssessmentSecurityQuarantineInput, 0, len(byFragmentID))
	for _, item := range plan.Items {
		entry := byFragmentID[item.Fragment.FragmentID]
		if entry == nil {
			continue
		}
		signals := make([]SubmissionSecuritySignal, 0, len(entry.signals))
		sources := make([]string, 0, len(entry.signals))
		for _, signal := range entry.signals {
			signals = append(signals, signal.SubmissionSecuritySignal)
			sources = append(sources, signal.Source)
		}
		draft := submissionSecurityQuarantineEventForSignals(signals, scan.SignalsTruncated, sources)
		for index, signal := range entry.signals {
			if index >= len(draft.Signals) {
				continue
			}
			if signal.Source == submissionSecuritySourceProposal {
				draft.Signals[index].Metadata["scope"] = "submission"
				continue
			}
			if signal.Source != submissionSecuritySourceEvidence {
				continue
			}
			quote, err := assessor.SemanticEvidenceSpan(item.Fragment.Content, signal.Start, signal.End)
			if err == nil {
				draft.Signals[index].Quote = quote
			}
		}
		quarantines = append(quarantines, repository.SubmissionAssessmentSecurityQuarantineInput{
			FragmentID:         item.Fragment.FragmentID,
			SecurityEventDraft: draft,
		})
	}
	if len(quarantines) == 0 {
		return nil, errors.New("submission assessment security quarantine has no target")
	}
	return quarantines, nil
}
