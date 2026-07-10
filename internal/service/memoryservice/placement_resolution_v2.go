package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/assertionservice"
)

func validateMigrationRefs(ctx context.Context, refs []domain.LegacyMemoryRef) error {
	if len(refs) == 0 {
		return nil
	}
	credential, ok := requestctx.ActorCredentialFromContext(ctx)
	if !ok || credential.Role != "manager" {
		return errors.New("memory service: legacy migration requires a manager API key")
	}
	seen := map[string]struct{}{}
	for i, ref := range refs {
		refType := strings.ToLower(strings.TrimSpace(ref.Type))
		switch refType {
		case "fragment", "claim", "fact", "dream":
		default:
			return fmt.Errorf("memory service: migration_refs[%d].type is invalid", i)
		}
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			return fmt.Errorf("memory service: migration_refs[%d].id is required", i)
		}
		key := refType + ":" + id
		if _, exists := seen[key]; exists {
			return fmt.Errorf("memory service: duplicate migration ref %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (s *service) ResolveMemoryPlacement(ctx context.Context, profileID string, req ResolvePlacementRequest) (*ResolvePlacementResult, error) {
	if s.deps.PlacementStore == nil {
		return nil, errors.New("memory service: placement store is required")
	}
	ingestID := strings.TrimSpace(req.IngestID)
	if ingestID == "" {
		return nil, errors.New("memory service: ingest_id is required")
	}
	run, err := s.deps.PlacementStore.GetRun(ctx, profileID, ingestID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("memory service: placement not found")
	}
	if len(run.MigrationRefs) > 0 {
		if err := requireMigrationManager(ctx); err != nil {
			return nil, err
		}
	}

	switch decision := strings.ToLower(strings.TrimSpace(req.Decision)); decision {
	case "acknowledge":
		return s.acknowledgePlacement(ctx, run)
	case "accept", "reject":
		return s.resolveReviewDecision(ctx, run, strings.TrimSpace(req.PlacementItemID), decision)
	case "correct":
		return s.correctPlacement(ctx, run, req)
	default:
		return nil, errors.New("memory service: decision must be acknowledge, accept, reject, or correct")
	}
}

func (s *service) acknowledgePlacement(ctx context.Context, run *domain.MemoryPlacementRun) (*ResolvePlacementResult, error) {
	if run.Status != domain.MemoryPlacementCompleted {
		return nil, errors.New("memory service: only completed placements can be acknowledged")
	}
	now := time.Now().UTC()
	run.AcknowledgedAt = &now
	run.UpdatedAt = now
	event := domain.AssertionTransitionEvent{
		EventID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte(run.IngestID+":acknowledged")).String(),
		ProfileID:  run.ProfileID,
		IngestID:   run.IngestID,
		EventType:  "acknowledged",
		ReasonCode: "client_acknowledgement",
		Source:     "client",
		OccurredAt: now,
	}
	if err := s.saveRunWithTransitions(ctx, *run, []domain.AssertionTransitionEvent{event}); err != nil {
		return nil, err
	}
	return &ResolvePlacementResult{Placement: *run}, nil
}

func (s *service) resolveReviewDecision(ctx context.Context, run *domain.MemoryPlacementRun, itemID, decision string) (*ResolvePlacementResult, error) {
	if s.deps.Assertions == nil {
		return nil, errors.New("memory service: assertion service is required")
	}
	if len(run.MigrationRefs) > 0 {
		return s.resolveMigrationReviewDecision(ctx, run, decision)
	}
	item := reviewItem(run, itemID)
	if item == nil {
		return nil, errors.New("memory service: placement review item not found")
	}
	if item.AssertionStatus != domain.AssertionStatusNeedsReview {
		return nil, errors.New("memory service: placement item does not need review")
	}
	now := time.Now().UTC()
	tier := domain.AssertionTierCandidate
	status := domain.AssertionStatusRejected
	category := domain.MemoryPlacementAssertionRejected
	reason := "user rejected the ambiguous relationship"
	terminalEvent := "rejected"
	if decision == "accept" {
		tier = domain.AssertionTierFact
		status = domain.AssertionStatusActive
		category = domain.MemoryPlacementAssertionFact
		reason = "user confirmed the relationship as an explicit authority"
		terminalEvent = "promoted"
	}
	updated, writeResult, err := s.deps.Assertions.UpdateState(ctx, run.ProfileID, item.AssertionID, tier, status, now)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, errors.New("memory service: assertion not found")
	}
	fromTier, fromStatus := item.Tier, item.AssertionStatus
	item.Tier = tier
	item.AssertionStatus = status
	item.Category = category
	item.Status = "completed"
	item.Reason = reason
	item.ReviewTaskID = ""
	item.UpdatedAt = now
	run.ReviewTasks = removeReviewTask(run.ReviewTasks, item.ItemID)
	finalizeResolvedRun(run, now)
	events := []domain.AssertionTransitionEvent{
		stateTransition(run, *item, "review_resolved", fromTier, fromStatus, "user_resolution", "user"),
		stateTransition(run, *item, terminalEvent, fromTier, fromStatus, reasonCode(reason), "user"),
	}
	for _, superseded := range writeResult.Superseded {
		events = append(events, transitionForSuperseded(run, superseded, now))
	}
	if err := s.saveRunWithTransitions(ctx, *run, events); err != nil {
		return nil, err
	}
	return &ResolvePlacementResult{Placement: *run}, nil
}

