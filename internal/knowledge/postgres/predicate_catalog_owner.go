package postgres

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

// EnsureSemanticReviewPredicateCandidate creates or expands a team-owned
// predicate candidate using the same catalog transaction used by semantic
// placement. The method remains on Knowledge because that package owns the
// predicate catalog and its lifecycle policy.
func (r *Store) EnsureSemanticReviewPredicateCandidate(ctx context.Context, input EnsureSemanticPredicateCandidateInput) (*SemanticReviewPredicateCandidate, error) {
	input = knowledgecontract.NormalizeEnsureSemanticPredicateCandidateInput(input)
	if err := knowledgecontract.ValidateEnsureSemanticPredicateCandidateInput(input); err != nil {
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
