package factservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"gorm.io/gorm"
)

// ConfirmMemory applies an explicit user clarification to a disputed claim.
//
// Supported decisions:
//   - accept_claim: supersede active conflicting facts for the claim's
//     (subject, predicate), then reuse an existing same-object team Fact when
//     available or create a new active Fact from the claim.
//   - keep_existing or reject_claim: reject the claim and keep active facts.
func (s *promoteClaimServiceImpl) ConfirmMemory(ctx context.Context, profileID string, req ConfirmMemoryRequest) (*ConfirmMemoryResult, error) {
	if req.ClaimID == "" {
		return nil, errors.New("confirm memory: claim_id is required")
	}

	var out *ConfirmMemoryResult
	lockErr := s.locker.WithClaimLock(ctx, s.pgDB, profileID, req.ClaimID, s.lockTimeout, func(_ *gorm.DB) error {
		result, err := s.doConfirmMemory(ctx, profileID, req)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if lockErr != nil {
		s.incMetric(ctx, "error")
		return nil, lockErr
	}
	return out, nil
}

func (s *promoteClaimServiceImpl) doConfirmMemory(ctx context.Context, profileID string, req ConfirmMemoryRequest) (*ConfirmMemoryResult, error) {
	claim, err := s.loadClaim(ctx, profileID, req.ClaimID)
	if err != nil {
		return nil, err
	}
	if err := ownership.RequireOwner(ctx, claim.OwnerProfileID); err != nil {
		return nil, err
	}

	switch req.Decision {
	case "accept_claim":
		return s.confirmAcceptClaim(ctx, profileID, req, claim)
	case "keep_existing", "reject_claim":
		if err := weakerPath(ctx, s.db, profileID, claim.ClaimID); err != nil {
			return nil, fmt.Errorf("confirm memory: reject claim: %w", err)
		}
		s.emitAudit(ctx, profileID, claim.ClaimID, "", "claim.confirm.reject")
		return &ConfirmMemoryResult{
			ClaimID:  claim.ClaimID,
			Decision: req.Decision,
			Status:   "rejected",
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrInvalidConfirmationDecision, req.Decision)
	}
}

func (s *promoteClaimServiceImpl) confirmAcceptClaim(ctx context.Context, profileID string, req ConfirmMemoryRequest, claim *domain.Claim) (*ConfirmMemoryResult, error) {
	gate, ok := DefaultPromotionGates[claim.Predicate]
	if !ok {
		return nil, fmt.Errorf("%w: predicate=%s", ErrPredicateNotPoliced, claim.Predicate)
	}
	if gateErr := evaluateGates(claim, gate); gateErr != nil {
		return nil, gateErr
	}

	activeFacts, err := findActiveFactsBySubjectPredicate(ctx, s.db, profileID, claim.Subject, claim.Predicate)
	if err != nil {
		return nil, fmt.Errorf("confirm memory: find active facts: %w", err)
	}

	conflicts := make([]*domain.Fact, 0, len(activeFacts))
	alignments := make([]*domain.Fact, 0, len(activeFacts))
	for _, fact := range activeFacts {
		if fact.Object != claim.Object {
			conflicts = append(conflicts, fact)
		} else {
			alignments = append(alignments, fact)
		}
	}

	if actorOwnerID := ownership.ActorOwnerID(ctx); actorOwnerID != "" {
		return s.confirmAcceptClaimForOwner(ctx, profileID, req, claim, gate, conflicts, alignments, actorOwnerID)
	}

	if len(conflicts) > 0 {
		if err := supersedePath(ctx, s.db, profileID, conflicts, claim.ClaimID, claim.ValidFrom); err != nil {
			return nil, fmt.Errorf("confirm memory: supersede conflicts: %w", err)
		}
	}

	fact, err := s.createNewFact(ctx, profileID, claim, gate)
	if err != nil {
		return nil, err
	}
	s.incMetric(ctx, "promoted")
	s.recordPromotionFunnelLatency(ctx, claim, "promoted")
	s.emitAudit(ctx, profileID, claim.ClaimID, fact.FactID, "claim.confirm.accept")
	return &ConfirmMemoryResult{
		ClaimID:  claim.ClaimID,
		Decision: req.Decision,
		Status:   "accepted",
		Fact:     fact,
	}, nil
}

func (s *promoteClaimServiceImpl) confirmAcceptClaimForOwner(
	ctx context.Context,
	profileID string,
	req ConfirmMemoryRequest,
	claim *domain.Claim,
	gate PromotionGate,
	conflicts []*domain.Fact,
	alignments []*domain.Fact,
	actorOwnerID string,
) (*ConfirmMemoryResult, error) {
	ownConflicts, foreignConflicts := splitFactsByOwner(conflicts, actorOwnerID)
	ownAlignments, foreignAlignments := splitFactsByOwner(alignments, actorOwnerID)
	if len(ownConflicts) > 0 {
		if err := supersedePath(ctx, s.db, profileID, ownConflicts, claim.ClaimID, claim.ValidFrom); err != nil {
			return nil, fmt.Errorf("confirm memory: supersede owner conflicts: %w", err)
		}
	}
	if len(foreignConflicts) == 0 {
		if len(ownAlignments) > 0 {
			if err := sameObjectConfirmPath(ctx, s.db, profileID, ownAlignments); err != nil {
				return nil, fmt.Errorf("confirm memory: same-object owner confirm: %w", err)
			}
			return s.confirmAcceptedExistingFact(ctx, profileID, req, claim, ownAlignments[0], "existing owner fact")
		}
		if len(foreignAlignments) > 0 {
			return s.confirmAcceptedExistingFact(ctx, profileID, req, claim, foreignAlignments[0], "existing team fact")
		}
	}
	fact, err := s.createNewFact(ctx, profileID, claim, gate)
	if err != nil {
		return nil, err
	}
	if err := overlayPath(ctx, s.db, profileID, fact.FactID, foreignConflicts); err != nil {
		return nil, fmt.Errorf("confirm memory: link overlays: %w", err)
	}
	if err := alignPath(ctx, s.db, profileID, fact.FactID, foreignAlignments); err != nil {
		return nil, fmt.Errorf("confirm memory: link alignments: %w", err)
	}
	s.incMetric(ctx, "promoted")
	s.recordPromotionFunnelLatency(ctx, claim, "promoted")
	s.emitAudit(ctx, profileID, claim.ClaimID, fact.FactID, "claim.confirm.accept")
	return &ConfirmMemoryResult{
		ClaimID:  claim.ClaimID,
		Decision: req.Decision,
		Status:   "accepted",
		Fact:     fact,
	}, nil
}

func (s *promoteClaimServiceImpl) confirmAcceptedExistingFact(
	ctx context.Context,
	profileID string,
	req ConfirmMemoryRequest,
	claim *domain.Claim,
	fact *domain.Fact,
	description string,
) (*ConfirmMemoryResult, error) {
	if err := s.linkClaimToExistingFact(ctx, profileID, claim.ClaimID, fact.FactID); err != nil {
		return nil, fmt.Errorf("confirm memory: link claim to %s: %w", description, err)
	}
	s.incMetric(ctx, "promoted")
	s.recordPromotionFunnelLatency(ctx, claim, "promoted")
	s.emitAudit(ctx, profileID, claim.ClaimID, fact.FactID, "claim.confirm.accept")
	return &ConfirmMemoryResult{
		ClaimID:  claim.ClaimID,
		Decision: req.Decision,
		Status:   "accepted",
		Fact:     fact,
	}, nil
}