func (s *service) resolveMigrationReviewDecision(ctx context.Context, run *domain.MemoryPlacementRun, decision string) (*ResolvePlacementResult, error) {
	if len(run.Items) == 0 || len(run.ReviewTasks) == 0 {
		return nil, errors.New("memory service: legacy migration has no review bundle")
	}
	now := time.Now().UTC()
	tier := domain.AssertionTierCandidate
	status := domain.AssertionStatusRejected
	category := domain.MemoryPlacementAssertionRejected
	reason := "user rejected the legacy decomposition bundle"
	terminalEvent := "rejected"
	if decision == "accept" {
		tier = domain.AssertionTierFact
		status = domain.AssertionStatusActive
		category = domain.MemoryPlacementAssertionFact
		reason = "user confirmed the complete legacy decomposition bundle"
		terminalEvent = "promoted"
	}

	updates := make([]assertionservice.StateUpdate, 0, len(run.Items))
	for i := range run.Items {
		item := &run.Items[i]
		if item.AssertionStatus != domain.AssertionStatusNeedsReview || item.AssertionID == "" {
			return nil, errors.New("memory service: every migration assertion must be pending review")
		}
		updates = append(updates, assertionservice.StateUpdate{AssertionID: item.AssertionID, Tier: tier, Status: status})
	}

	var writeResult assertionservice.WriteResult
	var err error
	if decision == "accept" {
		_, writeResult, err = s.deps.Assertions.FinalizeLegacyMigration(ctx, run.ProfileID, updates, run.MigrationRefs, now)
	} else {
		_, writeResult, err = s.deps.Assertions.UpdateStates(ctx, run.ProfileID, updates, now)
	}
	if err != nil {
		return nil, err
	}

	events := make([]domain.AssertionTransitionEvent, 0, len(run.Items)*2+len(writeResult.Superseded))
	for i := range run.Items {
		item := &run.Items[i]
		fromTier, fromStatus := item.Tier, item.AssertionStatus
		item.Tier = tier
		item.AssertionStatus = status
		item.Category = category
		item.Status = "completed"
		item.Reason = reason
		item.ReviewTaskID = ""
		item.UpdatedAt = now
		events = append(events,
			stateTransition(run, *item, "review_resolved", fromTier, fromStatus, "user_resolution", "user"),
			stateTransition(run, *item, terminalEvent, fromTier, fromStatus, reasonCode(reason), "user"),
		)
	}
	for _, superseded := range writeResult.Superseded {
		events = append(events, transitionForSuperseded(run, superseded, now))
	}
	run.ReviewTasks = []domain.MemoryReviewTask{}
	finalizeResolvedRun(run, now)
	if err := s.saveRunWithTransitions(ctx, *run, events); err != nil {
		return nil, err
	}
	return &ResolvePlacementResult{Placement: *run}, nil
}

