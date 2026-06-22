package skillpackservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
)

func (s *service) importItem(ctx context.Context, profileID, importID, artifactHash, fragmentID, mode string, item SkillPackItem, inspected InspectItem, decision string) ImportItemResult {
	return s.importItemWithSupport(ctx, profileID, importID, artifactHash, fragmentID, mode, item, inspected, decision, itemSupportInput{})
}

func (s *service) importItemWithSupport(ctx context.Context, profileID, importID, artifactHash, fragmentID, mode string, item SkillPackItem, inspected InspectItem, decision string, support itemSupportInput) ImportItemResult {
	result := ImportItemResult{Index: inspected.Index, Item: item, Conflicts: inspected.ConflictingFacts, Decision: decision}
	if decision == DecisionSkip {
		result.Status = "skipped"
		return result
	}
	if decision == DecisionDemoteToClaim && item.SourceKind != SourceKindFact {
		return itemError(result, fmt.Errorf("%s is only valid for %s items", DecisionDemoteToClaim, SourceKindFact))
	}
	claim := claimFromItem(item, mode, artifactHash, inspected.Index, importID, support, fragmentID)
	createRes, err := s.deps.ClaimCreate.Create(ctx, profileID, claim)
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		return result
	}
	result.ClaimID = createRes.Claim.ClaimID

	if mode == ModeReview {
		if !createRes.Duplicate {
			if err := s.graphOps.tagClaim(ctx, profileID, result.ClaimID, importID, artifactHash, item.SourceKind); err != nil {
				return itemError(result, s.cleanupCreatedEntity(ctx, profileID, "claim", result.ClaimID, err))
			}
			after := claimLedgerState(createRes.Claim, importID)
			if err := s.appendChange(ctx, profileID, importID, "claim", result.ClaimID, domain.SkillPackChangeActionCreated, nil, after); err != nil {
				return itemError(result, s.cleanupCreatedEntity(ctx, profileID, "claim", result.ClaimID, err))
			}
		}
		result.Status = "imported"
		return result
	}

	promotedFactID := ""
	if item.SourceKind == SourceKindFact && decision != DecisionDemoteToClaim && createRes.Duplicate {
		var err error
		promotedFactID, err = s.graphOps.promotedFactIDForClaim(ctx, profileID, result.ClaimID)
		if err != nil {
			return itemError(result, err)
		}
		if promotedFactID != "" {
			result.FactID = promotedFactID
			result.Status = "promoted"
			return result
		}
	}

	beforeClaim := claimLedgerState(createRes.Claim, "")
	if createRes.Duplicate && s.deps.ClaimGet != nil {
		if existing, err := s.deps.ClaimGet.Get(ctx, profileID, result.ClaimID); err == nil {
			beforeClaim = claimLedgerState(existing, "")
		}
	}
	if createRes.Duplicate {
		if err := s.graphOps.trustExistingClaim(ctx, profileID, result.ClaimID); err != nil {
			return itemError(result, err)
		}
	} else {
		if err := s.graphOps.trustClaim(ctx, profileID, result.ClaimID, importID, artifactHash, item.SourceKind); err != nil {
			return itemError(result, s.cleanupCreatedEntity(ctx, profileID, "claim", result.ClaimID, err))
		}
	}
	failClaimMutation := func(err error) ImportItemResult {
		return itemError(result, s.cleanupClaimMutation(ctx, profileID, importID, result.ClaimID, createRes.Duplicate, beforeClaim, err))
	}
	failMutation := failClaimMutation

	if decision == DecisionSupersedeLocal {
		if s.deps.FactGet == nil {
			return failClaimMutation(errors.New("fact get service is required to supersede local facts"))
		}
		var factIDs []string
		var superseded []supersededFactSnapshot
		for _, f := range inspected.ConflictingFacts {
			before, err := s.deps.FactGet.Get(ctx, profileID, f.FactID)
			if err != nil {
				return failClaimMutation(err)
			}
			if before == nil {
				return failClaimMutation(fmt.Errorf("fact %s not found for supersede", f.FactID))
			}
			beforeState := factLedgerState(before)
			if err := s.appendChange(ctx, profileID, importID, "fact", f.FactID, domain.SkillPackChangeActionSuperseded, beforeState, map[string]any{
				"fact_id": f.FactID,
				"status":  string(domain.FactStatusSuperseded),
			}); err != nil {
				return failClaimMutation(err)
			}
			superseded = append(superseded, supersededFactSnapshot{factID: f.FactID, before: beforeState})
			factIDs = append(factIDs, f.FactID)
		}
		if err := s.graphOps.supersedeFacts(ctx, profileID, factIDs, result.ClaimID, importID); err != nil {
			return failClaimMutation(err)
		}
		failMutation = func(err error) ImportItemResult {
			err = s.restoreSupersededFacts(ctx, profileID, importID, superseded, err)
			return failClaimMutation(err)
		}
	}

	if item.SourceKind == SourceKindFact && decision != DecisionDemoteToClaim {
		if s.deps.FactPromote == nil {
			return failMutation(errors.New("fact promote service is required"))
		}
		fact, err := s.deps.FactPromote.Promote(ctx, profileID, result.ClaimID)
		if err != nil {
			return failMutation(err)
		}
		result.FactID = fact.FactID
		factCreated := fact.PromotedFromClaimID == result.ClaimID
		if factCreated {
			if err := s.graphOps.tagFact(ctx, profileID, fact.FactID, importID, artifactHash, item.SourceKind); err != nil {
				err = s.cleanupCreatedEntity(ctx, profileID, "fact", fact.FactID, err)
				return failMutation(err)
			}
		}
		if !createRes.Duplicate {
			afterClaim := map[string]any{"claim_id": result.ClaimID, "subject": item.Subject, "predicate": item.Predicate, "object": item.Object, "status": string(domain.StatusSuperseded), "import_id": importID}
			if s.deps.ClaimGet != nil {
				if finalClaim, err := s.deps.ClaimGet.Get(ctx, profileID, result.ClaimID); err == nil {
					afterClaim = claimLedgerState(finalClaim, importID)
				}
			}
			if err := s.appendChange(ctx, profileID, importID, "claim", result.ClaimID, domain.SkillPackChangeActionCreated, nil, afterClaim); err != nil {
				if factCreated {
					err = s.cleanupCreatedEntity(ctx, profileID, "fact", fact.FactID, err)
				}
				return failMutation(err)
			}
		} else {
			afterClaim := trustedClaimAfterState(result.ClaimID, domain.StatusSuperseded)
			if err := s.appendChange(ctx, profileID, importID, "claim", result.ClaimID, domain.SkillPackChangeActionUpdated, beforeClaim, afterClaim); err != nil {
				if factCreated {
					err = s.cleanupCreatedEntity(ctx, profileID, "fact", fact.FactID, err)
				}
				return failMutation(err)
			}
		}
		if factCreated {
			if err := s.appendChange(ctx, profileID, importID, "fact", fact.FactID, domain.SkillPackChangeActionCreated, nil, factLedgerStateWithImport(fact, importID)); err != nil {
				err = s.cleanupCreatedEntity(ctx, profileID, "fact", fact.FactID, err)
				return failMutation(err)
			}
		}
		result.Status = "promoted"
		return result
	}

	if !createRes.Duplicate {
		afterClaim := map[string]any{"claim_id": result.ClaimID, "subject": item.Subject, "predicate": item.Predicate, "object": item.Object, "status": string(domain.StatusValidated), "entailment_verdict": string(domain.VerdictEntailed), "import_id": importID}
		if s.deps.ClaimGet != nil {
			if finalClaim, err := s.deps.ClaimGet.Get(ctx, profileID, result.ClaimID); err == nil {
				afterClaim = claimLedgerState(finalClaim, importID)
			}
		}
		if err := s.appendChange(ctx, profileID, importID, "claim", result.ClaimID, domain.SkillPackChangeActionCreated, nil, afterClaim); err != nil {
			return failMutation(err)
		}
	} else {
		afterClaim := trustedClaimAfterState(result.ClaimID, domain.StatusValidated)
		if err := s.appendChange(ctx, profileID, importID, "claim", result.ClaimID, domain.SkillPackChangeActionUpdated, beforeClaim, afterClaim); err != nil {
			return failMutation(err)
		}
	}
	result.Status = "validated"
	return result
}

