package memoryservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type semanticConflictContextValidator interface {
	ValidateRelationshipConflictContext(ctx context.Context, input repository.ValidateRelationshipConflictContextInput) error
}

func (s *semanticPlacementReviewSource) validateReviewSourceConflictContexts(
	ctx context.Context,
	run repository.PlacementRun,
	contexts []reviewSourceConflictContext,
) []verifier.SemanticValidationError {
	if len(contexts) == 0 {
		return nil
	}
	validator, ok := s.ledger.(semanticConflictContextValidator)
	if !ok {
		return nil
	}
	out := make([]verifier.SemanticValidationError, 0)
	for _, item := range contexts {
		err := validator.ValidateRelationshipConflictContext(ctx, repository.ValidateRelationshipConflictContextInput{
			TeamID:          run.TeamID,
			OwnerProfileID:  run.OwnerProfileID,
			ConflictID:      item.Context.ConflictID,
			ExpectedVersion: item.Context.ExpectedVersion,
		})
		if err == nil {
			continue
		}
		message := "conflict context could not be validated"
		if errors.Is(err, repository.ErrConflictContextStale) {
			message = "conflict context is stale or closed"
		}
		out = append(out, verifier.SemanticValidationError{
			Field:   fmt.Sprintf("relationship_hints[%d].conflict_context", item.Index),
			Message: message,
		})
	}
	return out
}

func reviewSourceConflictContextShapeErrors(proposal map[string]any) []verifier.SemanticValidationError {
	relationships := placementReviewObjectArray(proposal, "relationship_hints", "relationships")
	out := make([]verifier.SemanticValidationError, 0)
	for i, raw := range relationships {
		if _, exists := raw["conflict_context"]; !exists {
			continue
		}
		if _, ok := placementReviewConflictContext(raw); ok {
			continue
		}
		out = append(out, verifier.SemanticValidationError{
			Field:   fmt.Sprintf("relationship_hints[%d].conflict_context", i),
			Message: "must include conflict_id and expected_version",
		})
	}
	return out
}

func placementReviewConflictContext(raw map[string]any) (verifier.RelationshipConflictContext, bool) {
	context, ok := reviewMap(raw["conflict_context"])
	if !ok {
		return verifier.RelationshipConflictContext{}, false
	}
	conflictID := reviewString(context, "conflict_id")
	expectedVersion, ok := reviewInt(context, "expected_version")
	if conflictID == "" || !ok {
		return verifier.RelationshipConflictContext{}, false
	}
	return verifier.RelationshipConflictContext{
		ConflictID:      conflictID,
		ExpectedVersion: expectedVersion,
	}, true
}