func (s *service) correctPlacement(ctx context.Context, run *domain.MemoryPlacementRun, req ResolvePlacementRequest) (*ResolvePlacementResult, error) {
	if req.CorrectedProposal == nil {
		return nil, errors.New("memory service: corrected_proposal is required")
	}
	if len(req.Evidence) == 0 {
		return nil, errors.New("memory service: correction evidence is required")
	}
	target, err := correctionReviewTarget(run, strings.TrimSpace(req.PlacementItemID))
	if err != nil {
		return nil, err
	}
	additional, err := normalizeEvidenceInputs(run.IngestID+":correction", req.Evidence)
	if err != nil {
		return nil, err
	}
	offset := len(run.Evidence)
	for i := range additional {
		additional[i].Index = offset + i
		additional[i].Authority = string(domain.AuthorityAuthoritative)
		additional[i].TrustedAuthority = true
		additional[i].SourceGroup = "user-resolution:" + run.IngestID
	}
	proposal := *req.CorrectedProposal
	for i := range proposal.Relationships {
		proposal.Relationships[i].Evidence = make([]domain.MemoryEvidenceRef, 0, len(additional))
		for _, evidence := range additional {
			proposal.Relationships[i].Evidence = append(proposal.Relationships[i].Evidence, domain.MemoryEvidenceRef{
				EvidenceIndex: evidence.Index,
				Start:         0,
				End:           utf8.RuneCountInString(evidence.Content),
			})
		}
	}
	combined := append(append([]domain.MemoryEvidence(nil), run.Evidence...), additional...)
	if err := validateProposal(combined, proposal); err != nil {
		return nil, err
	}
	security := assessEvidenceInjection(additional)
	if security.Quarantined {
		if s.deps.FragmentQuarantine == nil {
			return nil, errors.New("memory service: quarantined fragment create service is required")
		}
		attemptID, err := correctionAttemptID(additional, proposal)
		if err != nil {
			return nil, fmt.Errorf("memory service: fingerprint correction: %w", err)
		}
		for i := range additional {
			metadata := make(map[string]any, len(additional[i].Metadata)+2)
			for key, value := range additional[i].Metadata {
				metadata[key] = value
			}
			metadata["placement_ingest_id"] = run.IngestID
			metadata["placement_stage"] = "correction"
			additional[i].Metadata = metadata
			if _, err := s.createPlacementFragment(ctx, run.ProfileID, additional[i], true); err != nil {
				s.log().Error("memory service: failed to persist quarantined correction fragment", "ingest_id", run.IngestID, "evidence_index", additional[i].Index, "error", err)
				return nil, err
			}
		}
		now := time.Now().UTC()
		run.Security = security
		run.UpdatedAt = now
		events := []domain.AssertionTransitionEvent{
			{
				EventID:    correctionTransitionID(run.IngestID, attemptID, "proposed"),
				ProfileID:  run.ProfileID,
				IngestID:   run.IngestID,
				EventType:  "proposed",
				ReasonCode: "user_correction",
				Source:     "user",
				OccurredAt: now,
			},
			{
				EventID:    correctionTransitionID(run.IngestID, attemptID, "quarantined"),
				ProfileID:  run.ProfileID,
				IngestID:   run.IngestID,
				EventType:  "quarantined",
				ReasonCode: "correction_prompt_injection",
				Source:     "security_filter",
				OccurredAt: now,
			},
		}
		if err := s.saveRunWithTransitions(ctx, *run, events); err != nil {
			return nil, err
		}
		return &ResolvePlacementResult{Placement: *run}, nil
	}
	if s.deps.FragmentCreate == nil || s.deps.Assertions == nil {
		return nil, errors.New("memory service: fragment and assertion services are required")
	}

	fragmentIDs := map[int]string{}
	for _, evidence := range additional {
		fragment, err := s.createPlacementFragment(ctx, run.ProfileID, evidence, false)
		if err != nil {
			return nil, err
		}
		fragmentIDs[evidence.Index] = fragment.Fragment.FragmentID
	}
	now := time.Now().UTC()
	events := []domain.AssertionTransitionEvent{}
	if len(run.MigrationRefs) > 0 {
		updates := make([]assertionservice.StateUpdate, 0, len(run.Items))
		for _, item := range run.Items {
			if item.AssertionStatus != domain.AssertionStatusNeedsReview || item.AssertionID == "" {
				return nil, errors.New("memory service: every migration assertion must be pending review")
			}
			updates = append(updates, assertionservice.StateUpdate{AssertionID: item.AssertionID, Tier: item.Tier, Status: domain.AssertionStatusRetracted})
		}
		if _, _, err := s.deps.Assertions.UpdateStates(ctx, run.ProfileID, updates, now); err != nil {
			return nil, err
		}
		for i := range run.Items {
			old := &run.Items[i]
			fromTier, fromStatus := old.Tier, old.AssertionStatus
			old.AssertionStatus = domain.AssertionStatusRetracted
			events = append(events,
				stateTransition(run, *old, "review_resolved", fromTier, fromStatus, "user_resolution", "user"),
				stateTransition(run, *old, "corrected", fromTier, fromStatus, "user_correction", "user"),
			)
		}
	} else if old := target; old != nil && old.AssertionID != "" {
		fromTier, fromStatus := old.Tier, old.AssertionStatus
		updated, _, err := s.deps.Assertions.UpdateState(ctx, run.ProfileID, old.AssertionID, old.Tier, domain.AssertionStatusRetracted, now)
		if err != nil {
			return nil, err
		}
		if updated != nil {
			old.AssertionStatus = domain.AssertionStatusRetracted
			eventType := "corrected"
			if len(proposal.Relationships) == 1 && proposal.Relationships[0].Polarity != old.ReviewedRelationship.Polarity {
				eventType = "reversed"
			}
			events = append(events,
				stateTransition(run, *old, "review_resolved", fromTier, fromStatus, "user_resolution", "user"),
				stateTransition(run, *old, eventType, fromTier, fromStatus, "user_correction", "user"),
			)
		}
	}
	run.Evidence = combined
	run.Proposal = proposal
	run.Items = initialPlacementItems(run.IngestID, run.ProfileID, proposal.Relationships, now)
	attachFragmentIDs(run.Items, fragmentIDs)
	run.ReviewTasks = []domain.MemoryReviewTask{}
	run.Security = security
	run.Status = domain.MemoryPlacementQueued
	run.RequiresAck = false
	run.AcknowledgedAt = nil
	run.CompletedAt = nil
	run.Error = ""
	run.UpdatedAt = now
	if err := s.saveRunWithTransitions(ctx, *run, events); err != nil {
		return nil, err
	}
	s.kickPlacementWorker()
	return &ResolvePlacementResult{Placement: *run}, nil
}

