package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const recallFeedbackEventPruneInterval = time.Hour

type RecallFeedbackEventReader interface {
	ListRecallFeedbackEvents(ctx context.Context, filter domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error)
	GetRecallFeedbackEvent(ctx context.Context, recallID string) (*domain.RecallFeedbackEvent, error)
}

type RecallFeedbackEventRecorder interface {
	RecordRecallSnapshot(ctx context.Context, event domain.RecallFeedbackEvent) error
	RecordRecallFeedback(ctx context.Context, feedback domain.RecallFeedbackSubmission) error
}

type RecallFeedbackEventService interface {
	RecallFeedbackEventReader
	RecallFeedbackEventRecorder
	Prune(ctx context.Context) error
	Start(ctx context.Context)
	Shutdown(ctx context.Context) error
}

type RecallFeedbackRetentionProvider interface {
	RecallFeedbackRuntimeConfig(ctx context.Context) (domain.RecallFeedbackRuntimeConfig, error)
}

type RecallFeedbackResultResolver interface {
	ResolveRecallFeedbackResults(ctx context.Context, profileID string, refs []domain.RecallFeedbackResultRef) ([]domain.RecallFeedbackResolvedResult, error)
}

type RecallFeedbackEventServiceImpl struct {
	repo      repository.RecallFeedbackEventRepository
	retention RecallFeedbackRetentionProvider
	resolver  RecallFeedbackResultResolver
	now       func() time.Time

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
}

var _ RecallFeedbackEventService = (*RecallFeedbackEventServiceImpl)(nil)

func NewRecallFeedbackEventService(
	repo repository.RecallFeedbackEventRepository,
	retention RecallFeedbackRetentionProvider,
	resolver RecallFeedbackResultResolver,
) *RecallFeedbackEventServiceImpl {
	return &RecallFeedbackEventServiceImpl{
		repo:      repo,
		retention: retention,
		resolver:  resolver,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (s *RecallFeedbackEventServiceImpl) RecordRecallSnapshot(ctx context.Context, event domain.RecallFeedbackEvent) error {
	if s == nil || s.repo == nil {
		return nil
	}
	event = s.enrich(ctx, event)
	event.SnapshotState = domain.RecallFeedbackSnapshotCaptured
	if strings.TrimSpace(event.RecallID) == "" {
		return fmt.Errorf("recall feedback event recall_id is required")
	}
	return s.repo.RecordSnapshot(ctx, event)
}

func (s *RecallFeedbackEventServiceImpl) RecordRecallFeedback(ctx context.Context, feedback domain.RecallFeedbackSubmission) error {
	if s == nil || s.repo == nil {
		return nil
	}
	now := s.now().UTC()
	recallID := strings.TrimSpace(feedback.RecallID)
	if recallID == "" {
		return fmt.Errorf("recall feedback event recall_id is required")
	}
	existing, err := s.repo.Get(ctx, recallID)
	if err != nil {
		return err
	}
	if existing == nil || !recallFeedbackEventAuthorizedForSubmission(ctx, existing) {
		return repository.ErrRecallFeedbackEventNotFound
	}
	if existing.SnapshotState != domain.RecallFeedbackSnapshotCaptured {
		return fmt.Errorf("recall feedback event snapshot is required")
	}
	if err := validateRecallFeedbackSubmissionRefs(feedback, existing.ResultRefs); err != nil {
		return err
	}
	used := feedback.Used
	answerSupported := feedback.AnswerSupported
	missingContext := feedback.MissingContext
	irrelevant := feedback.Irrelevant
	event := *existing
	event = s.enrich(ctx, event)
	event.UpdatedAt = now
	event.FeedbackAt = &now
	event.SnapshotState = domain.RecallFeedbackSnapshotCaptured
	event.Used = &used
	event.AnswerSupported = &answerSupported
	event.Quality = strings.ToLower(strings.TrimSpace(feedback.Quality))
	event.MissingContext = &missingContext
	event.Irrelevant = &irrelevant
	event.FeedbackComment = strings.TrimSpace(feedback.FeedbackComment)
	event.IrrelevantRefs = feedback.IrrelevantRefs
	event.DreamFeedback = feedback.DreamFeedback
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	return s.repo.RecordFeedback(ctx, event)
}

func (s *RecallFeedbackEventServiceImpl) ListRecallFeedbackEvents(ctx context.Context, filter domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("recall feedback event service unavailable")
	}
	return s.repo.List(ctx, filter)
}

func (s *RecallFeedbackEventServiceImpl) GetRecallFeedbackEvent(ctx context.Context, recallID string) (*domain.RecallFeedbackEvent, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("recall feedback event service unavailable")
	}
	event, err := s.repo.Get(ctx, recallID)
	if err != nil || event == nil {
		return event, err
	}
	if s.resolver != nil && len(event.ResultRefs) > 0 {
		scopeID := ""
		if event.TeamID != nil {
			scopeID = event.TeamID.String()
		} else if event.ProfileID != nil {
			scopeID = event.ProfileID.String()
		}
		if scopeID != "" {
			resolved, resolveErr := s.resolver.ResolveRecallFeedbackResults(ctx, scopeID, event.ResultRefs)
			if resolveErr != nil {
				return nil, resolveErr
			}
			event.ResolvedResults = resolved
		}
	}
	return event, nil
}

