package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

const (
	defaultPlacementMaxAttempts     = 5
	defaultPlacementRetryBaseSecond = 5
	defaultPlacementRetryMaxSecond  = 60
)

// EvidenceInput is a single client-supplied evidence item for server-owned
// placement. Normal clients do not submit extracted claims or promotion hints.
type EvidenceInput struct {
	Content        string         `json:"content"`
	SourceType     string         `json:"source_type,omitempty"`
	Source         string         `json:"source,omitempty"`
	Authority      string         `json:"authority,omitempty"`
	SourceGroup    string         `json:"source_group,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Labels         []string       `json:"labels,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// RememberRequest stores chat-session evidence for asynchronous placement.
type RememberRequest struct {
	Evidence []EvidenceInput `json:"evidence"`
}

// PlacementStatusRequest fetches one server-owned placement run.
type PlacementStatusRequest struct {
	IngestID string `json:"ingest_id"`
}

// PlacementStatusResult is the public polling response for remember.
type PlacementStatusResult struct {
	Run domain.MemoryPlacementRun `json:"placement"`
}

// DisputeRequest starts or continues a bounded placement dispute session.
type DisputeRequest struct {
	IngestID        string          `json:"ingest_id,omitempty"`
	PlacementItemID string          `json:"placement_item_id,omitempty"`
	DisputeID       string          `json:"dispute_id,omitempty"`
	Action          string          `json:"action,omitempty"`
	Message         string          `json:"message,omitempty"`
	Evidence        []EvidenceInput `json:"evidence,omitempty"`
}

// DisputeResult returns the current dispute session and any updated placement.
type DisputeResult struct {
	Session   *domain.MemoryDisputeSession `json:"session,omitempty"`
	Placement *domain.MemoryPlacementRun   `json:"placement,omitempty"`
}

// Remember stores normal chat-session evidence and queues server-owned
// placement. Client LLMs cannot submit claims or promotion hints on this path.
func (s *service) Remember(ctx context.Context, profileID string, req RememberRequest) (*RememberResult, error) {
	if s.deps.PlacementStore == nil {
		return nil, errors.New("memory service: placement store is required")
	}
	if s.deps.SemanticStore == nil && s.deps.FragmentCreate == nil {
		return nil, errors.New("memory service: semantic store is required")
	}
	if len(req.Evidence) == 0 {
		return nil, errors.New("memory service: evidence is required")
	}

	teamID, teamName, ownerProfileID, ownerProfileName := semanticPrincipal(ctx, profileID)
	ingestID := uuid.NewString()
	now := time.Now().UTC()
	scanner := evidenceScanner(s.deps)
	securityAssessments := make([]domain.EvidenceSecurityAssessment, 0, len(req.Evidence))
	run := domain.MemoryPlacementRun{
		IngestID:          ingestID,
		ProfileID:         teamID,
		OwnerProfileID:    ownerProfileID,
		Status:            domain.MemoryPlacementProcessing,
		CheckAfterSeconds: 60,
		StatusTool:        "get_memory_placement",
		AvailableAt:       now,
		Evidence:          make([]domain.MemoryEvidence, 0, len(req.Evidence)),
		Items:             make([]domain.MemoryPlacementItem, 0, len(req.Evidence)),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	for i, evidence := range req.Evidence {
		content := strings.TrimSpace(evidence.Content)
		if content == "" {
			return nil, fmt.Errorf("memory service: evidence[%d].content is required", i)
		}
		assessment, err := scanner.ScanEvidence(content)
		if err != nil {
			return nil, fmt.Errorf("memory service: evidence[%d] security scan failed: %w", i, err)
		}
		securityAssessments = append(securityAssessments, assessment)
		sourceType, err := parseSourceType(evidence.SourceType)
		if err != nil {
			return nil, fmt.Errorf("memory service: evidence[%d].source_type is invalid", i)
		}
		authority, err := parseAuthority(evidence.Authority)
		if err != nil {
			return nil, fmt.Errorf("memory service: evidence[%d].authority is invalid", i)
		}
		sourceGroup := strings.TrimSpace(evidence.SourceGroup)
		if sourceGroup == "" {
			sourceGroup = strings.TrimSpace(evidence.Source)
		}
		idempotencyKey := strings.TrimSpace(evidence.IdempotencyKey)
		if idempotencyKey == "" {
			idempotencyKey = fmt.Sprintf("%s:%d", ingestID, i)
		}
		run.Evidence = append(run.Evidence, domain.MemoryEvidence{
			Index:            i,
			Content:          content,
			SourceType:       string(sourceType),
			Source:           evidence.Source,
			Authority:        string(authority),
			SourceGroup:      sourceGroup,
			IdempotencyKey:   idempotencyKey,
			Labels:           append([]string(nil), evidence.Labels...),
			Metadata:         evidence.Metadata,
			SecurityDecision: assessment.Decision,
		})
		item := domain.MemoryPlacementItem{
			ItemID:         uuid.NewString(),
			IngestID:       ingestID,
			ProfileID:      teamID,
			OwnerProfileID: ownerProfileID,
			EvidenceIndex:  i,
			Category:       domain.MemoryPlacementFragmentOnly,
			Status:         string(domain.MemoryPlacementQueued),
			Reason:         "evidence stored; awaiting Dense-Mem verifier placement",
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if assessment.Decision == domain.EvidenceSecurityQuarantine {
			item.Category = domain.MemoryPlacementEvidenceQuarantined
			item.Status = "completed"
			item.Reason = "Evidence was quarantined by Dense-Mem security policy; it is excluded from model-backed processing."
		}
		run.Items = append(run.Items, item)
	}
	if err := s.deps.PlacementStore.CreateRun(ctx, run); err != nil {
		return nil, err
	}
	fragments := make([]FragmentOutcome, 0, len(req.Evidence))
	if s.deps.SemanticStore != nil {
		stored, err := s.deps.SemanticStore.StoreRemember(ctx, repository.SemanticRememberInput{
			TeamID:           teamID,
			TeamName:         teamName,
			OwnerProfileID:   ownerProfileID,
			OwnerProfileName: ownerProfileName,
			Evidence:         semanticEvidenceInputsWithSecurity(run.Evidence, securityAssessments),
		})
		if err != nil {
			failedAt := time.Now().UTC()
			run.Status = domain.MemoryPlacementFailed
			run.Error = err.Error()
			run.UpdatedAt = failedAt
			run.CompletedAt = &failedAt
			for i := range run.Items {
				run.Items[i].Status = "failed"
				run.Items[i].Error = err.Error()
			}
			_ = s.deps.PlacementStore.SaveRun(ctx, run)
			return nil, err
		}
		for i, evidence := range stored.Evidence {
			if i < len(run.Items) {
				run.Items[i].FragmentID = evidence.FragmentID
			}
			fragments = append(fragments, FragmentOutcome{
				ID:        evidence.FragmentID,
				Status:    "stored",
				CreatedAt: evidence.CreatedAt,
			})
		}
	} else {
		for i, evidence := range req.Evidence {
			content := strings.TrimSpace(evidence.Content)
			fragmentRes, err := s.deps.FragmentCreate.Create(ctx, teamID, &dto.CreateFragmentRequest{
				Content:        content,
				SourceType:     "conversation",
				Source:         evidence.Source,
				Authority:      "primary",
				IdempotencyKey: evidence.IdempotencyKey,
				Labels:         evidence.Labels,
				Metadata:       evidence.Metadata,
				SourceQuality:  defaultSourceQuality("primary"),
			})
			if err != nil {
				failedAt := time.Now().UTC()
				run.Status = domain.MemoryPlacementFailed
				run.Error = err.Error()
				run.UpdatedAt = failedAt
				run.CompletedAt = &failedAt
				run.Items[i].Status = "failed"
				run.Items[i].Error = err.Error()
				_ = s.deps.PlacementStore.SaveRun(ctx, run)
				return nil, err
			}
			status := "created"
			if fragmentRes.Duplicate {
				status = "duplicate"
			}
			fragments = append(fragments, FragmentOutcome{
				ID:          fragmentRes.Fragment.FragmentID,
				Status:      status,
				DuplicateOf: fragmentRes.DuplicateOf,
				CreatedAt:   fragmentRes.Fragment.CreatedAt,
			})
			run.Items[i].FragmentID = fragmentRes.Fragment.FragmentID
		}
	}
	queuedAt := time.Now().UTC()
	if placementItemsCompleted(run.Items) {
		run.Status = domain.MemoryPlacementCompleted
		run.CompletedAt = &queuedAt
	} else {
		run.Status = domain.MemoryPlacementQueued
	}
	run.AvailableAt = queuedAt
	run.UpdatedAt = queuedAt
	if err := s.deps.PlacementStore.SaveRun(ctx, run); err != nil {
		return nil, err
	}
	if run.Status == domain.MemoryPlacementQueued {
		s.kickPlacementWorker()
	}
	return &RememberResult{
		IngestID:          ingestID,
		Status:            string(run.Status),
		CheckAfterSeconds: 60,
		StatusTool:        "get_memory_placement",
		Evidence:          fragments,
		Items:             run.Items,
	}, nil
}

func (s *service) GetMemoryPlacement(ctx context.Context, profileID string, req PlacementStatusRequest) (*PlacementStatusResult, error) {
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
	return &PlacementStatusResult{Run: *run}, nil
}

func (s *service) DisputeMemoryPlacement(ctx context.Context, profileID string, req DisputeRequest) (*DisputeResult, error) {
	if s.deps.PlacementStore == nil {
		return nil, errors.New("memory service: placement store is required")
	}
	now := time.Now().UTC()
	action := strings.TrimSpace(req.Action)
	if action == releaseQuarantineAction {
		return s.releasePlacementQuarantine(ctx, profileID, req, now)
	}
	if action != "" {
		return nil, fmt.Errorf("memory service: unsupported placement action %q", action)
	}
	if strings.TrimSpace(req.DisputeID) != "" {
		session, run, err := s.deps.PlacementStore.UpdateDisputeWithRun(ctx, profileID, req.DisputeID, func(session *domain.MemoryDisputeSession, run *domain.MemoryPlacementRun) error {
			return s.applyDisputeTurn(ctx, profileID, run, session, req, now)
		})
		if err != nil {
			return nil, err
		}
		if session == nil {
			return nil, errors.New("memory service: dispute not found")
		}
		if run == nil {
			return nil, errors.New("memory service: placement not found")
		}
		return &DisputeResult{Session: session, Placement: run}, nil
	}
	if strings.TrimSpace(req.IngestID) == "" {
		return nil, errors.New("memory service: ingest_id is required")
	}
	run, err := s.deps.PlacementStore.GetRun(ctx, profileID, strings.TrimSpace(req.IngestID))
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("memory service: placement not found")
	}
	session := &domain.MemoryDisputeSession{
		DisputeID:       uuid.NewString(),
		ProfileID:       profileID,
		IngestID:        strings.TrimSpace(req.IngestID),
		PlacementItemID: strings.TrimSpace(req.PlacementItemID),
		Status:          domain.MemoryDisputeOpen,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.applyDisputeTurn(ctx, profileID, run, session, req, now); err != nil {
		return nil, err
	}
	if err := s.deps.PlacementStore.CreateDisputeAndSaveRun(ctx, *session, *run); err != nil {
		return nil, err
	}
	return &DisputeResult{Session: session, Placement: run}, nil
}

func (s *service) StartPlacementWorker(ctx context.Context, interval time.Duration) {
	if s.deps.PlacementStore == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	kickCh := s.placementKick
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			for {
				processed, err := s.ProcessNextPlacement(ctx)
				if err != nil {
					s.log().Warn("memory placement worker failed", slog.String("error", err.Error()))
					break
				}
				if !processed {
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-kickCh:
			}
		}
	}()
}

func (s *service) ProcessNextPlacement(ctx context.Context) (bool, error) {
	if s.deps.PlacementStore == nil {
		return false, errors.New("memory service: placement store is required")
	}
	run, err := s.deps.PlacementStore.ClaimNextQueuedRun(ctx)
	if err != nil {
		return false, err
	}
	if run == nil {
		return false, nil
	}
	return true, s.processPlacementRun(ctx, run)
}

func (s *service) kickPlacementWorker() {
	if s.deps.PlacementStore == nil || s.placementKick == nil {
		return
	}
	select {
	case s.placementKick <- struct{}{}:
	default:
	}
}

func (s *service) processPlacementRun(ctx context.Context, run *domain.MemoryPlacementRun) error {
	if run == nil {
		return nil
	}
	if maxAttempts := s.placementMaxAttempts(); maxAttempts > 0 && run.Attempts > maxAttempts {
		s.failPlacementRun(ctx, run, fmt.Errorf("memory placement exceeded max attempts (%d)", maxAttempts))
		return nil
	}
	if s.deps.SemanticStore != nil {
		return s.processSemanticPlacementRun(ctx, run)
	}
	now := time.Now().UTC()
	run.Status = domain.MemoryPlacementProcessing
	run.StartedAt = &now
	run.UpdatedAt = now
	if err := s.deps.PlacementStore.SaveRun(ctx, *run); err != nil {
		return err
	}

	for i := range run.Items {
		item := &run.Items[i]
		if item.Status == "completed" {
			continue
		}
		evidence := evidenceForIndex(run.Evidence, item.EvidenceIndex)
		if strings.TrimSpace(evidence.Content) == "" {
			item.Category = domain.MemoryPlacementNeedsEvidence
			item.Status = "completed"
			item.Reason = "Dense-Mem verifier could not find evidence content for this placement item."
			item.UpdatedAt = time.Now().UTC()
			continue
		}
		if looksFalse(evidence.Content) {
			item.Category = domain.MemoryPlacementRejectedFalse
			item.Status = "completed"
			item.Reason = "Dense-Mem verifier classified the evidence as a contradiction or false-memory correction."
			item.UpdatedAt = time.Now().UTC()
			continue
		}
		claim, ok := extractServerClaim(evidence, item.FragmentID)
		if !ok {
			item.Category = domain.MemoryPlacementFragmentOnly
			item.Status = "completed"
			item.Reason = "Evidence was stored as a fragment; the verifier found no supported personal-memory claim to extract."
			item.UpdatedAt = time.Now().UTC()
			continue
		}
		outcome, _ := s.processClaim(ctx, run.ProfileID, item.FragmentID, claim, true)
		item.ClaimID = outcome.ClaimID
		if outcome.Fact != nil {
			item.FactID = outcome.Fact.FactID
		}
		item.Category, item.Reason = placementFromClaimOutcome(outcome)
		item.Status = "completed"
		item.Error = outcome.Error
		item.UpdatedAt = time.Now().UTC()
	}
	completedAt := time.Now().UTC()
	run.Status = domain.MemoryPlacementCompleted
	run.CompletedAt = &completedAt
	run.AvailableAt = completedAt
	run.UpdatedAt = completedAt
	return s.deps.PlacementStore.SaveRun(ctx, *run)
}

func (s *service) processSemanticPlacementRun(ctx context.Context, run *domain.MemoryPlacementRun) error {
	now := time.Now().UTC()
	run.Status = domain.MemoryPlacementProcessing
	run.StartedAt = &now
	run.UpdatedAt = now
	if err := s.deps.PlacementStore.SaveRun(ctx, *run); err != nil {
		return err
	}
	activeEvidence := placementActiveEvidence(run)
	if len(activeEvidence) == 0 {
		completedAt := time.Now().UTC()
		run.Status = domain.MemoryPlacementCompleted
		run.Error = ""
		run.CompletedAt = &completedAt
		run.AvailableAt = completedAt
		run.UpdatedAt = completedAt
		return s.deps.PlacementStore.SaveRun(ctx, *run)
	}

	teamID := strings.TrimSpace(run.ProfileID)
	ownerProfileID := strings.TrimSpace(run.OwnerProfileID)
	if ownerProfileID == "" {
		ownerProfileID = teamID
	}
	if s.deps.SemanticReviewer == nil {
		err := errors.New("memory service: semantic reviewer is required")
		s.failPlacementRun(ctx, run, err)
		return err
	}
	review, err := s.deps.SemanticReviewer.ReviewSemantic(ctx, semanticReviewRequest{
		RequestID: run.IngestID,
		Evidence:  activeEvidence,
	})
	if err != nil {
		retry, saveErr := s.retryOrFailPlacementRun(ctx, run, err)
		if saveErr != nil {
			return saveErr
		}
		if retry {
			return nil
		}
		return err
	}
	if len(review.SecurityAssessments) > 0 {
		return s.quarantinePlacementEvidence(ctx, run, review.SecurityAssessments, "Evidence was quarantined by Dense-Mem reviewer security policy; it is excluded from model-backed processing.")
	}
	reviewRelationships := annotateSemanticReviewRelationships(review)
	verifierContext, err := s.deps.SemanticStore.LoadSemanticVerifierContext(ctx, teamID, reviewRelationships)
	if err != nil {
		s.failPlacementRun(ctx, run, err)
		return err
	}
	verification, err := verifySemanticRelationshipsWithEvidence(ctx, s.deps.SemanticVerifier, run.IngestID, reviewRelationships, activeEvidence, verifierContext)
	if err != nil {
		retry, saveErr := s.retryOrFailPlacementRun(ctx, run, err)
		if saveErr != nil {
			return saveErr
		}
		if retry {
			return nil
		}
		return err
	}
	if len(verification.SecurityAssessments) > 0 {
		return s.quarantinePlacementEvidence(ctx, run, verification.SecurityAssessments, "Evidence was quarantined by Dense-Mem verifier security policy; it is excluded from model-backed processing.")
	}
	relationships := verification.Relationships
	stored, err := s.deps.SemanticStore.StoreRemember(ctx, repository.SemanticRememberInput{
		TeamID:         teamID,
		OwnerProfileID: ownerProfileID,
		Evidence:       semanticEvidenceInputsFromMemory(run.Evidence),
		Relationships:  relationships,
	})
	if err != nil {
		s.failPlacementRun(ctx, run, err)
		return err
	}

	outcomesByEvidence := semanticPlacementRelationshipOutcomes(relationships, stored)
	for i := range run.Items {
		item := &run.Items[i]
		if item.Status == "completed" {
			continue
		}
		if item.FragmentID == "" && item.EvidenceIndex >= 0 && item.EvidenceIndex < len(stored.Evidence) {
			item.FragmentID = stored.Evidence[item.EvidenceIndex].FragmentID
		}
		item.RelationshipOutcomes = append([]domain.MemoryPlacementRelationshipOutcome(nil), outcomesByEvidence[item.EvidenceIndex]...)
		item.Category = domain.MemoryPlacementEvidenceProcessed
		item.Status = "completed"
		if len(item.RelationshipOutcomes) > 0 {
			item.Reason = "Evidence was stored and bounded semantic outcomes are listed."
		} else {
			item.Reason = "Evidence was stored; no supported semantic relationship was extracted."
		}
		item.Error = ""
		item.UpdatedAt = time.Now().UTC()
	}
	completedAt := time.Now().UTC()
	run.Status = domain.MemoryPlacementCompleted
	run.Error = ""
	run.CompletedAt = &completedAt
	run.AvailableAt = completedAt
	run.UpdatedAt = completedAt
	return s.deps.PlacementStore.SaveRun(ctx, *run)
}

func placementItemsCompleted(items []domain.MemoryPlacementItem) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.Status != "completed" {
			return false
		}
	}
	return true
}

