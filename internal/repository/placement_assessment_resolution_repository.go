package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func validatePlacementAssessmentSelection(
	ctx context.Context,
	tx *gorm.DB,
	input ResolvePlacementReviewInput,
	scope placementResolutionScope,
) error {
	if scope.PlacementItemID == "" {
		return nil
	}
	switch domain.ResolveAction(input.Action) {
	case domain.ResolveSelectEntity:
		return validatePlacementAssessmentEntitySelection(ctx, tx, input, scope)
	case domain.ResolveSelectPredicate:
		return validatePlacementAssessmentPredicateSelection(ctx, tx, input, scope)
	default:
		return nil
	}
}

func validatePlacementAssessmentEntitySelection(
	ctx context.Context,
	tx *gorm.DB,
	input ResolvePlacementReviewInput,
	scope placementResolutionScope,
) error {
	var taskCount int
	if err := tx.WithContext(ctx).Raw(`
		SELECT count(*)
		FROM review_tasks
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_item_id = ?::uuid
		  AND assessment_id IS NOT NULL
		  AND payload->>'semantic_kind' = 'identity'
		  AND payload->>'mention_ref' = ?
	`, input.TeamID, input.OwnerProfileID, scope.PlacementItemID, input.EntityRef).Row().Scan(&taskCount); err != nil {
		return err
	}
	if taskCount == 0 {
		return nil
	}
	var validCount int
	if err := tx.WithContext(ctx).Raw(`
		SELECT count(*)
		FROM review_tasks
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_item_id = ?::uuid
		  AND assessment_id IS NOT NULL
		  AND status IN ('open', 'acknowledged')
		  AND expires_at > now()
		  AND payload->>'semantic_kind' = 'identity'
		  AND payload->>'mention_ref' = ?
		  AND payload->'options' @> jsonb_build_array(jsonb_build_object('entity_id', ?::text))
	`, input.TeamID, input.OwnerProfileID, scope.PlacementItemID, input.EntityRef, input.CandidateEntityID).Row().Scan(&validCount); err != nil {
		return err
	}
	if validCount != 1 {
		return fmt.Errorf("%w: selected entity is not an open server-supplied option", ErrPlacementResolutionInvalidState)
	}
	return nil
}

func validatePlacementAssessmentPredicateSelection(
	ctx context.Context,
	tx *gorm.DB,
	input ResolvePlacementReviewInput,
	scope placementResolutionScope,
) error {
	var taskCount int
	if err := tx.WithContext(ctx).Raw(`
		SELECT count(*)
		FROM review_tasks
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_item_id = ?::uuid
		  AND assessment_id IS NOT NULL
		  AND observation_id = ?::uuid
		  AND payload->>'semantic_kind' = 'predicate'
	`, input.TeamID, input.OwnerProfileID, scope.PlacementItemID, input.ObservationID).Row().Scan(&taskCount); err != nil {
		return err
	}
	if taskCount == 0 {
		return nil
	}
	var validCount int
	if err := tx.WithContext(ctx).Raw(`
		SELECT count(*)
		FROM review_tasks
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_item_id = ?::uuid
		  AND assessment_id IS NOT NULL
		  AND observation_id = ?::uuid
		  AND status IN ('open', 'acknowledged')
		  AND expires_at > now()
		  AND payload->>'semantic_kind' = 'predicate'
		  AND payload->'options' @> jsonb_build_array(
		      jsonb_build_object('predicate_key', ?::text, 'version', ?::integer)
		  )
	`, input.TeamID, input.OwnerProfileID, scope.PlacementItemID, input.ObservationID,
		input.PredicateKey, input.PredicateVersion).Row().Scan(&validCount); err != nil {
		return err
	}
	if validCount != 1 {
		return fmt.Errorf("%w: selected predicate is not an open server-supplied option", ErrPlacementResolutionInvalidState)
	}
	return nil
}

func appendPlacementResolutionItems(
	ctx context.Context,
	tx *gorm.DB,
	input ResolvePlacementReviewInput,
	scope placementResolutionScope,
	fragments []EvidenceFragment,
) ([]PlacementItem, error) {
	switch domain.ResolveAction(input.Action) {
	case domain.ResolveAccept, domain.ResolveCorrect, domain.ResolveConfirmNewEntity:
	default:
		return nil, nil
	}
	if len(fragments) == 0 {
		return nil, errors.New("new evidence placement item is required")
	}
	normalized := normalizeCreateIngestInput(CreateIngestInput{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		Evidence:       input.Evidence,
	})
	if len(normalized.Evidence) != len(fragments) {
		return nil, errors.New("new evidence fragments do not match placement inputs")
	}
	items := make([]PlacementItem, 0, len(fragments))
	createInput := CreateIngestInput{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		Status:         string(domain.PlacementRunQueued),
	}
	for index, fragment := range fragments {
		item, err := insertPlacementItem(ctx, tx, createInput, input.IngestID, scope.PlacementRunID, fragment, normalized.Evidence[index])
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func placementResolutionEvidenceIDs(fragments []EvidenceFragment) []string {
	ids := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		ids = append(ids, fragment.FragmentID)
	}
	return ids
}

func placementResolutionItemIDs(items []PlacementItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.PlacementItemID)
	}
	return ids
}
