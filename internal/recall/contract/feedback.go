package contract

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var ErrRecallFeedbackEventNotFound = errors.New("recall feedback event not found")

// FeedbackEventRepository is the persistence port for Recall session
// snapshots and their subsequent feedback.
type FeedbackEventRepository interface {
	RecordSnapshot(context.Context, domain.RecallFeedbackEvent) error
	RecordFeedback(context.Context, domain.RecallFeedbackEvent) error
	List(context.Context, domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error)
	Get(context.Context, string) (*domain.RecallFeedbackEvent, error)
	PruneBefore(context.Context, time.Time) error
}