func placementActiveEvidence(run *domain.MemoryPlacementRun) []domain.MemoryEvidence {
	if run == nil {
		return nil
	}
	activeIndexes := map[int]struct{}{}
	for _, item := range run.Items {
		if item.Status == "completed" || item.Category == domain.MemoryPlacementEvidenceQuarantined {
			continue
		}
		activeIndexes[item.EvidenceIndex] = struct{}{}
	}
	out := make([]domain.MemoryEvidence, 0, len(activeIndexes))
	for _, evidence := range run.Evidence {
		if _, ok := activeIndexes[evidence.Index]; ok {
			out = append(out, evidence)
		}
	}
	return out
}

func (s *service) retryOrFailPlacementRun(ctx context.Context, run *domain.MemoryPlacementRun, err error) (bool, error) {
	if run == nil {
		return false, nil
	}
	if !isRetryablePlacementError(err) || run.Attempts >= s.placementMaxAttempts() {
		s.failPlacementRun(ctx, run, err)
		return false, nil
	}
	now := time.Now().UTC()
	delay := placementRetryDelay(run.Attempts)
	availableAt := now.Add(delay)
	run.Status = domain.MemoryPlacementQueued
	run.CheckAfterSeconds = int(delay.Seconds())
	run.Error = err.Error()
	run.AvailableAt = availableAt
	run.StartedAt = nil
	run.CompletedAt = nil
	run.UpdatedAt = now
	for i := range run.Items {
		run.Items[i].Status = string(domain.MemoryPlacementQueued)
		run.Items[i].Error = err.Error()
		run.Items[i].UpdatedAt = now
	}
	if saveErr := s.deps.PlacementStore.SaveRun(ctx, *run); saveErr != nil {
		return false, saveErr
	}
	s.log().Warn(
		"memory placement retry scheduled",
		slog.String("ingest_id", run.IngestID),
		slog.Int("attempt", run.Attempts),
		slog.Int("max_attempts", s.placementMaxAttempts()),
		slog.Int("retry_after_seconds", int(delay.Seconds())),
		slog.String("error", err.Error()),
	)
	return true, nil
}

