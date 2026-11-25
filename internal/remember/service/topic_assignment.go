package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	assignment "github.com/markhuangai/dense-mem/internal/topics/assignment"
)

func (p *rememberSynchronousProcessor) prepareTopics(ctx context.Context, space domain.MemorySpaceKind, commit *knowledge.SynchronousRememberCommitInput, candidates []knowledge.RememberDuplicateCandidateGroup, settings *domain.DecisionRuntimeConfig) error {
	if space != domain.MemorySpaceTeamShared || p.topicAssignment == nil {
		return nil
	}
	byCanonical := map[string]string{}
	for _, group := range candidates {
		for _, candidate := range group.Candidates {
			byCanonical[candidate.FragmentID] = candidate.Content
		}
	}
	slices := make([]assignment.Slice, 0, len(commit.Evidence))
	semanticReuse := map[int]string{}
	for _, r := range commit.DuplicateResolutions {
		if r.Disposition == "reuse" && !r.Exact {
			text, ok := byCanonical[r.CandidateFragmentID]
			if !ok {
				return fmt.Errorf("%w: assignment canonical duplicate absent", ErrRememberStaleInput)
			}
			semanticReuse[r.EvidenceIndex] = text
		}
	}
	for index, item := range commit.Evidence {
		if item.InitialEvent != nil && item.InitialEvent.Decision == "quarantine" {
			continue
		}
		text := item.Content
		if canonical, ok := semanticReuse[index]; ok {
			text = canonical
		}
		slices = append(slices, assignment.Slice{Index: index, Text: text})
	}
	ctx, cancel := ContextForPhase(ctx, RememberPhaseAssessment)
	defer cancel()
	results, err := p.topicAssignment.Prepare(ctx, assignment.Scope{TeamID: commit.TeamID, SpaceID: commit.SpaceID, Generation: commit.SpaceGeneration, OperationID: commit.IngestID}, slices, settings)
	var malformed *modelprovider.MalformedResponseError
	switch {
	case errors.Is(err, assignment.ErrStorage):
		return fmt.Errorf("%w: %w", ErrRememberDatabaseFailure, err)
	case errors.As(err, &malformed) && malformed.FailureClass == "input_budget":
		return fmt.Errorf("%w: %w", ErrRememberInputBudgetExceeded, err)
	case errors.Is(err, modelprovider.ErrVerifierMalformedResponse):
		return fmt.Errorf("%w: %w", ErrRememberProviderResponseInvalid, err)
	case err != nil:
		return err
	}
	commit.TopicAssignments = results
	return nil
}
