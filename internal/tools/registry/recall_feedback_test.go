package registry

import (
	"context"
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

func TestBuildDefault_RegistersRecallFeedbackTool(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	if _, ok := reg.Get("submit_recall_feedback"); !ok {
		t.Fatal("submit_recall_feedback not registered")
	}
}

func TestSubmitRecallFeedbackRejectsInvalidQuality(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	})
	tool, _ := reg.Get("submit_recall_feedback")

	_, err := tool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recall_id":        "rec-1",
		"used":             true,
		"answer_supported": true,
		"quality":          "excellent",
		"missing_context":  false,
		"irrelevant":       false,
	})
	if err == nil || !strings.Contains(err.Error(), "submit_recall_feedback: quality must be one of") {
		t.Fatalf("err = %v; want invalid quality", err)
	}
}

func TestSubmitRecallFeedbackDisabledByRuntimeConfig(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: false},
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	})
	tool, _ := reg.Get("submit_recall_feedback")

	_, err := tool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recall_id":        "rec_1",
		"used":             true,
		"answer_supported": true,
		"quality":          "high",
		"missing_context":  false,
		"irrelevant":       false,
	})
	if err == nil || !strings.Contains(err.Error(), "tool disabled") {
		t.Fatalf("err = %v; want disabled tool", err)
	}
}

func TestRecallMemoryFeedbackRequestDisabledByDefault(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{Recall: stubRecallWithHit{}})
	tool, _ := reg.Get("recall_memory")

	out, err := tool.Invoke(context.Background(), "profile-feedback", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	if _, ok := out["feedback_request"]; ok {
		t.Fatalf("feedback_request present when disabled: %v", out["feedback_request"])
	}
}

func TestRecallMemoryFeedbackRequestAndSubmit(t *testing.T) {
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	reg, _ := BuildDefault(Dependencies{
		Recall:               stubRecallWithHit{},
		Metrics:              metrics,
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
	})
	recallTool, _ := reg.Get("recall_memory")

	out, err := recallTool.Invoke(context.Background(), "profile-feedback", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	request, ok := out["feedback_request"].(map[string]any)
	if !ok {
		t.Fatalf("feedback_request missing or wrong type: %v", out["feedback_request"])
	}
	recallID, _ := request["recall_id"].(string)
	if !strings.HasPrefix(recallID, "rec_") || request["tool"] != "submit_recall_feedback" {
		t.Fatalf("feedback_request = %v", request)
	}

	submitTool, ok := reg.Get("submit_recall_feedback")
	if !ok {
		t.Fatal("submit_recall_feedback not registered")
	}
	submitOut, err := submitTool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recall_id":        recallID,
		"used":             true,
		"answer_supported": true,
		"quality":          "high",
		"missing_context":  false,
		"irrelevant":       false,
	})
	if err != nil {
		t.Fatalf("submit_recall_feedback Invoke: %v", err)
	}
	if submitOut["recorded"] != true {
		t.Fatalf("submit output = %v", submitOut)
	}
	samples := metrics.RecallFeedbackSamples()
	if len(samples) != 1 {
		t.Fatalf("recall feedback samples = %d; want 1", len(samples))
	}
	if !samples[0].Used || !samples[0].AnswerSupported || samples[0].Quality != "high" || samples[0].QualityScore != 1 {
		t.Fatalf("recall feedback sample = %+v", samples[0])
	}
}