func (s *service) failPlacementRun(ctx context.Context, run *domain.MemoryPlacementRun, err error) {
	if run == nil {
		return
	}
	failedAt := time.Now().UTC()
	run.Status = domain.MemoryPlacementFailed
	if err != nil {
		run.Error = err.Error()
	}
	run.AvailableAt = failedAt
	run.UpdatedAt = failedAt
	run.CompletedAt = &failedAt
	for i := range run.Items {
		run.Items[i].Status = "failed"
		if err != nil {
			run.Items[i].Error = err.Error()
		}
		run.Items[i].UpdatedAt = failedAt
	}
	_ = s.deps.PlacementStore.SaveRun(ctx, *run)
}

func (s *service) placementMaxAttempts() int {
	if s.deps.PlacementMaxAttempts > 0 {
		return s.deps.PlacementMaxAttempts
	}
	return defaultPlacementMaxAttempts
}

func placementRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	seconds := defaultPlacementRetryBaseSecond
	for i := 1; i < attempt; i++ {
		seconds *= 2
		if seconds >= defaultPlacementRetryMaxSecond {
			seconds = defaultPlacementRetryMaxSecond
			break
		}
	}
	return time.Duration(seconds) * time.Second
}

func isRetryablePlacementError(err error) bool {
	return errors.Is(err, verifier.ErrVerifierTimeout) ||
		errors.Is(err, verifier.ErrVerifierProvider) ||
		errors.Is(err, verifier.ErrVerifierRateLimit) ||
		errors.Is(err, verifier.ErrVerifierMalformedResponse) ||
		errors.Is(err, context.DeadlineExceeded)
}