func correctionAttemptID(evidence []domain.MemoryEvidence, proposal domain.MemoryProposal) (string, error) {
	payload, err := json.Marshal(struct {
		Evidence []domain.MemoryEvidence `json:"evidence"`
		Proposal domain.MemoryProposal   `json:"proposal"`
	}{Evidence: evidence, Proposal: proposal})
	if err != nil {
		return "", err
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, payload).String(), nil
}

func correctionTransitionID(ingestID, attemptID, eventType string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join([]string{ingestID, "correction", attemptID, eventType}, ":"))).String()
}

func correctionReviewTarget(run *domain.MemoryPlacementRun, itemID string) (*domain.MemoryPlacementItem, error) {
	if run == nil || run.Status != domain.MemoryPlacementAwaitingReview {
		return nil, errors.New("memory service: only placements awaiting review can be corrected")
	}
	if len(run.MigrationRefs) > 0 {
		if len(run.Items) == 0 || len(run.ReviewTasks) == 0 {
			return nil, errors.New("memory service: legacy migration has no review bundle")
		}
		for i := range run.Items {
			if run.Items[i].AssertionStatus != domain.AssertionStatusNeedsReview || run.Items[i].AssertionID == "" {
				return nil, errors.New("memory service: every migration assertion must be pending review")
			}
		}
		return nil, nil
	}
	target := reviewItem(run, itemID)
	if target == nil {
		return nil, errors.New("memory service: placement review item not found")
	}
	if target.AssertionStatus != domain.AssertionStatusNeedsReview || target.AssertionID == "" {
		return nil, errors.New("memory service: placement item does not need review")
	}
	return target, nil
}

func requireMigrationManager(ctx context.Context) error {
	credential, ok := requestctx.ActorCredentialFromContext(ctx)
	if !ok || credential.Role != "manager" {
		return errors.New("memory service: legacy migration requires a manager API key")
	}
	return nil
}

func transition(run *domain.MemoryPlacementRun, item domain.MemoryPlacementItem, eventType, reason, source string) domain.AssertionTransitionEvent {
	eventID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join([]string{run.IngestID, item.ItemID, item.AssertionID, eventType}, ":"))).String()
	return domain.AssertionTransitionEvent{
		EventID:     eventID,
		ProfileID:   run.ProfileID,
		IngestID:    run.IngestID,
		ItemID:      item.ItemID,
		AssertionID: item.AssertionID,
		EventType:   eventType,
		ToTier:      item.Tier,
		ToStatus:    item.AssertionStatus,
		ReasonCode:  reason,
		Source:      source,
		OccurredAt:  time.Now().UTC(),
	}
}

