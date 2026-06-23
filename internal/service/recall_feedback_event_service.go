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
	used := feedback.Used
	answerSupported := feedback.AnswerSupported
	missingContext := feedback.MissingContext
	irrelevant := feedback.Irrelevant
	event := s.enrich(ctx, domain.RecallFeedbackEvent{
		RecallID:        strings.TrimSpace(feedback.RecallID),
		CreatedAt:       now,
		UpdatedAt:       now,
		FeedbackAt:      &now,
		ToolName:        "recall_memory",
		ToolArgs:        map[string]any{},
		ResultRefs:      []domain.RecallFeedbackResultRef{},
		SnapshotState:   domain.RecallFeedbackSnapshotFeedbackOnly,
		Used:            &used,
		AnswerSupported: &answerSupported,
		Quality:         strings.ToLower(strings.TrimSpace(feedback.Quality)),
		MissingContext:  &missingContext,
		Irrelevant:      &irrelevant,
	})
	if event.RecallID == "" {
		return fmt.Errorf("recall feedback event recall_id is required")
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
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok {
		if event.TeamID == nil && actor.TeamID != uuid.Nil {
			event.TeamID = &actor.TeamID
		}
		if event.ProfileID == nil && actor.ProfileID != uuid.Nil {
			event.ProfileID = &actor.ProfileID
		}
	}
	if credential, ok := requestctx.ActorCredentialFromContext(ctx); ok {
		if event.KeyID == nil && credential.KeyID != uuid.Nil {
			event.KeyID = &credential.KeyID
		}
		if event.AuthMethod == "" {
			event.AuthMethod = credential.AuthMethod
		}
	}
	if event.ToolName == "" {
		event.ToolName = "recall_memory"
	}
	return event
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