func (s *service) applyDisputeTurn(ctx context.Context, profileID string, run *domain.MemoryPlacementRun, session *domain.MemoryDisputeSession, req DisputeRequest, now time.Time) error {
	session.Turns = append(session.Turns, domain.MemoryDisputeTurn{
		At:       now,
		Role:     "client",
		Message:  strings.TrimSpace(req.Message),
		Evidence: disputeEvidence(req.Evidence),
	})
	session.Status = domain.MemoryDisputeProcessing
	session.UpdatedAt = now

	finalReason, accepted, rejected, err := s.applyDisputeEvidence(ctx, profileID, run, session, req)
	if err != nil {
		return err
	}
	switch {
	case accepted:
		session.Status = domain.MemoryDisputeAcceptedPromoted
		session.FinalReason = finalReason
		session.CompletedAt = &now
	case rejected:
		session.Status = domain.MemoryDisputeRejectedExplained
		session.FinalReason = finalReason
		session.CompletedAt = &now
	default:
		session.Status = domain.MemoryDisputeOpen
		session.FinalReason = "Dense-Mem verifier needs more evidence before changing the placement."
	}
	session.UpdatedAt = now
	return nil
}

func (s *service) applyDisputeEvidence(ctx context.Context, profileID string, run *domain.MemoryPlacementRun, session *domain.MemoryDisputeSession, req DisputeRequest) (string, bool, bool, error) {
	target := findPlacementItem(run, session.PlacementItemID)
	if target == nil && strings.TrimSpace(session.PlacementItemID) == "" && len(run.Items) > 0 {
		target = &run.Items[0]
		session.PlacementItemID = target.ItemID
	}
	if target == nil {
		return "", false, false, errors.New("memory service: placement item not found")
	}
	if looksFalse(req.Message) || evidenceLooksFalse(req.Evidence) {
		target.Category = domain.MemoryPlacementDisputeRejected
		target.Status = "completed"
		target.Reason = "Dense-Mem verifier kept the placement rejected because the dispute evidence contradicts the memory."
		target.UpdatedAt = time.Now().UTC()
		return target.Reason, false, true, nil
	}
	if hasDisputeEvidence(req.Evidence) && s.deps.FragmentCreate == nil {
		return "", false, false, errors.New("memory service: fragment create service is required")
	}
	for _, evidence := range req.Evidence {
		content := strings.TrimSpace(evidence.Content)
		if content == "" {
			continue
		}
		fragmentRes, err := s.deps.FragmentCreate.Create(ctx, profileID, &dto.CreateFragmentRequest{
			Content:        content,
			SourceType:     "conversation",
			Source:         evidence.Source,
			Authority:      "primary",
			IdempotencyKey: evidence.IdempotencyKey,
			Labels:         evidence.Labels,
			Metadata:       evidence.Metadata,
			SourceQuality:  defaultSourceQuality("primary"),
		})
		if err != nil {
			return "", false, false, err
		}
		claim, ok := extractServerClaim(domain.MemoryEvidence{Content: content}, fragmentRes.Fragment.FragmentID)
		if !ok {
			continue
		}
		outcome, _ := s.processClaim(ctx, profileID, fragmentRes.Fragment.FragmentID, claim, true)
		category, reason := placementFromClaimOutcome(outcome)
		if category == domain.MemoryPlacementPromotedFact || category == domain.MemoryPlacementValidatedClaim {
			target.Category = domain.MemoryPlacementDisputeAccepted
			target.Status = "completed"
			target.Reason = "Dense-Mem verifier accepted the dispute evidence and corrected the placement."
			target.ClaimID = outcome.ClaimID
			if outcome.Fact != nil {
				target.FactID = outcome.Fact.FactID
			}
			target.UpdatedAt = time.Now().UTC()
			_ = reason
			return target.Reason, true, false, nil
		}
	}
	return "", false, false, nil
}

