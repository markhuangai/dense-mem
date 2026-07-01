package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

type stubRecallFeedbackConfig struct {
	enabled bool
	err     error
}

func (s stubRecallFeedbackConfig) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return domain.RecallFeedbackRuntimeConfig{Enabled: s.enabled}, s.err
}

type stubEvaluationConfig struct {
	enabled bool
	err     error
}

func (s stubEvaluationConfig) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return domain.RecallFeedbackRuntimeConfig{}, nil
}

func (s stubEvaluationConfig) EvaluationRuntimeConfig(context.Context) (domain.EvaluationRuntimeConfig, error) {
	return domain.EvaluationRuntimeConfig{Enabled: s.enabled}, s.err
}

func TestBuildDefault_RegistersRecallSessionFeedbackTool(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, ok := reg.Get("submit_recall_session_feedback")
	if !ok {
		t.Fatal("submit_recall_session_feedback not registered")
	}
	if got := strings.Join(tool.RequiredScopes, ","); got != "write" {
		t.Fatalf("submit_recall_session_feedback scopes = %v; want [write]", tool.RequiredScopes)
	}
	if _, ok := reg.Get("submit_recall_feedback"); ok {
		t.Fatal("deprecated submit_recall_feedback must not be registered")
	}
}

func TestBuildDefault_RegistersEvaluationToolsWithReadWriteScope(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	for name := range evaluationToolNames {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if got := strings.Join(tool.RequiredScopes, ","); got != "read,write" {
			t.Fatalf("%s scopes = %v; want [read write]", name, tool.RequiredScopes)
		}
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

func TestSubmitRecallSessionFeedbackRequiresCommentForNonHighFeedback(t *testing.T) {
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
			"quality":          "medium",
			"missing_context":  false,
			"irrelevant":       false,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "recalls[0].feedback_comment is required") {
		t.Fatalf("err = %v; want missing feedback comment", err)
	}
}

func TestSubmitRecallSessionFeedbackAcceptsLegacyCommentFields(t *testing.T) {
	recorder := &stubRecallFeedbackRecorder{}
	reg, _ := BuildDefault(Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	})
	tool, _ := reg.Get("submit_recall_session_feedback")

	_, err := tool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recalls": []any{map[string]any{
			"recall_id":        "rec-1",
			"used":             true,
			"answer_supported": false,
			"quality":          "low",
			"missing_context":  true,
			"irrelevant":       false,
			"failure_reason":   "legacy fallback comment",
		}},
	})
	if err != nil {
		t.Fatalf("submit_recall_session_feedback Invoke: %v", err)
	}
	if len(recorder.feedback) != 1 || recorder.feedback[0].FeedbackComment != "legacy fallback comment" {
		t.Fatalf("recorded feedback = %+v", recorder.feedback)
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

func TestToolVisibleGatesEvaluationTools(t *testing.T) {
	ctx := context.Background()
	evalTool := Tool{Name: "eval_get_manifest"}

	if !ToolVisible(ctx, Tool{Name: "recall_memory"}, nil) {
		t.Fatal("non-evaluation tool should be visible without runtime config")
	}
	if ToolVisible(ctx, evalTool, nil) {
		t.Fatal("evaluation tool should be hidden without runtime config")
	}
	if ToolVisible(ctx, evalTool, stubRecallFeedbackConfig{enabled: true}) {
		t.Fatal("evaluation tool should be hidden when runtime config cannot provide evaluation settings")
	}
	if ToolVisible(ctx, evalTool, stubEvaluationConfig{enabled: false}) {
		t.Fatal("evaluation tool should be hidden when evaluation mode is disabled")
	}
	if !ToolVisible(ctx, evalTool, stubEvaluationConfig{enabled: true}) {
		t.Fatal("evaluation tool should be visible when evaluation mode is enabled")
	}
	if ToolVisible(ctx, evalTool, stubEvaluationConfig{enabled: true, err: errors.New("config unavailable")}) {
		t.Fatal("evaluation tool should be hidden when runtime config is unavailable")
	}
}

func TestSubmitRecallSessionFeedbackRecordsFailureDetails(t *testing.T) {
	recorder := &stubRecallFeedbackRecorder{}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	reg, _ := BuildDefault(Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
		Metrics:              metrics,
	})
	tool, _ := reg.Get("submit_recall_session_feedback")

	_, err := tool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recalls": []any{map[string]any{
			"recall_id":        " rec-1 ",
			"used":             true,
			"answer_supported": false,
			"quality":          "low",
			"missing_context":  true,
			"irrelevant":       true,
			"feedback_comment": " Returned stale UI redesign notes instead of the button disabled-state pattern. Existing SearchPanel button handling pattern. ",
			"irrelevant_result_refs": []any{map[string]any{
				"type": "fragment",
				"id":   " fragment-1 ",
				"rank": 1,
			}},
			"dream_feedback": []any{map[string]any{
				"dream_id":         " dream-1 ",
				"used":             true,
				"quality":          "medium",
				"contradicted":     false,
				"feedback_comment": " Plausible relationship but missing stronger source overlap. ",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("submit_recall_session_feedback Invoke: %v", err)
	}
	if len(recorder.feedback) != 1 {
		t.Fatalf("recorded feedback = %d; want 1", len(recorder.feedback))
	}
	got := recorder.feedback[0]
	if got.RecallID != "rec-1" {
		t.Fatalf("recorded feedback = %+v", got)
	}
	if got.FeedbackComment != "Returned stale UI redesign notes instead of the button disabled-state pattern. Existing SearchPanel button handling pattern." {
		t.Fatalf("feedback_comment = %q", got.FeedbackComment)
	}
	if len(got.IrrelevantRefs) != 1 || got.IrrelevantRefs[0].ID != "fragment-1" || got.IrrelevantRefs[0].Rank != 1 {
		t.Fatalf("irrelevant refs = %+v", got.IrrelevantRefs)
	}
	if len(got.DreamFeedback) != 1 || got.DreamFeedback[0].DreamID != "dream-1" || got.DreamFeedback[0].Quality != "medium" || got.DreamFeedback[0].FeedbackComment != "Plausible relationship but missing stronger source overlap." {
		t.Fatalf("dream feedback = %+v", got.DreamFeedback)
	}
	if len(metrics.RecallFeedbackSamples()) != 1 {
		t.Fatalf("recall feedback samples = %d; want 1", len(metrics.RecallFeedbackSamples()))
	}
}

func TestSubmitRecallSessionFeedbackAcceptsDreamIrrelevantRefs(t *testing.T) {
	recorder := &stubRecallFeedbackRecorder{}
	reg, _ := BuildDefault(Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	})
	tool, _ := reg.Get("submit_recall_session_feedback")

	_, err := tool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recalls": []any{map[string]any{
			"recall_id":        "rec-1",
			"used":             true,
			"answer_supported": false,
			"quality":          "low",
			"missing_context":  false,
			"irrelevant":       true,
			"feedback_comment": "Dream hypothesis contradicted the retrieved facts.",
			"irrelevant_result_refs": []any{map[string]any{
				"type": "dream",
				"id":   "dream-1",
				"rank": 1,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("submit_recall_session_feedback Invoke: %v", err)
	}
	got := recorder.feedback[0]
	if len(got.IrrelevantRefs) != 1 || got.IrrelevantRefs[0].Type != domain.RecallFeedbackResultTypeDream {
		t.Fatalf("irrelevant refs = %+v", got.IrrelevantRefs)
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

func TestRecallMemoryRecallEventIncludesDreamRefs(t *testing.T) {
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	recorder := &stubRecallFeedbackRecorder{}
	reg, _ := BuildDefault(Dependencies{
		Recall:               stubRecallWithHit{},
		Dreams:               &stubDreamService{},
		Metrics:              metrics,
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
	})
	recallTool, _ := reg.Get("recall_memory")

	_, err := recallTool.Invoke(context.Background(), "profile-feedback", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	if len(recorder.snapshots) != 1 {
		t.Fatalf("recorded snapshots = %d; want 1", len(recorder.snapshots))
	}
	refs := recorder.snapshots[0].ResultRefs
	if len(refs) != 2 {
		t.Fatalf("snapshot result refs = %+v; want memory and dream refs", refs)
	}
	if refs[1].Type != domain.RecallFeedbackResultTypeDream || refs[1].ID != "dream-1" || refs[1].Tier != "dream" {
		t.Fatalf("dream ref = %+v", refs[1])
	}
}

func TestRecallFeedbackRecorderErrorsSuppressRecallEventAndFailFeedbackSubmit(t *testing.T) {
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
	if _, ok := out["recall_event"]; ok {
		t.Fatalf("recall_event should be omitted when snapshot persistence fails: %v", out["recall_event"])
	}

	submitTool, _ := reg.Get("submit_recall_session_feedback")
	_, err = submitTool.Invoke(context.Background(), "profile-feedback", map[string]any{
		"recalls": []any{map[string]any{
			"recall_id":        "rec_failed",
			"used":             true,
			"answer_supported": false,
			"quality":          "low",
			"missing_context":  true,
			"irrelevant":       false,
			"feedback_comment": "persistence failure should stop metrics recording",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "failed to record recalls[0]") {
		t.Fatalf("submit_recall_session_feedback err = %v; want persistence failure", err)
	}
	if len(metrics.RecallFeedbackSamples()) != 0 {
		t.Fatalf("metrics samples = %d; want 0 after persistence failure", len(metrics.RecallFeedbackSamples()))
	}
}

func TestRecallFeedbackHelpersCaptureArgsAndResultRefs(t *testing.T) {
	validAt := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	knownAt := time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC)
	recordedAt := time.Date(2026, 6, 23, 9, 0, 0, 0, time.UTC)
	retractedAt := time.Date(2026, 6, 23, 9, 30, 0, 0, time.UTC)

	args := recallFeedbackToolArgs(map[string]any{
		"query":            "why",
		"limit":            float64(3),
		"valid_at":         validAt.Format(time.RFC3339),
		"known_at":         knownAt.Format(time.RFC3339),
		"include_evidence": true,
		"use_communities":  true,
		"ignored":          "not persisted",
	}, recallservice.RecallRequest{
		Query:           "why",
		Limit:           3,
		ValidAt:         &validAt,
		KnownAt:         &knownAt,
		IncludeEvidence: true,
		UseCommunities:  true,
	})
	input := args["input"].(map[string]any)
	if _, ok := input["ignored"]; ok {
		t.Fatalf("input copy persisted ignored key: %#v", input)
	}
	effective := args["effective"].(map[string]any)
	if effective["valid_at"] != validAt.Format(time.RFC3339Nano) || effective["known_at"] != knownAt.Format(time.RFC3339Nano) {
		t.Fatalf("effective time args = %#v", effective)
	}

	refs := recallFeedbackResultRefs([]recallservice.RecallHit{
		{
			Tier:       recallservice.TierActiveFact,
			Score:      0.9,
			FinalScore: 0.95,
			Fact: &domain.Fact{
				FactID:      "fact-1",
				Status:      domain.FactStatusRetracted,
				RecordedAt:  recordedAt,
				ValidFrom:   &validAt,
				ValidTo:     &knownAt,
				RetractedAt: &retractedAt,
			},
		},
		{
			Tier: recallservice.TierValidatedClaim,
			Claim: &domain.Claim{
				ClaimID:    "claim-1",
				Status:     domain.StatusValidated,
				RecordedAt: recordedAt,
				ValidFrom:  &validAt,
				ValidTo:    &knownAt,
			},
		},
		{
			Tier:         recallservice.TierFragment,
			SemanticRank: 2,
			KeywordRank:  3,
			Fragment: &domain.Fragment{
				FragmentID:  "fragment-1",
				Status:      domain.FragmentStatusRetracted,
				CreatedAt:   recordedAt,
				UpdatedAt:   knownAt,
				RetractedAt: &retractedAt,
			},
		},
		{},
	}, nil)

	if len(refs) != 3 {
		t.Fatalf("refs = %#v; want 3 persisted refs", refs)
	}
	if refs[0].Type != domain.RecallFeedbackResultTypeFact || refs[0].ID != "fact-1" || refs[0].Score == nil || refs[0].FinalScore == nil || refs[0].RetractedAt == nil {
		t.Fatalf("fact ref = %+v", refs[0])
	}
	if refs[1].Type != domain.RecallFeedbackResultTypeClaim || refs[1].ID != "claim-1" || refs[1].Score != nil || refs[1].FinalScore != nil {
		t.Fatalf("claim ref = %+v", refs[1])
	}
	if refs[2].Type != domain.RecallFeedbackResultTypeFragment || refs[2].ID != "fragment-1" || refs[2].SemanticRank != 2 || refs[2].KeywordRank != 3 {
		t.Fatalf("fragment ref = %+v", refs[2])
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
