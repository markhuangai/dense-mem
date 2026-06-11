package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

func TestBuildDefault_DreamInvokers(t *testing.T) {
	dreams := &stubDreamService{}
	reg, _ := BuildDefault(Dependencies{Dreams: dreams})

	statusTool, _ := reg.Get("dreaming_status")
	statusOut, err := statusTool.Invoke(context.Background(), "profile-dream", map[string]any{})
	if err != nil {
		t.Fatalf("dreaming_status Invoke: %v", err)
	}
	if statusOut["pending_count"] != float64(1) {
		t.Fatalf("dreaming_status pending_count = %v, want 1", statusOut["pending_count"])
	}

	runTool, _ := reg.Get("run_dreaming_cycle")
	runOut, err := runTool.Invoke(context.Background(), "profile-dream", map[string]any{
		"reflect_enabled":    false,
		"reevaluate_enabled": true,
		"dream_enabled":      true,
		"max_outputs":        float64(4),
	})
	if err != nil {
		t.Fatalf("run_dreaming_cycle Invoke: %v", err)
	}
	if runOut["status"] != "completed" {
		t.Fatalf("run_dreaming_cycle status = %v, want completed", runOut["status"])
	}
	if !dreams.lastRunReq.Manual || dreams.lastRunReq.ReflectEnabled == nil || *dreams.lastRunReq.ReflectEnabled {
		t.Fatalf("run_dreaming_cycle request = %+v, want manual with reflect override false", dreams.lastRunReq)
	}
	if dreams.lastRunReq.ReevaluateEnabled == nil || !*dreams.lastRunReq.ReevaluateEnabled || dreams.lastRunReq.MaxOutputs != 4 {
		t.Fatalf("run_dreaming_cycle request = %+v, want reevaluate true and max outputs 4", dreams.lastRunReq)
	}

	listTool, _ := reg.Get("list_dreams")
	listOut, err := listTool.Invoke(context.Background(), "profile-dream", map[string]any{
		"limit":  float64(3),
		"status": "proposed",
	})
	if err != nil {
		t.Fatalf("list_dreams Invoke: %v", err)
	}
	listed := listOut["dreams"].([]*domain.Dream)
	if len(listed) != 1 || dreams.lastListOpts.Limit != 3 || dreams.lastListOpts.Status != "proposed" {
		t.Fatalf("list_dreams output = %v opts = %+v", listOut, dreams.lastListOpts)
	}

	getTool, _ := reg.Get("get_dream")
	getOut, err := getTool.Invoke(context.Background(), "profile-dream", map[string]any{"dream_id": "dream-1"})
	if err != nil {
		t.Fatalf("get_dream Invoke: %v", err)
	}
	if getOut["dream_id"] != "dream-1" {
		t.Fatalf("get_dream dream_id = %v, want dream-1", getOut["dream_id"])
	}
	if _, err := getTool.Invoke(context.Background(), "profile-dream", map[string]any{}); err == nil || !strings.Contains(err.Error(), "dream_id is required") {
		t.Fatalf("get_dream missing id err = %v", err)
	}

	resolveTool, _ := reg.Get("resolve_dream_feedback")
	resolveOut, err := resolveTool.Invoke(context.Background(), "profile-dream", map[string]any{
		"dream_id": "dream-1",
		"decision": "reinforce",
		"feedback": "still plausible",
	})
	if err != nil {
		t.Fatalf("resolve_dream_feedback Invoke: %v", err)
	}
	if resolveOut["dream"] == nil || dreams.lastResolveReq.Decision != "reinforce" || dreams.lastResolveReq.Feedback != "still plausible" {
		t.Fatalf("resolve_dream_feedback output = %v request = %+v", resolveOut, dreams.lastResolveReq)
	}
}

type stubDreamService struct {
	lastRunReq     dreamservice.RunCycleRequest
	lastListOpts   dreamservice.ListOptions
	lastResolveReq dreamservice.ResolveFeedbackRequest
	recallQuery    string
}

func (s *stubDreamService) RunCycle(ctx context.Context, profileID string, req dreamservice.RunCycleRequest) (*dreamservice.RunCycleResult, error) {
	s.lastRunReq = req
	return &dreamservice.RunCycleResult{RunID: "run-1", ProfileID: profileID, RunDate: "2026-06-11", Status: "completed"}, nil
}

func (s *stubDreamService) List(ctx context.Context, profileID string, opts dreamservice.ListOptions) ([]*domain.Dream, string, error) {
	s.lastListOpts = opts
	return []*domain.Dream{stubDream(profileID)}, "next-dream", nil
}

func (s *stubDreamService) Get(ctx context.Context, profileID, dreamID string) (*domain.Dream, error) {
	dream := stubDream(profileID)
	dream.DreamID = dreamID
	return dream, nil
}

func (s *stubDreamService) Recall(ctx context.Context, profileID, query string, limit int) ([]*domain.Dream, error) {
	s.recallQuery = query
	return []*domain.Dream{stubDream(profileID)}, nil
}

func (s *stubDreamService) ResolveFeedback(ctx context.Context, profileID string, req dreamservice.ResolveFeedbackRequest) (*dreamservice.ResolveFeedbackResult, error) {
	s.lastResolveReq = req
	return &dreamservice.ResolveFeedbackResult{Dream: stubDream(profileID)}, nil
}

func (s *stubDreamService) Status(ctx context.Context, profileID string) (*dreamservice.StatusResult, error) {
	return &dreamservice.StatusResult{
		EffectiveConfig: dreamservice.EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, StartTimeLocal: "03:00", Timezone: "UTC", MaxOutputs: 5}},
		LatestRun:       &dreamservice.RunCycleResult{RunID: "run-1", ProfileID: profileID, Status: "completed"},
		PendingCount:    1,
	}, nil
}

func (s *stubDreamService) EffectiveConfig(context.Context, string) (dreamservice.EffectiveConfig, error) {
	return dreamservice.EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, StartTimeLocal: "03:00", Timezone: "UTC", MaxOutputs: 5}}, nil
}

func stubDream(profileID string) *domain.Dream {
	return &domain.Dream{
		DreamID:         "dream-1",
		ProfileID:       profileID,
		Hypothesis:      "A may affect B.",
		WhatIf:          "What if A and B interact?",
		PossibleOutcome: "Review before promotion.",
		Status:          domain.DreamStatusProposed,
	}
}
