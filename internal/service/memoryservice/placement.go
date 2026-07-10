package memoryservice

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
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
	Evidence      []EvidenceInput          `json:"evidence"`
	Proposal      domain.MemoryProposal    `json:"proposal"`
	MigrationRefs []domain.LegacyMemoryRef `json:"migration_refs,omitempty"`
}

// PlacementStatusRequest fetches one server-owned placement run.
type PlacementStatusRequest struct {
	IngestID string `json:"ingest_id"`
}

// PlacementStatusResult is the public polling response for remember.
type PlacementStatusResult struct {
	Run domain.MemoryPlacementRun `json:"placement"`
}

type ResolvePlacementRequest struct {
	IngestID          string                 `json:"ingest_id"`
	PlacementItemID   string                 `json:"placement_item_id,omitempty"`
	Decision          string                 `json:"decision"`
	CorrectedProposal *domain.MemoryProposal `json:"corrected_proposal,omitempty"`
	Evidence          []EvidenceInput        `json:"evidence,omitempty"`
}

type ResolvePlacementResult struct {
	Placement domain.MemoryPlacementRun `json:"placement"`
}

// DisputeRequest starts or continues a bounded placement dispute session.
type DisputeRequest struct {
	IngestID        string          `json:"ingest_id,omitempty"`
	PlacementItemID string          `json:"placement_item_id,omitempty"`
	DisputeID       string          `json:"dispute_id,omitempty"`
	Message         string          `json:"message,omitempty"`
	Evidence        []EvidenceInput `json:"evidence,omitempty"`
}

// DisputeResult returns the current dispute session and any updated placement.
type DisputeResult struct {
	Session   domain.MemoryDisputeSession `json:"session"`
	Placement *domain.MemoryPlacementRun  `json:"placement,omitempty"`
}

// Remember stores normal chat-session evidence and queues server-owned
// placement. Client LLMs cannot submit claims or promotion hints on this path.
func (s *service) Remember(ctx context.Context, profileID string, req RememberRequest) (*RememberResult, error) {
	return s.rememberV2(ctx, profileID, req)
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
		return &DisputeResult{Session: *session, Placement: run}, nil
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
	return &DisputeResult{Session: *session, Placement: run}, nil
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
	return s.processPlacementRunV2(ctx, run)
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

func looksFalse(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	for _, marker := range []string{
		"false memory",
		"not true",
		"incorrect",
		"contradicts",
		"contradicted",
		"reject this memory",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