func (s *service) log() *slog.Logger {
	if s.deps.Logger != nil {
		return s.deps.Logger
	}
	return slog.Default()
}

func extractServerClaim(evidence domain.MemoryEvidence, fragmentID string) (TypedClaimInput, bool) {
	content := strings.TrimSpace(evidence.Content)
	if content == "" {
		return TypedClaimInput{}, false
	}
	lower := strings.ToLower(content)
	patterns := []struct {
		prefix    string
		subject   string
		predicate string
	}{
		{"the user prefers ", "user", "prefers"},
		{"user prefers ", "user", "prefers"},
		{"i prefer ", "user", "prefers"},
		{"the user likes ", "user", "likes"},
		{"user likes ", "user", "likes"},
		{"i like ", "user", "likes"},
		{"the user uses ", "user", "uses"},
		{"user uses ", "user", "uses"},
		{"i use ", "user", "uses"},
		{"the user knows ", "user", "knows"},
		{"user knows ", "user", "knows"},
		{"the user works on ", "user", "works_on"},
		{"user works on ", "user", "works_on"},
		{"the user works at ", "user", "works_at"},
		{"user works at ", "user", "works_at"},
		{"the user has goal ", "user", "has_goal"},
		{"user has goal ", "user", "has_goal"},
		{"dense-mem uses ", "Dense-Mem", "uses"},
		{"dense-mem knows ", "Dense-Mem", "knows"},
	}
	for _, pattern := range patterns {
		if strings.HasPrefix(lower, pattern.prefix) {
			object := strings.TrimSpace(content[len(pattern.prefix):])
			object = strings.Trim(object, " \t\r\n.")
			if object == "" {
				return TypedClaimInput{}, false
			}
			return TypedClaimInput{
				Subject:           pattern.subject,
				Predicate:         pattern.predicate,
				Object:            object,
				Modality:          string(domain.ModalityAssertion),
				Polarity:          string(domain.PolarityPlus),
				Speaker:           "server_adjudicator",
				ExtractConf:       0.75,
				ResolutionConf:    0.75,
				SupportedBy:       []string{fragmentID},
				ExtractionModel:   "dense-mem-server-adjudicator",
				ExtractionVersion: "v2",
				PipelineRunID:     evidence.IdempotencyKey,
				Classification:    map[string]any{"placement": "server_owned"},
			}, true
		}
	}
	return TypedClaimInput{}, false
}

