package postgres

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func normalizeRememberCommitScope(scope RememberCommitScope) RememberCommitScope {
	scope.TeamID = strings.TrimSpace(scope.TeamID)
	scope.OwnerProfileID = strings.TrimSpace(scope.OwnerProfileID)
	scope.IngestID = strings.TrimSpace(scope.IngestID)
	return scope
}

func validateRememberCommitScope(scope RememberCommitScope) error {
	for label, value := range map[string]string{
		"team_id": scope.TeamID, "owner_profile_id": scope.OwnerProfileID,
		"ingest_id": scope.IngestID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	return nil
}

func normalizeCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) CommitSubmissionAssessmentInput {
	input.RememberCommitScope = normalizeRememberCommitScope(input.RememberCommitScope)
	input.AssessmentID = strings.TrimSpace(input.AssessmentID)
	for i := range input.Items {
		input.Items[i].FragmentID = strings.TrimSpace(input.Items[i].FragmentID)
		input.Items[i].EvidenceID = strings.TrimSpace(input.Items[i].EvidenceID)
	}
	for i := range input.KnownEvidenceSnapshot {
		item := &input.KnownEvidenceSnapshot[i]
		item.TeamID = strings.TrimSpace(item.TeamID)
		item.EvidenceID = strings.TrimSpace(item.EvidenceID)
		item.FragmentID = strings.TrimSpace(item.FragmentID)
		item.IngestID = strings.TrimSpace(item.IngestID)
		item.OwnerProfileID = strings.TrimSpace(item.OwnerProfileID)
		item.ContentHash = strings.TrimSpace(item.ContentHash)
		item.Authority = strings.TrimSpace(item.Authority)
		item.SourceID = strings.TrimSpace(item.SourceID)
		item.SourceRevisionID = strings.TrimSpace(item.SourceRevisionID)
		item.CurrentSourceRevisionID = strings.TrimSpace(item.CurrentSourceRevisionID)
		item.SpaceID = strings.TrimSpace(item.SpaceID)
	}
	for i := range input.EvidenceConflictResults {
		for j := range input.EvidenceConflictResults[i].Positions {
			input.EvidenceConflictResults[i].Positions[j].EvidenceID = strings.TrimSpace(input.EvidenceConflictResults[i].Positions[j].EvidenceID)
		}
	}
	if input.EvidenceConflictCandidateEvidenceIDs != nil {
		normalizedCandidates := make(map[string][]string, len(input.EvidenceConflictCandidateEvidenceIDs))
		for evidenceID, candidates := range input.EvidenceConflictCandidateEvidenceIDs {
			evidenceID = strings.TrimSpace(evidenceID)
			values := make([]string, 0, len(candidates))
			seen := make(map[string]struct{}, len(candidates))
			for _, candidateID := range candidates {
				candidateID = strings.TrimSpace(candidateID)
				if candidateID == "" {
					continue
				}
				if _, exists := seen[candidateID]; exists {
					continue
				}
				seen[candidateID] = struct{}{}
				values = append(values, candidateID)
			}
			sort.Strings(values)
			normalizedCandidates[evidenceID] = values
		}
		input.EvidenceConflictCandidateEvidenceIDs = normalizedCandidates
	}
	return input
}

func validateCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) error {
	if err := validateRememberCommitScope(input.RememberCommitScope); err != nil {
		return err
	}
	if err := validateKnownEvidenceSnapshots(input); err != nil {
		return err
	}
	if _, err := uuid.Parse(input.AssessmentID); err != nil {
		if len(input.Items) > 0 {
			return fmt.Errorf("assessment_id is required: %w", err)
		}
	}
	if len(input.Items) == 0 {
		if len(input.KnownEvidenceSnapshot) != 0 {
			return errors.New("terminal synchronous result cannot carry known evidence")
		}
		if len(input.EvidenceConflictResults) != 0 {
			return errors.New("terminal synchronous result cannot carry evidence conflicts")
		}
		return nil
	}
	seenItems := map[string]struct{}{}
	seenEvidenceIDs := map[string]struct{}{}
	for _, item := range input.Items {
		if _, err := uuid.Parse(item.FragmentID); err != nil {
			return fmt.Errorf("fragment_id is required: %w", err)
		}
		if _, exists := seenItems[item.FragmentID]; exists {
			return errors.New("submission assessment item identity is duplicated")
		}
		seenItems[item.FragmentID] = struct{}{}
		if item.EvidenceID != "" {
			if _, exists := seenEvidenceIDs[item.EvidenceID]; exists {
				return errors.New("submission assessment evidence identity is duplicated")
			}
			seenEvidenceIDs[item.EvidenceID] = struct{}{}
		}
	}
	if len(input.EvidenceConflictResults) > EvidenceConflictMaxResults {
		return fmt.Errorf("submission evidence conflicts must contain at most %d results", EvidenceConflictMaxResults)
	}
	for evidenceID, candidates := range input.EvidenceConflictCandidateEvidenceIDs {
		if strings.TrimSpace(evidenceID) == "" {
			return errors.New("submission evidence conflict candidate evidence_id is required")
		}
		for _, candidateID := range candidates {
			if _, err := uuid.Parse(candidateID); err != nil {
				return fmt.Errorf("submission evidence conflict candidate %q is invalid: %w", candidateID, err)
			}
		}
	}
	for conflictIndex, conflict := range input.EvidenceConflictResults {
		if len(conflict.Positions) < 2 || len(conflict.Positions) > EvidenceConflictMaxPositions {
			return fmt.Errorf("submission evidence conflict[%d] must contain between 2 and %d positions", conflictIndex, EvidenceConflictMaxPositions)
		}
		seenPositions := make(map[string]struct{}, len(conflict.Positions))
		for positionIndex, position := range conflict.Positions {
			if strings.TrimSpace(position.EvidenceID) == "" || position.Start < 0 || position.End <= position.Start {
				return fmt.Errorf("submission evidence conflict[%d] position[%d] is invalid", conflictIndex, positionIndex)
			}
			key := fmt.Sprintf("%s:%d:%d", position.EvidenceID, position.Start, position.End)
			if _, exists := seenPositions[key]; exists {
				return fmt.Errorf("submission evidence conflict[%d] position[%d] is duplicated", conflictIndex, positionIndex)
			}
			seenPositions[key] = struct{}{}
		}
	}
	return nil
}

func validateKnownEvidenceSnapshots(input CommitSubmissionAssessmentInput) error {
	seen := make(map[string]struct{}, len(input.KnownEvidenceSnapshot))
	for index, item := range input.KnownEvidenceSnapshot {
		if item.TeamID != input.TeamID {
			return fmt.Errorf("known evidence snapshot[%d] team_id does not match the Remember team", index)
		}
		for label, value := range map[string]string{
			"evidence_id": item.EvidenceID, "fragment_id": item.FragmentID,
			"ingest_id": item.IngestID, "owner_profile_id": item.OwnerProfileID,
		} {
			if _, err := uuid.Parse(value); err != nil {
				return fmt.Errorf("known evidence snapshot[%d] %s is required: %w", index, label, err)
			}
		}
		if item.EvidenceID != item.FragmentID {
			return fmt.Errorf("known evidence snapshot[%d] evidence_id and fragment_id must match", index)
		}
		if _, exists := seen[item.EvidenceID]; exists {
			return errors.New("known evidence snapshot identity is duplicated")
		}
		seen[item.EvidenceID] = struct{}{}
		if item.Content == "" || item.ContentHash == "" {
			return fmt.Errorf("known evidence snapshot[%d] content and content_hash are required", index)
		}
		if !domain.Authority(item.Authority).IsValid() {
			return fmt.Errorf("known evidence snapshot[%d] authority is unsupported", index)
		}
		if _, err := uuid.Parse(item.SpaceID); err != nil {
			return fmt.Errorf("known evidence snapshot[%d] space_id is required: %w", index, err)
		}
		if item.SpaceGeneration < 1 {
			return fmt.Errorf("known evidence snapshot[%d] space_generation must be positive", index)
		}
		if (item.SourceID == "") != (item.SourceRevisionID == "") {
			return fmt.Errorf("known evidence snapshot[%d] source and source revision must be provided together", index)
		}
		if item.SourceID != "" {
			if _, err := uuid.Parse(item.SourceID); err != nil {
				return fmt.Errorf("known evidence snapshot[%d] source_id is invalid: %w", index, err)
			}
			if _, err := uuid.Parse(item.SourceRevisionID); err != nil {
				return fmt.Errorf("known evidence snapshot[%d] source_revision_id is invalid: %w", index, err)
			}
			if item.CurrentSourceRevisionID == "" || item.CurrentSourceRevisionID != item.SourceRevisionID {
				return fmt.Errorf("known evidence snapshot[%d] source revision is not current", index)
			}
			if _, err := uuid.Parse(item.CurrentSourceRevisionID); err != nil {
				return fmt.Errorf("known evidence snapshot[%d] current_source_revision_id is invalid: %w", index, err)
			}
		} else if item.CurrentSourceRevisionID != "" {
			return fmt.Errorf("known evidence snapshot[%d] current source revision requires a source", index)
		}
	}
	return nil
}

func cloneSubmissionPayload(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