func (s *RecallFeedbackEventServiceImpl) Prune(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	days := DefaultRecallFeedbackRetentionDays
	if s.retention != nil {
		cfg, err := s.retention.RecallFeedbackRuntimeConfig(ctx)
		if err != nil {
			return fmt.Errorf("failed to read recall feedback retention config: %w", err)
		}
		if cfg.RetentionDays <= 0 {
			return fmt.Errorf("invalid recall feedback retention days: %d", cfg.RetentionDays)
		}
		days = cfg.RetentionDays
	}
	cutoff := s.now().UTC().AddDate(0, 0, -days)
	return s.repo.PruneBefore(ctx, cutoff)
}

func (s *RecallFeedbackEventServiceImpl) Start(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	go s.run(runCtx, done)
}

func (s *RecallFeedbackEventServiceImpl) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *RecallFeedbackEventServiceImpl) enrich(ctx context.Context, event domain.RecallFeedbackEvent) domain.RecallFeedbackEvent {
	now := s.now().UTC()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if event.UpdatedAt.IsZero() {
		event.UpdatedAt = now
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok {
		if event.TeamID == nil && actor.TeamID != uuid.Nil {
			event.TeamID = &actor.TeamID
		}
		if event.ProfileID == nil && actor.OwnerID != uuid.Nil {
			event.ProfileID = &actor.OwnerID
		}
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok {
		if event.KeyID == nil && actor.CredentialID != nil {
			credentialID := *actor.CredentialID
			event.KeyID = &credentialID
		}
		if event.AuthMethod == "" {
			event.AuthMethod = actor.AuthMethod
		}
	}
	if event.ToolName == "" {
		event.ToolName = "recall_memory"
	}
	return event
}

func recallFeedbackEventAuthorizedForSubmission(ctx context.Context, event *domain.RecallFeedbackEvent) bool {
	if event == nil {
		return false
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil {
		return true
	}
	if event.TeamID != nil {
		return *event.TeamID == actor.TeamID
	}
	if event.ProfileID != nil && actor.OwnerID != uuid.Nil {
		return *event.ProfileID == actor.OwnerID
	}
	return false
}

func validateRecallFeedbackSubmissionRefs(feedback domain.RecallFeedbackSubmission, returned []domain.RecallFeedbackResultRef) error {
	lookup := recallFeedbackReturnedRefSet(returned)
	seen := map[string]struct{}{}
	for _, ref := range feedback.IrrelevantRefs {
		key := recallFeedbackRefKey(ref.Type, ref.ID)
		if key == "" {
			return fmt.Errorf("recall feedback result ref was not returned by recall event")
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("recall feedback result refs must not contain duplicates")
		}
		seen[key] = struct{}{}
		if !lookup.contains(ref.Type, ref.ID, ref.Rank) {
			return fmt.Errorf("recall feedback result ref was not returned by recall event")
		}
	}
	seenHypotheses := map[string]struct{}{}
	for _, item := range feedback.DreamFeedback {
		key := recallFeedbackRefKey(domain.RecallFeedbackResultTypeHypothesis, item.DreamID)
		if key == "" {
			return fmt.Errorf("recall feedback hypothesis ref was not returned by recall event")
		}
		if _, ok := seenHypotheses[key]; ok {
			return fmt.Errorf("recall feedback hypothesis refs must not contain duplicates")
		}
		seenHypotheses[key] = struct{}{}
		if !lookup.contains(domain.RecallFeedbackResultTypeHypothesis, item.DreamID, 0) {
			return fmt.Errorf("recall feedback hypothesis ref was not returned by recall event")
		}
	}
	return nil
}

type recallFeedbackReturnedRefs map[string]map[int]struct{}

func recallFeedbackReturnedRefSet(refs []domain.RecallFeedbackResultRef) recallFeedbackReturnedRefs {
	out := recallFeedbackReturnedRefs{}
	for _, ref := range refs {
		key := recallFeedbackRefKey(ref.Type, ref.ID)
		if key == "" {
			continue
		}
		ranks := out[key]
		if ranks == nil {
			ranks = map[int]struct{}{}
			out[key] = ranks
		}
		ranks[ref.Rank] = struct{}{}
	}
	return out
}

func (refs recallFeedbackReturnedRefs) contains(refType, id string, rank int) bool {
	key := recallFeedbackRefKey(refType, id)
	if key == "" {
		return false
	}
	ranks, ok := refs[key]
	if !ok {
		return false
	}
	if rank <= 0 {
		return true
	}
	_, ok = ranks[rank]
	return ok
}

func recallFeedbackRefKey(refType, id string) string {
	refType = recallFeedbackCanonicalResultType(refType)
	id = strings.TrimSpace(id)
	if refType == "" || id == "" {
		return ""
	}
	return refType + "\x00" + id
}

func recallFeedbackCanonicalResultType(refType string) string {
	switch strings.TrimSpace(refType) {
	case domain.RecallFeedbackResultTypeDream, domain.RecallFeedbackResultTypeHypothesis:
		return domain.RecallFeedbackResultTypeHypothesis
	case domain.RecallFeedbackResultTypeFragment,
		domain.RecallFeedbackResultTypeClaim,
		domain.RecallFeedbackResultTypeFact,
		domain.RecallFeedbackResultTypeEvidence,
		domain.RecallFeedbackResultTypeRelationship,
		domain.RecallFeedbackResultTypeEntity,
		domain.RecallFeedbackResultTypeValue,
		domain.RecallFeedbackResultTypeCommunity:
		return strings.TrimSpace(refType)
	default:
		return ""
	}
}

func (s *RecallFeedbackEventServiceImpl) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	_ = s.Prune(ctx)
	ticker := time.NewTicker(recallFeedbackEventPruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Prune(ctx)
		}
	}
}
