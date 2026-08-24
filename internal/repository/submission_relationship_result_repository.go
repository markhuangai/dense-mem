package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type submissionRelationshipAppliedSplit struct {
	RelationshipRef string
	SplitIndex      int
	Result          RelationshipDecisionResult
}

func insertSubmissionRelationshipResults(
	ctx context.Context,
	tx *gorm.DB,
	scope SubmissionAssessmentRunScope,
	results []SubmissionRelationshipResultInput,
	applied []submissionRelationshipAppliedSplit,
) error {
	if len(results) == 0 && len(applied) == 0 {
		return nil
	}
	byRef := make(map[string]SubmissionRelationshipResultInput, len(results)+len(applied))
	for _, result := range results {
		ref := strings.TrimSpace(result.RelationshipRef)
		if ref == "" {
			return errors.New("submission relationship result ref is required")
		}
		result.RelationshipRef = ref
		byRef[ref] = result
	}
	appliedRefs := make(map[string]struct{}, len(applied))
	for _, appliedSplit := range applied {
		ref := strings.TrimSpace(appliedSplit.RelationshipRef)
		if ref == "" {
			return errors.New("submission relationship applied split ref is required")
		}
		key := fmt.Sprintf("%s:%d", ref, appliedSplit.SplitIndex)
		if _, exists := appliedRefs[key]; exists {
			return fmt.Errorf("submission relationship result %q split_index is duplicated", ref)
		}
		appliedRefs[key] = struct{}{}
		if _, exists := byRef[ref]; !exists {
			return fmt.Errorf("submission relationship result %q is missing", ref)
		}
		result := byRef[ref]
		if result.Disposition != "stored" {
			return fmt.Errorf("submission relationship result %q received a split for %s", ref, result.Disposition)
		}
		if appliedSplit.Result.Relationship == nil {
			return fmt.Errorf("submission relationship result %q split has no Relationship", ref)
		}
		result.Reason = ""
		result.Splits = append(result.Splits, SubmissionRelationshipSplitInput{
			SplitIndex:          appliedSplit.SplitIndex,
			RelationshipID:      appliedSplit.Result.Relationship.RelationshipID,
			RelationshipVersion: appliedSplit.Result.Relationship.Version,
			Status:              appliedSplit.Result.Relationship.Status,
		})
		byRef[ref] = result
	}
	ordered := make([]SubmissionRelationshipResultInput, 0, len(byRef))
	for _, result := range byRef {
		if result.Disposition != "stored" && result.Disposition != "not_stored" {
			return fmt.Errorf("submission relationship result %q has unsupported disposition", result.RelationshipRef)
		}
		if result.Disposition == "stored" && len(result.Splits) == 0 {
			return fmt.Errorf("submission relationship result %q has no stored split", result.RelationshipRef)
		}
		if result.Disposition == "stored" {
			if strings.TrimSpace(result.Reason) != "" {
				return fmt.Errorf("submission relationship result %q stored reason must be empty", result.RelationshipRef)
			}
			sortSubmissionRelationshipSplits(result.Splits)
			for splitIndex := range result.Splits {
				if result.Splits[splitIndex].SplitIndex != splitIndex {
					return fmt.Errorf("submission relationship result %q split_index must be contiguous", result.RelationshipRef)
				}
				if _, err := uuid.Parse(result.Splits[splitIndex].RelationshipID); err != nil {
					return fmt.Errorf("submission relationship result %q relationship_id is invalid: %w", result.RelationshipRef, err)
				}
				if result.Splits[splitIndex].RelationshipVersion < 1 || result.Splits[splitIndex].Status != "active" {
					return fmt.Errorf("submission relationship result %q split is not active and versioned", result.RelationshipRef)
				}
			}
		}
		if result.Disposition == "not_stored" && len(result.Splits) != 0 {
			return fmt.Errorf("submission relationship result %q has not_stored splits", result.RelationshipRef)
		}
		reason := strings.TrimSpace(result.Reason)
		if result.Disposition == "not_stored" && reason != "not_supported_by_evidence" && reason != "stale_input" && reason != "security_quarantine" {
			return fmt.Errorf("submission relationship result %q has unsupported not_stored reason", result.RelationshipRef)
		}
		ordered = append(ordered, result)
	}
	// Stable ordering makes the persisted result deterministic for replay and
	// keeps status inspection independent of map iteration order.
	sortSubmissionRelationshipResults(ordered)
	for _, result := range ordered {
		persistedSplits := result.Splits
		if persistedSplits == nil {
			persistedSplits = []SubmissionRelationshipSplitInput{}
		}
		splits, err := json.Marshal(persistedSplits)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO submission_relationship_results (
			    team_id, ingest_id, placement_run_id, owner_profile_id,
			    relationship_ref, disposition, reason, splits,
			    space_id, space_generation
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?::jsonb,
			    (SELECT space_id FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid),
			    (SELECT space_generation FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid)
			)
		`, scope.TeamID, scope.IngestID, scope.PlacementRunID, scope.OwnerProfileID,
			result.RelationshipRef, result.Disposition, strings.TrimSpace(result.Reason), string(splits),
			scope.TeamID, scope.PlacementRunID, scope.TeamID, scope.PlacementRunID).Error; err != nil {
			return err
		}
	}
	return nil
}

func sortSubmissionRelationshipSplits(splits []SubmissionRelationshipSplitInput) {
	for i := 1; i < len(splits); i++ {
		for j := i; j > 0 && splits[j].SplitIndex < splits[j-1].SplitIndex; j-- {
			splits[j], splits[j-1] = splits[j-1], splits[j]
		}
	}
}

func sortSubmissionRelationshipResults(results []SubmissionRelationshipResultInput) {
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].RelationshipRef < results[j-1].RelationshipRef; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
}