func placementFromClaimOutcome(out ClaimOutcome) (domain.MemoryPlacementCategory, string) {
	if out.Fact != nil || out.Status == "promoted" || out.Promotion == "promoted" {
		return domain.MemoryPlacementPromotedFact, "Dense-Mem verifier validated and promoted the extracted claim to a fact."
	}
	switch out.Status {
	case string(domain.StatusValidated):
		return domain.MemoryPlacementValidatedClaim, "Dense-Mem verifier validated the extracted claim; promotion did not create a fact."
	case string(domain.StatusCandidate):
		return domain.MemoryPlacementCandidateClaim, "Dense-Mem verifier kept the extracted claim as a candidate pending stronger support."
	case string(domain.StatusDisputed), string(domain.StatusRejected):
		return domain.MemoryPlacementRejectedFalse, "Dense-Mem verifier found the extracted claim contradicted or rejected."
	case "predicate_not_supported":
		return domain.MemoryPlacementFragmentOnly, "Evidence was stored as a fragment because the extracted predicate is not promoted by policy."
	case "invalid":
		return domain.MemoryPlacementFragmentOnly, "Evidence was stored as a fragment because the extracted claim was invalid."
	}
	if out.Error != "" {
		return domain.MemoryPlacementNeedsEvidence, "Dense-Mem verifier could not complete placement without more evidence."
	}
	return domain.MemoryPlacementFragmentOnly, "Evidence was stored as a fragment."
}