func itemError(result ImportItemResult, err error) ImportItemResult {
	result.Status = "error"
	result.Error = err.Error()
	return result
}

type supersededFactSnapshot struct {
	factID string
	before map[string]any
}

func trustedClaimAfterState(claimID string, status domain.ClaimStatus) map[string]any {
	return map[string]any{
		"claim_id":           claimID,
		"status":             string(status),
		"entailment_verdict": string(domain.VerdictEntailed),
		"verifier_model":     "memory_pack.source_trust",
	}
}

func (s *service) appendChange(ctx context.Context, profileID, importID, entityType, entityID, action string, before, after map[string]any) error {
	if s.deps.Ledger == nil {
		return nil
	}
	return s.deps.Ledger.AppendChange(ctx, domain.SkillPackImportChange{
		ChangeID:    uuid.NewString(),
		ImportID:    importID,
		TeamID:      profileID,
		EntityType:  entityType,
		EntityID:    entityID,
		Action:      action,
		BeforeState: before,
		AfterState:  after,
		CreatedAt:   time.Now().UTC(),
	})
}

func (s *service) cleanupCreatedEntity(ctx context.Context, profileID, entityType, entityID string, cause error) error {
	if cause == nil || !s.graphOps.available() {
		return cause
	}
	if err := s.graphOps.deleteEntity(ctx, profileID, entityType, entityID); err != nil {
		return fmt.Errorf("%w; cleanup %s %s: %v", cause, entityType, entityID, err)
	}
	return cause
}

func (s *service) cleanupClaimMutation(ctx context.Context, profileID, importID, claimID string, duplicate bool, before map[string]any, cause error) error {
	if cause == nil || !s.graphOps.available() {
		return cause
	}
	if !duplicate {
		return s.cleanupCreatedEntity(ctx, profileID, "claim", claimID, cause)
	}
	if err := s.graphOps.restoreClaim(ctx, profileID, claimID, importID, before); err != nil {
		return fmt.Errorf("%w; cleanup claim %s: %v", cause, claimID, err)
	}
	return cause
}

func (s *service) restoreSupersededFacts(ctx context.Context, profileID, importID string, facts []supersededFactSnapshot, cause error) error {
	if cause == nil || len(facts) == 0 || !s.graphOps.available() {
		return cause
	}
	for _, fact := range facts {
		if err := s.graphOps.restoreFact(ctx, profileID, fact.factID, importID, fact.before); err != nil {
			return fmt.Errorf("%w; cleanup fact %s: %v", cause, fact.factID, err)
		}
	}
	return cause
}
