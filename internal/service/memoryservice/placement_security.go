package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const (
	releaseQuarantineAction       = "release_quarantine"
	quarantineReleaseReasonMaxLen = 1000
)

func (s *service) quarantinePlacementEvidence(ctx context.Context, run *domain.MemoryPlacementRun, assessments []semanticEvidenceSecurityAssessment, reason string) error {
	if run == nil || len(assessments) == 0 {
		return nil
	}
	if s.deps.SemanticStore == nil {
		return errors.New("memory service: semantic store is required")
	}
	now := time.Now().UTC()
	for _, item := range assessments {
		evidence := evidenceForIndex(run.Evidence, item.EvidenceIndex)
		if evidence.Content == "" {
			return errors.New("memory service: quarantined evidence content is missing")
		}
		placementItem := placementItemForEvidenceIndex(run.Items, item.EvidenceIndex)
		if placementItem == nil || placementItem.FragmentID == "" {
			return errors.New("memory service: quarantined evidence fragment is missing")
		}
		if err := s.deps.SemanticStore.StoreEvidenceSecurityEvent(ctx, repository.SemanticEvidenceSecurityEventInput{
			TeamID:           run.ProfileID,
			OwnerProfileID:   ownerProfileIDForRun(run),
			FragmentID:       placementItem.FragmentID,
			Content:          evidence.Content,
			Assessment:       item.Assessment,
			DeactivateSearch: true,
		}); err != nil {
			return err
		}
		updateEvidenceSecurityDecision(run.Evidence, item.EvidenceIndex, domain.EvidenceSecurityQuarantine)
		placementItem.Category = domain.MemoryPlacementEvidenceQuarantined
		placementItem.Status = "completed"
		placementItem.Reason = reason
		placementItem.Error = ""
		placementItem.UpdatedAt = now
	}
	run.Error = ""
	run.StartedAt = nil
	run.AvailableAt = now
	run.UpdatedAt = now
	if placementItemsCompleted(run.Items) {
		run.Status = domain.MemoryPlacementCompleted
		run.CompletedAt = &now
	} else {
		run.Status = domain.MemoryPlacementQueued
		run.CompletedAt = nil
	}
	if err := s.deps.PlacementStore.SaveRun(ctx, *run); err != nil {
		return err
	}
	if run.Status == domain.MemoryPlacementQueued {
		s.kickPlacementWorker()
	}
	return nil
}

func (s *service) releasePlacementQuarantine(ctx context.Context, profileID string, req DisputeRequest, now time.Time) (*DisputeResult, error) {
	if strings.TrimSpace(req.DisputeID) != "" {
		return nil, errors.New("memory service: release_quarantine does not accept dispute_id")
	}
	if !isManagerCredential(ctx) {
		return nil, errors.New("memory service: manager role is required to release quarantine")
	}
	ingestID := strings.TrimSpace(req.IngestID)
	if ingestID == "" {
		return nil, errors.New("memory service: ingest_id is required")
	}
	itemID := strings.TrimSpace(req.PlacementItemID)
	if itemID == "" {
		return nil, errors.New("memory service: placement_item_id is required")
	}
	reason := strings.TrimSpace(req.Message)
	if reason == "" {
		return nil, errors.New("memory service: quarantine release reason is required")
	}
	if len([]rune(reason)) > quarantineReleaseReasonMaxLen {
		return nil, fmt.Errorf("memory service: quarantine release reason must be at most %d characters", quarantineReleaseReasonMaxLen)
	}
	if s.deps.SemanticStore == nil {
		return nil, errors.New("memory service: semantic store is required")
	}
	run, err := s.deps.PlacementStore.GetRun(ctx, profileID, ingestID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("memory service: placement not found")
	}
	item := findPlacementItem(run, itemID)
	if item == nil {
		return nil, errors.New("memory service: placement item not found")
	}
	if item.Category != domain.MemoryPlacementEvidenceQuarantined {
		return nil, errors.New("memory service: placement item is not quarantined")
	}
	if strings.TrimSpace(item.FragmentID) == "" {
		return nil, errors.New("memory service: quarantined evidence fragment is missing")
	}
	evidence := evidenceForIndex(run.Evidence, item.EvidenceIndex)
	if strings.TrimSpace(evidence.Content) == "" {
		return nil, errors.New("memory service: quarantined evidence content is missing")
	}
	if err := s.deps.SemanticStore.StoreEvidenceSecurityEvent(ctx, repository.SemanticEvidenceSecurityEventInput{
		TeamID:         run.ProfileID,
		OwnerProfileID: ownerProfileIDForRun(run),
		FragmentID:     item.FragmentID,
		Content:        evidence.Content,
		Assessment: domain.EvidenceSecurityAssessment{
			Decision:  domain.EvidenceSecurityGuarded,
			EventKind: domain.EvidenceSecurityEventQuarantineRelease,
			Reason:    reason,
		},
	}); err != nil {
		return nil, err
	}
	updateEvidenceSecurityDecision(run.Evidence, item.EvidenceIndex, domain.EvidenceSecurityGuarded)
	item.Category = domain.MemoryPlacementFragmentOnly
	item.Status = string(domain.MemoryPlacementQueued)
	item.Reason = "Quarantine was released by a manager; evidence will re-enter guarded placement."
	item.Error = ""
	item.ClaimID = ""
	item.FactID = ""
	item.RelationshipOutcomes = nil
	item.UpdatedAt = now
	run.Status = domain.MemoryPlacementQueued
	run.Attempts = 0
	run.Error = ""
	run.StartedAt = nil
	run.CompletedAt = nil
	run.AvailableAt = now
	run.UpdatedAt = now
	if err := s.deps.PlacementStore.SaveRun(ctx, *run); err != nil {
		return nil, err
	}
	s.kickPlacementWorker()
	return &DisputeResult{Placement: run}, nil
}

func isManagerCredential(ctx context.Context) bool {
	credential, ok := requestctx.ActorCredentialFromContext(ctx)
	return ok && strings.EqualFold(strings.TrimSpace(credential.Role), "manager")
}

func ownerProfileIDForRun(run *domain.MemoryPlacementRun) string {
	if run == nil || run.OwnerProfileID == "" {
		if run == nil {
			return ""
		}
		return run.ProfileID
	}
	return run.OwnerProfileID
}

func placementItemForEvidenceIndex(items []domain.MemoryPlacementItem, evidenceIndex int) *domain.MemoryPlacementItem {
	for i := range items {
		if items[i].EvidenceIndex == evidenceIndex {
			return &items[i]
		}
	}
	return nil
}

func updateEvidenceSecurityDecision(evidence []domain.MemoryEvidence, evidenceIndex int, decision domain.EvidenceSecurityDecision) {
	for i := range evidence {
		if evidence[i].Index == evidenceIndex {
			evidence[i].SecurityDecision = decision
			return
		}
	}
}
