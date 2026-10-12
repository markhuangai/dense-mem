package registry

import (
	"context"
	"errors"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type evaluationAuditStub struct {
	entries []accessservice.AuditLogEntry
}

func (s *evaluationAuditStub) Append(_ context.Context, entry accessservice.AuditLogEntry) error {
	s.entries = append(s.entries, entry)
	return nil
}

func firstEvalItem(t *testing.T, page map[string]any) map[string]any {
	t.Helper()
	items, ok := page["items"].([]map[string]any)
	if !ok || len(items) == 0 {
		t.Fatalf("page items = %#v", page["items"])
	}
	return items[0]
}

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
