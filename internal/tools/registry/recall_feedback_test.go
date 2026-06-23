package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type stubRecallFeedbackConfig struct {
	enabled bool
	err     error
}

func (s stubRecallFeedbackConfig) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return domain.RecallFeedbackRuntimeConfig{Enabled: s.enabled}, s.err
}

func TestBuildDefault_RegistersRecallSessionFeedbackTool(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	if _, ok := reg.Get("submit_recall_session_feedback"); !ok {
		t.Fatal("submit_recall_session_feedback not registered")
	}
	if _, ok := reg.Get("submit_recall_feedback"); ok {
		t.Fatal("deprecated submit_recall_feedback must not be registered")
	}
}

func TestSubmitRecallSessionFeedbackRejectsInvalidQuality(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	})
	tool, _ := reg.Get("submit_recall_session_feedback")

	_, err := tool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recalls": []any{map[string]any{
			"recall_id":        "rec-1",
			"used":             true,
			"answer_supported": true,
			"quality":          "excellent",
			"missing_context":  false,
			"irrelevant":       false,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "submit_recall_session_feedback: recalls[0].quality must be one of") {
		t.Fatalf("err = %v; want invalid quality", err)
	}
}

func TestSubmitRecallSessionFeedbackDisabledByRuntimeConfig(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: false},
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	})
	tool, _ := reg.Get("submit_recall_session_feedback")

	_, err := tool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recalls": []any{map[string]any{
			"recall_id":        "rec_1",
			"used":             true,
			"answer_supported": true,
			"quality":          "high",
			"missing_context":  false,
			"irrelevant":       false,
		}},
	})
	if err == nil || !errors.Is(err, ErrToolDisabled) {
		t.Fatalf("err = %v; want disabled tool", err)
	}
}

func TestToolVisibleGatesRecallFeedbackTool(t *testing.T) {
	ctx := context.Background()
	feedbackTool := Tool{Name: SubmitRecallSessionFeedbackToolName}

	if !ToolVisible(ctx, Tool{Name: "recall_memory"}, nil) {
		t.Fatal("non-feedback tool should be visible without runtime config")
	}
	if ToolVisible(ctx, feedbackTool, nil) {
		t.Fatal("feedback tool should be hidden without runtime config")
	}
	if ToolVisible(ctx, feedbackTool, stubRecallFeedbackConfig{enabled: false}) {
		t.Fatal("feedback tool should be hidden when runtime config disables it")
	}
	if !ToolVisible(ctx, feedbackTool, stubRecallFeedbackConfig{enabled: true}) {
		t.Fatal("feedback tool should be visible when runtime config enables it")
	}
	if ToolVisible(ctx, feedbackTool, stubRecallFeedbackConfig{enabled: true, err: errors.New("config unavailable")}) {
		t.Fatal("feedback tool should be hidden when runtime config is unavailable")
	}
}

func TestRecallMemoryRecallEventDisabledByDefault(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{Recall: stubRecallWithHit{}})
	tool, _ := reg.Get("recall_memory")

	out, err := tool.Invoke(context.Background(), "profile-feedback", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	if _, ok := out["recall_event"]; ok {
		t.Fatalf("recall_event present when disabled: %v", out["recall_event"])
	}
}

func TestRecallMemoryRecallEventRequiresMetrics(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{
		Recall:               stubRecallWithHit{},
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
	})
	tool, _ := reg.Get("recall_memory")

	out, err := tool.Invoke(context.Background(), "profile-feedback", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	if _, ok := out["recall_event"]; ok {
		t.Fatalf("recall_event present without metrics: %v", out["recall_event"])
	}
}