func evidenceForIndex(evidence []domain.MemoryEvidence, index int) domain.MemoryEvidence {
	for _, item := range evidence {
		if item.Index == index {
			return item
		}
	}
	return domain.MemoryEvidence{}
}

func disputeEvidence(input []EvidenceInput) []domain.MemoryEvidence {
	out := make([]domain.MemoryEvidence, 0, len(input))
	for i, evidence := range input {
		out = append(out, domain.MemoryEvidence{
			Index:          i,
			Content:        strings.TrimSpace(evidence.Content),
			Source:         evidence.Source,
			IdempotencyKey: evidence.IdempotencyKey,
			Labels:         append([]string(nil), evidence.Labels...),
			Metadata:       evidence.Metadata,
		})
	}
	return out
}

func findPlacementItem(run *domain.MemoryPlacementRun, itemID string) *domain.MemoryPlacementItem {
	itemID = strings.TrimSpace(itemID)
	if run == nil || itemID == "" {
		return nil
	}
	for i := range run.Items {
		if run.Items[i].ItemID == itemID {
			return &run.Items[i]
		}
	}
	return nil
}

func evidenceLooksFalse(evidence []EvidenceInput) bool {
	for _, item := range evidence {
		if looksFalse(item.Content) {
			return true
		}
	}
	return false
}

func hasDisputeEvidence(evidence []EvidenceInput) bool {
	for _, item := range evidence {
		if strings.TrimSpace(item.Content) != "" {
			return true
		}
	}
	return false
}
