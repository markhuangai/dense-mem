package registry

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type stubRecallFeedbackConfig struct {
	enabled bool
	err     error
}

func (s stubRecallFeedbackConfig) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return domain.RecallFeedbackRuntimeConfig{Enabled: s.enabled}, s.err
}

type stubRecallFeedbackRecorder struct {
	snapshots []domain.RecallFeedbackEvent
	feedback  []domain.RecallFeedbackSubmission
	err       error
	failAfter int
}

func (s *stubRecallFeedbackRecorder) RecordRecallSnapshot(_ context.Context, event domain.RecallFeedbackEvent) error {
	if s.err != nil {
		return s.err
	}
	s.snapshots = append(s.snapshots, event)
	return nil
}

func (s *stubRecallFeedbackRecorder) RecordRecallFeedback(_ context.Context, feedback domain.RecallFeedbackSubmission) error {
	if s.err != nil {
		return s.err
	}
	if s.failAfter > 0 && len(s.feedback) >= s.failAfter {
		return errors.New("stub feedback failure")
	}
	s.feedback = append(s.feedback, feedback)
	return nil
}
