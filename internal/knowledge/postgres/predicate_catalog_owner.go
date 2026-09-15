package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// EnsureSemanticReviewPredicateCandidate creates or expands a team-owned
// predicate candidate using the same catalog transaction used by semantic
// placement. The method remains on Knowledge because that package owns the
// predicate catalog and its lifecycle policy.
func (r *Store) EnsureSemanticReviewPredicateCandidate(ctx context.Context, input EnsureSemanticPredicateCandidateInput) (*SemanticReviewPredicateCandidate, error) {
	input = normalizeEnsureSemanticPredicateCandidateInput(input)
	if err := validateEnsureSemanticPredicateCandidateInput(input); err != nil {
		return nil, err
	}
	var candidate *SemanticReviewPredicateCandidate
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		var err error
		candidate, err = ensureSemanticPredicateCandidateTx(ctx, tx, input)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge: ensure review predicate candidate: %w", err)
	}
	return candidate, nil
}

func normalizeEnsureSemanticPredicateCandidateInput(input EnsureSemanticPredicateCandidateInput) EnsureSemanticPredicateCandidateInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Predicate = strings.TrimSpace(input.Predicate)
	input.RelationshipKind = strings.TrimSpace(input.RelationshipKind)
	input.SubjectKind = strings.TrimSpace(input.SubjectKind)
	input.ObjectKind = strings.TrimSpace(input.ObjectKind)
	input.Origin = strings.TrimSpace(input.Origin)
	if input.Origin == "" {
		input.Origin = "provider_generated"
	}
	if input.SubjectKind == "" {
		input.SubjectKind = string(domain.EntityKindOther)
	}
	if input.ObjectKind == "" {
		input.ObjectKind = string(domain.EntityKindOther)
	}
	return input
}

func validateEnsureSemanticPredicateCandidateInput(input EnsureSemanticPredicateCandidateInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.Predicate == "" {
		return errors.New("predicate is required")
	}
	if !contains(domain.RelationshipKinds(), input.RelationshipKind) {
		return fmt.Errorf("relationship_kind is unsupported %q", input.RelationshipKind)
	}
	if !contains(domain.EntityKinds(), input.SubjectKind) {
		return fmt.Errorf("subject_kind is unsupported %q", input.SubjectKind)
	}
	if !contains(append(domain.EntityKinds(), domain.ValueTypes()...), input.ObjectKind) {
		return fmt.Errorf("object_kind is unsupported %q", input.ObjectKind)
	}
	return nil
}
