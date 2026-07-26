package memoryservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type semanticConflictContextValidator interface {
	ValidateV2RelationshipConflictContext(ctx context.Context, input repository.V2ValidateRelationshipConflictContextInput) error
}

func (s *semanticPlacementReviewSource) v2ValidateReviewSourceConflictContexts(
	ctx context.Context,
	run repository.V2PlacementRun,
	contexts []v2ReviewSourceConflictContext,
) []verifier.V2SemanticValidationError {
	if len(contexts) == 0 {
		return nil
	}
	validator, ok := s.ledger.(semanticConflictContextValidator)
	if !ok {
		return nil
	}
	out := make([]verifier.V2SemanticValidationError, 0)
	for _, item := range contexts {
		err := validator.ValidateV2RelationshipConflictContext(ctx, repository.V2ValidateRelationshipConflictContextInput{
			TeamID:          run.TeamID,
			OwnerProfileID:  run.OwnerProfileID,
			ConflictID:      item.Context.ConflictID,
			ExpectedVersion: item.Context.ExpectedVersion,
		})
		if err == nil {
			continue
		}
		message := "conflict context could not be validated"
		if errors.Is(err, repository.ErrV2ConflictContextStale) {
			message = "conflict context is stale or closed"
		}
		out = append(out, verifier.V2SemanticValidationError{
			Field:   fmt.Sprintf("relationship_hints[%d].conflict_context", item.Index),
			Message: message,
		})
	}
	return out
}

func v2ReviewSourceConflictContextShapeErrors(proposal map[string]any) []verifier.V2SemanticValidationError {
	relationships := v2PlacementReviewObjectArray(proposal, "relationship_hints", "relationships")
	out := make([]verifier.V2SemanticValidationError, 0)
	for i, raw := range relationships {
		if _, exists := raw["conflict_context"]; !exists {
			continue
		}
		if _, ok := v2PlacementReviewConflictContext(raw); ok {
			continue
		}
		out = append(out, verifier.V2SemanticValidationError{
			Field:   fmt.Sprintf("relationship_hints[%d].conflict_context", i),
			Message: "must include conflict_id and expected_version",
		})
	}
	return out
}

func v2PlacementReviewConflictContext(raw map[string]any) (verifier.V2RelationshipConflictContext, bool) {
	context, ok := v2ReviewMap(raw["conflict_context"])
	if !ok {
		return verifier.V2RelationshipConflictContext{}, false
	}
	conflictID := v2ReviewString(context, "conflict_id")
	expectedVersion, ok := v2ReviewInt(context, "expected_version")
	if conflictID == "" || !ok {
		return verifier.V2RelationshipConflictContext{}, false
	}
	return verifier.V2RelationshipConflictContext{
		ConflictID:      conflictID,
		ExpectedVersion: expectedVersion,
	}, true
}