func TestRecallMemoryRecallEventAndSessionSubmit(t *testing.T) {
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	recorder := &stubRecallFeedbackRecorder{}
	reg, _ := BuildDefault(Dependencies{
		Recall:               stubRecallWithHit{},
		Metrics:              metrics,
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
	})
	recallTool, _ := reg.Get("recall_memory")

	out, err := recallTool.Invoke(context.Background(), "profile-feedback", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	event, ok := out["recall_event"].(map[string]any)
	if !ok {
		t.Fatalf("recall_event missing or wrong type: %v", out["recall_event"])
	}
	recallID, _ := event["recall_id"].(string)
	if !strings.HasPrefix(recallID, "rec_") || event["feedback_tool"] != SubmitRecallSessionFeedbackToolName || event["feedback_timing"] != "deferred_until_final_answer" {
		t.Fatalf("recall_event = %v", event)
	}
	if len(recorder.snapshots) != 1 {
		t.Fatalf("recorded snapshots = %d; want 1", len(recorder.snapshots))
	}
	snapshot := recorder.snapshots[0]
	if snapshot.RecallID != recallID || snapshot.Query != "q" {
		t.Fatalf("snapshot = %+v; want recall id and query", snapshot)
	}
	if len(snapshot.ResultRefs) != 1 || snapshot.ResultRefs[0].Type != domain.RecallFeedbackResultTypeFragment || snapshot.ResultRefs[0].ID != "fragment-hit" {
		t.Fatalf("snapshot result refs = %+v", snapshot.ResultRefs)
	}
	if _, ok := snapshot.ToolArgs["input"].(map[string]any); !ok {
		t.Fatalf("snapshot tool args = %#v; want input map", snapshot.ToolArgs)
	}

	submitTool, ok := reg.Get("submit_recall_session_feedback")
	if !ok {
		t.Fatal("submit_recall_session_feedback not registered")
	}
	submitOut, err := submitTool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recalls": []any{map[string]any{
			"recall_id":        recallID,
			"used":             true,
			"answer_supported": true,
			"quality":          "high",
			"missing_context":  false,
			"irrelevant":       false,
		}},
	})
	if err != nil {
		t.Fatalf("submit_recall_session_feedback Invoke: %v", err)
	}
	if submitOut["recorded"] != true {
		t.Fatalf("submit output = %v", submitOut)
	}
	if submitOut["recorded_count"] != 1 {
		t.Fatalf("submit output = %v", submitOut)
	}
	samples := metrics.RecallFeedbackSamples()
	if len(samples) != 1 {
		t.Fatalf("recall feedback samples = %d; want 1", len(samples))
	}
	if !samples[0].Used || !samples[0].AnswerSupported || samples[0].Quality != "high" || samples[0].QualityScore != 1 {
		t.Fatalf("recall feedback sample = %+v", samples[0])
	}
	if len(recorder.feedback) != 1 {
		t.Fatalf("recorded feedback = %d; want 1", len(recorder.feedback))
	}
	if recorder.feedback[0].RecallID != recallID || recorder.feedback[0].Quality != "high" {
		t.Fatalf("recorded feedback = %+v", recorder.feedback[0])
	}
}

func TestRecallFeedbackRecorderErrorsDoNotFailTools(t *testing.T) {
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	reg, _ := BuildDefault(Dependencies{
		Recall:               stubRecallWithHit{},
		Metrics:              metrics,
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: &stubRecallFeedbackRecorder{err: errors.New("record failed")},
	})
	recallTool, _ := reg.Get("recall_memory")
	out, err := recallTool.Invoke(context.Background(), "profile-feedback", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	event := out["recall_event"].(map[string]any)
	recallID := event["recall_id"].(string)

	submitTool, _ := reg.Get("submit_recall_session_feedback")
	_, err = submitTool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recalls": []any{map[string]any{
			"recall_id":        recallID,
			"used":             true,
			"answer_supported": false,
			"quality":          "low",
			"missing_context":  true,
			"irrelevant":       false,
		}},
	})
	if err != nil {
		t.Fatalf("submit_recall_session_feedback Invoke: %v", err)
	}
	if len(metrics.RecallFeedbackSamples()) != 1 {
		t.Fatalf("metrics samples = %d; want 1", len(metrics.RecallFeedbackSamples()))
	}
}

type stubRecallFeedbackRecorder struct {
	snapshots []domain.RecallFeedbackEvent
	feedback  []domain.RecallFeedbackSubmission
	err       error
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
	s.feedback = append(s.feedback, feedback)
	return nil
}
