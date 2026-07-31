package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	appservice "github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

type evaluationAuditStub struct {
	entries []appservice.AuditLogEntry
}

func (s *evaluationAuditStub) Append(_ context.Context, entry appservice.AuditLogEntry) error {
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

func (s stubRecallFeedbackConfig) EvaluationRuntimeConfig(context.Context) (domain.EvaluationRuntimeConfig, error) {
	return domain.EvaluationRuntimeConfig{Enabled: s.enabled}, s.err
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

type stubDreamService struct {
	lastRunReq     dreamservice.RunCycleRequest
	lastListOpts   dreamservice.ListOptions
	lastResolveReq dreamservice.ResolveFeedbackRequest
	recallQuery    string
	recallErr      error
}

func (s *stubDreamService) RunCycle(_ context.Context, profileID string, req dreamservice.RunCycleRequest) (*dreamservice.RunCycleResult, error) {
	s.lastRunReq = req
	return &dreamservice.RunCycleResult{RunID: "run-1", TeamID: profileID, RunDate: "2026-06-11", Status: "completed"}, nil
}

func (s *stubDreamService) RunScheduledCycle(context.Context, string, time.Time) (*dreamservice.RunCycleResult, error) {
	return &dreamservice.RunCycleResult{RunID: "run-1", Status: "completed"}, nil
}

func (s *stubDreamService) RecordMissedScheduledCycle(context.Context, string, string) (*dreamservice.RunCycleResult, error) {
	return &dreamservice.RunCycleResult{RunID: "run-1", Status: "missed"}, nil
}

func (s *stubDreamService) List(_ context.Context, profileID string, opts dreamservice.ListOptions) ([]*domain.Dream, string, error) {
	s.lastListOpts = opts
	return []*domain.Dream{stubDream(profileID)}, "next-dream", nil
}

func (s *stubDreamService) Get(_ context.Context, profileID, dreamID string) (*domain.Dream, error) {
	dream := stubDream(profileID)
	dream.DreamID = dreamID
	return dream, nil
}

func (s *stubDreamService) ListRuns(context.Context, string, int) ([]*dreamservice.RunCycleResult, error) {
	return []*dreamservice.RunCycleResult{{RunID: "run-1", Status: "completed"}}, nil
}

func (s *stubDreamService) Recall(_ context.Context, profileID, query string, _ int) ([]*domain.Dream, error) {
	s.recallQuery = query
	if s.recallErr != nil {
		return nil, s.recallErr
	}
	return []*domain.Dream{stubDream(profileID)}, nil
}

func (s *stubDreamService) ResolveFeedback(_ context.Context, profileID string, req dreamservice.ResolveFeedbackRequest) (*dreamservice.ResolveFeedbackResult, error) {
	s.lastResolveReq = req
	return &dreamservice.ResolveFeedbackResult{Dream: stubDream(profileID)}, nil
}

func (s *stubDreamService) Status(context.Context, string) (*dreamservice.StatusResult, error) {
	return &dreamservice.StatusResult{
		EffectiveConfig: dreamservice.EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, StartTimeLocal: "03:00", Timezone: "UTC", MaxOutputs: 5}},
		LatestRun:       &dreamservice.RunCycleResult{RunID: "run-1", Status: "completed"},
		PendingCount:    1,
	}, nil
}

func (s *stubDreamService) EffectiveConfig(context.Context, string) (dreamservice.EffectiveConfig, error) {
	return dreamservice.EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, StartTimeLocal: "03:00", Timezone: "UTC", MaxOutputs: 5}}, nil
}

func stubDream(profileID string) *domain.Dream {
	return &domain.Dream{
		DreamID:                        "dream-1",
		TeamID:                         profileID,
		Hypothesis:                     "A may affect B.",
		WhatIf:                         "What if A and B interact?",
		PossibleOutcome:                "Review before promotion.",
		Rationale:                      "Eligible relationship context suggests review.",
		SubjectEntityID:                "entity-a",
		PredicateKey:                   "affects",
		ObjectEntityID:                 "entity-b",
		SourceRelationshipIDs:          []string{"relationship-1"},
		SourceCandidateRelationshipIDs: []string{},
		SourceVersions:                 map[string]int{"relationship-1": 1},
		GeneratorKind:                  "deterministic",
		GeneratorVersion:               "dream-v2",
		Status:                         domain.DreamStatusProposed,
		CreatedAt:                      time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
	}
}
