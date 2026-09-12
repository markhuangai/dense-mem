package contract

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var ErrRecallFeedbackEventNotFound = errors.New("recall feedback event not found")

// FeedbackObservation is the bounded metric projection for one accepted
// recall-feedback submission. It contains no request identity or free-form
// text, so the feedback port cannot create sensitive metric labels.
type FeedbackObservation struct {
	Used            bool
	AnswerSupported bool
	Quality         string
	MissingContext  bool
	Irrelevant      bool
}

// FeedbackMetrics records accepted recall feedback for the Recall capability.
// Implementations may attach request identity through the optional contextual
// extension below.
type FeedbackMetrics interface {
	ObserveRecallFeedback(FeedbackObservation)
}

// ContextualFeedbackMetrics preserves request-scoped metric attribution when
// an implementation supports it without widening the required port.
type ContextualFeedbackMetrics interface {
	FeedbackMetrics
	ObserveRecallFeedbackFor(context.Context, FeedbackObservation)
}

// FeedbackEventRepository is the persistence port for Recall session
// snapshots and their subsequent feedback.
type FeedbackEventRepository interface {
	RecordSnapshot(context.Context, domain.RecallFeedbackEvent) error
	RecordFeedback(context.Context, domain.RecallFeedbackEvent) error
	List(context.Context, domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error)
	Get(context.Context, string) (*domain.RecallFeedbackEvent, error)
	PruneBefore(context.Context, time.Time) error
}