func transitionForSuperseded(run *domain.MemoryPlacementRun, superseded assertionservice.SupersededAssertion, now time.Time) domain.AssertionTransitionEvent {
	eventID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join([]string{run.IngestID, superseded.AssertionID, "superseded"}, ":"))).String()
	return domain.AssertionTransitionEvent{
		EventID:     eventID,
		ProfileID:   run.ProfileID,
		IngestID:    run.IngestID,
		AssertionID: superseded.AssertionID,
		EventType:   "superseded",
		FromTier:    superseded.Tier,
		ToTier:      superseded.Tier,
		FromStatus:  superseded.Status,
		ToStatus:    domain.AssertionStatusSuperseded,
		ReasonCode:  "new_current_assertion",
		Source:      "policy_engine",
		OccurredAt:  now,
	}
}

func stateTransition(run *domain.MemoryPlacementRun, item domain.MemoryPlacementItem, eventType string, fromTier domain.AssertionTier, fromStatus domain.AssertionStatus, reason, source string) domain.AssertionTransitionEvent {
	event := transition(run, item, eventType, reason, source)
	event.FromTier = fromTier
	event.FromStatus = fromStatus
	return event
}

func reviewItem(run *domain.MemoryPlacementRun, itemID string) *domain.MemoryPlacementItem {
	if run == nil {
		return nil
	}
	if itemID != "" {
		for i := range run.Items {
			if run.Items[i].ItemID == itemID {
				return &run.Items[i]
			}
		}
		return nil
	}
	var found *domain.MemoryPlacementItem
	for i := range run.Items {
		if run.Items[i].AssertionStatus != domain.AssertionStatusNeedsReview {
			continue
		}
		if found != nil {
			return nil
		}
		found = &run.Items[i]
	}
	return found
}

func removeReviewTask(tasks []domain.MemoryReviewTask, itemID string) []domain.MemoryReviewTask {
	out := make([]domain.MemoryReviewTask, 0, len(tasks))
	for _, task := range tasks {
		if task.PlacementItemID != itemID {
			out = append(out, task)
		}
	}
	return out
}

func finalizeResolvedRun(run *domain.MemoryPlacementRun, now time.Time) {
	run.UpdatedAt = now
	if len(run.ReviewTasks) > 0 {
		run.Status = domain.MemoryPlacementAwaitingReview
		return
	}
	run.Status = domain.MemoryPlacementCompleted
	run.RequiresAck = true
	run.CompletedAt = &now
}

func reasonCode(reason string) string {
	switch {
	case strings.Contains(reason, "explicit authority"):
		return "authority_fact_gate"
	case strings.Contains(reason, "two independent"):
		return "two_source_fact_gate"
	case strings.Contains(reason, "entailed"):
		return "entailed"
	case strings.Contains(reason, "insufficient"):
		return "insufficient"
	case strings.Contains(reason, "contradicted"):
		return "contradicted"
	case strings.Contains(reason, "review"):
		return "ambiguity"
	default:
		return "placement_decision"
	}
}

func (s *service) saveRunWithTransitions(ctx context.Context, run domain.MemoryPlacementRun, events []domain.AssertionTransitionEvent) error {
	if err := s.deps.PlacementStore.SaveRunWithTransitions(ctx, run, events); err != nil {
		return err
	}
	for _, event := range events {
		observability.RecordAssertionTransition(ctx, s.deps.Metrics, event.EventType, string(event.ToTier), string(event.ToStatus))
	}
	return nil
}

func (s *service) failPlacement(ctx context.Context, run *domain.MemoryPlacementRun, cause error) error {
	if run == nil {
		return cause
	}
	now := time.Now().UTC()
	run.Status = domain.MemoryPlacementFailed
	run.Error = cause.Error()
	run.UpdatedAt = now
	run.CompletedAt = &now
	for i := range run.Items {
		if run.Items[i].Status != "completed" {
			run.Items[i].Status = "failed"
			run.Items[i].Error = cause.Error()
			run.Items[i].UpdatedAt = now
		}
	}
	if s.deps.PlacementStore != nil {
		if err := s.deps.PlacementStore.SaveRun(ctx, *run); err != nil {
			return fmt.Errorf("%w; save placement failure: %v", cause, err)
		}
	}
	return cause
}
