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
	decisionSchema := resolveTool.InputSchema["properties"].(map[string]any)["decision"].(map[string]any)
	decisionEnums := decisionSchema["enum"].([]string)
	for _, decision := range []string{"ignore", "confirm_true", "confirm_false", "promote_candidate"} {
		if !containsString(decisionEnums, decision) {
			t.Fatalf("resolve_dream_feedback decision enum missing %q: %v", decision, decisionEnums)
		}
	}
	resolveOut, err := resolveTool.Invoke(context.Background(), "profile-dream", map[string]any{
		"dream_id": "dream-1",
		"decision": "confirm_true",
		"feedback": "prior conversation confirms it",
	})
	if err != nil {
		t.Fatalf("resolve_dream_feedback Invoke: %v", err)
	}
	if resolveOut["dream"] == nil || dreams.lastResolveReq.Decision != "confirm_true" || dreams.lastResolveReq.Feedback != "prior conversation confirms it" {
		t.Fatalf("resolve_dream_feedback output = %v request = %+v", resolveOut, dreams.lastResolveReq)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type stubDreamService struct {
	lastRunReq     dreamservice.RunCycleRequest
	lastListOpts   dreamservice.ListOptions
	lastResolveReq dreamservice.ResolveFeedbackRequest
	recallQuery    string
	recallErr      error
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

func (s *stubDreamService) ListRuns(context.Context, string, int) ([]*dreamservice.RunCycleResult, error) {
	return []*dreamservice.RunCycleResult{{RunID: "run-1", Status: "completed"}}, nil
}

func (s *stubDreamService) Recall(ctx context.Context, profileID, query string, limit int) ([]*domain.Dream, error) {
	s.recallQuery = query
	if s.recallErr != nil {
		return nil, s.recallErr
	}
	return []*domain.Dream{stubDream(profileID)}, nil
}

func (s *stubDreamService) ResolveFeedback(ctx context.Context, profileID string, req dreamservice.ResolveFeedbackRequest) (*dreamservice.ResolveFeedbackResult, error) {
	s.lastResolveReq = req
	return &dreamservice.ResolveFeedbackResult{Dream: stubDream(profileID)}, nil
}

func (s *stubDreamService) Status(ctx context.Context, profileID string) (*dreamservice.StatusResult, error) {
	return &dreamservice.StatusResult{
		EffectiveConfig: dreamservice.EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, StartTimeLocal: "03:00", Timezone: "UTC", MaxOutputs: 5}},
		LatestRun:       &dreamservice.RunCycleResult{RunID: "run-1", ProfileID: profileID, Status: "completed"},
		PendingCount:    1,
	}, nil
}

func (s *stubDreamService) EffectiveConfig(context.Context, string) (dreamservice.EffectiveConfig, error) {
	return dreamservice.EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, StartTimeLocal: "03:00", Timezone: "UTC", MaxOutputs: 5}}, nil
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
