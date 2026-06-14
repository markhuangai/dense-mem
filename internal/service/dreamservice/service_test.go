package dreamservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRunCycleSkipsExistingScheduledRunButAllowsManual(t *testing.T) {
	cfg := domain.DreamingRuntimeConfig{
		Enabled:           true,
		StartTimeLocal:    "03:00",
		Timezone:          "UTC",
		ReflectEnabled:    false,
		ReevaluateEnabled: false,
		DreamEnabled:      false,
		MaxOutputs:        5,
	}
	graph := &cycleRunGraphStub{existingRun: true}
	svc := New(Dependencies{
		Graph:     graph,
		AppConfig: cycleAppConfigStub{cfg: cfg},
		Now: func() time.Time {
			return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
		},
	})

	result, err := svc.RunCycle(context.Background(), "profile-1", RunCycleRequest{MaxOutputs: 3})
	require.NoError(t, err)
	require.Equal(t, "skipped", result.Status)
	require.Equal(t, 0, graph.writes)

	result, err = svc.RunCycle(context.Background(), "profile-1", RunCycleRequest{Manual: true})
	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 1, graph.writes)
}

func TestRunCycleValidationDisabledAndReflectError(t *testing.T) {
	_, err := New(Dependencies{}).RunCycle(context.Background(), "", RunCycleRequest{})
	require.ErrorContains(t, err, "profile id is required")

	disabled := New(Dependencies{
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:           false,
			StartTimeLocal:    "03:00",
			Timezone:          "UTC",
			ReflectEnabled:    true,
			ReevaluateEnabled: true,
			DreamEnabled:      true,
			MaxOutputs:        5,
		}},
		Now: func() time.Time { return time.Date(2026, 6, 11, 4, 0, 0, 0, time.UTC) },
	})
	result, err := disabled.RunCycle(context.Background(), "profile-1", RunCycleRequest{})
	require.NoError(t, err)
	require.Equal(t, "skipped", result.Status)
	require.Equal(t, "2026-06-11", result.RunDate)

	graph := &cycleRunGraphStub{executeWrites: true}
	reflectErr := New(Dependencies{
		Graph:  graph,
		Memory: &dreamMemoryStub{err: errors.New("reflect failed")},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:           true,
			StartTimeLocal:    "03:00",
			Timezone:          "UTC",
			ReflectEnabled:    true,
			ReevaluateEnabled: false,
			DreamEnabled:      false,
			MaxOutputs:        5,
		}},
		Now: func() time.Time { return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC) },
	})
	result, err = reflectErr.RunCycle(context.Background(), "profile-1", RunCycleRequest{})
	require.ErrorContains(t, err, "reflect phase: reflect failed")
	require.Equal(t, "error", result.Status)
	require.Contains(t, result.Error, "reflect phase")
	require.Equal(t, 1, graph.writes)
	require.True(t, hasDreamWriteQuery(graph.writeQueries, "DreamCycleRun"))
}

func TestRunCycleRunsEnabledPhasesAndWritesDreams(t *testing.T) {
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	graph := &cycleRunGraphStub{
		executeWrites: true,
		inputRows: []map[string]any{
			{"type": "fact", "id": "fact-1", "subject": "assistant", "predicate": "uses", "object": "dense-mem", "status": "active"},
			{"type": "claim", "id": "claim-1", "subject": "workflow", "predicate": "needs", "object": "review", "status": "candidate"},
		},
	}
	memory := &dreamMemoryStub{result: &memoryservice.ReflectResult{
		StaleFacts:      []*domain.Fact{{FactID: "fact-stale"}},
		CandidateClaims: []*domain.Claim{{ClaimID: "claim-candidate"}},
		DisputedClaims:  []*domain.Claim{{ClaimID: "claim-disputed"}},
		Clarifications:  []memoryservice.Clarification{{ID: "clarification-1"}},
	}}
	generator := &dreamGeneratorStub{
		model: "stub-model",
		generated: []GeneratedDream{
			{
				Hypothesis:      "A nightly workflow may surface stale review work.",
				WhatIf:          "What if dense-mem connects stale facts with candidate claims?",
				PossibleOutcome: "Ask for review before promotion.",
				Rationale:       "Pairs unrelated inputs for review.",
				Likelihood:      1.4,
				Confidence:      -0.2,
				SourceRefs:      []domain.DreamSourceRef{{Type: "fact", ID: "fact-1"}},
			},
			{
				Hypothesis: "",
				SourceRefs: []domain.DreamSourceRef{{Type: "claim", ID: "claim-1"}},
			},
		},
	}
	svc := New(Dependencies{
		Graph:     graph,
		Memory:    memory,
		Generator: generator,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:           true,
			StartTimeLocal:    "03:00",
			Timezone:          "UTC",
			ReflectEnabled:    true,
			ReevaluateEnabled: true,
			DreamEnabled:      true,
			MaxOutputs:        2,
			Model:             "configured-model",
		}},
		Now: func() time.Time { return now },
	})

	result, err := svc.RunCycle(context.Background(), "profile-1", RunCycleRequest{MaxOutputs: 3})

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.True(t, result.ReflectRan)
	require.True(t, result.ReevaluateRan)
	require.True(t, result.DreamRan)
	require.Equal(t, 1, result.StaleFacts)
	require.Equal(t, 1, result.CandidateClaims)
	require.Equal(t, 1, result.DisputedClaims)
	require.Equal(t, 1, result.Clarifications)
	require.Equal(t, 2, result.ReevaluatedDreams)
	require.Equal(t, 1, result.CreatedDreams)
	require.Equal(t, 1, memory.reflects)
	require.Equal(t, 3, graph.writes)
	require.Equal(t, 3, generator.lastReq.MaxOutputs)
	require.Equal(t, "configured-model", generator.lastReq.GeneratorModel)
	require.Len(t, generator.lastReq.Inputs, 2)
	require.True(t, hasDreamWriteQuery(graph.writeQueries, "MERGE (d:Dream"))
	require.True(t, hasDreamWriteQuery(graph.writeQueries, "DREAMS_FROM"))
	require.True(t, hasDreamWriteQuery(graph.writeQueries, "DreamCycleRun"))
}

func TestRunCycleRecordsGeneratorError(t *testing.T) {
	graph := &cycleRunGraphStub{
		executeWrites: true,
		inputRows: []map[string]any{
			{"type": "fact", "id": "fact-1", "subject": "assistant"},
			{"type": "claim", "id": "claim-1", "subject": "workflow"},
		},
	}
	svc := New(Dependencies{
		Graph:     graph,
		Generator: &dreamGeneratorStub{err: errors.New("generate failed")},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:           true,
			StartTimeLocal:    "03:00",
			Timezone:          "UTC",
			ReflectEnabled:    false,
			ReevaluateEnabled: false,
			DreamEnabled:      true,
			MaxOutputs:        2,
		}},
		Now: func() time.Time { return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC) },
	})

	result, err := svc.RunCycle(context.Background(), "profile-1", RunCycleRequest{})

	require.ErrorContains(t, err, "dream phase: generate failed")
	require.Equal(t, "error", result.Status)
	require.Contains(t, result.Error, "dream phase: generate failed")
	require.Equal(t, 1, graph.writes)
	require.True(t, hasDreamWriteQuery(graph.writeQueries, "DreamCycleRun"))
}

func TestRunCycleRecordsLockError(t *testing.T) {
	graph := &cycleRunGraphStub{executeWrites: true}
	svc := New(Dependencies{
		Graph:    graph,
		Locker:   dreamLockerStub{err: errors.New("lock failed")},
		Postgres: &gorm.DB{},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:           true,
			StartTimeLocal:    "03:00",
			Timezone:          "UTC",
			ReflectEnabled:    false,
			ReevaluateEnabled: false,
			DreamEnabled:      false,
			MaxOutputs:        5,
		}},
		Now: func() time.Time { return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC) },
	})

	result, err := svc.RunCycle(context.Background(), "profile-1", RunCycleRequest{})

	require.ErrorContains(t, err, "lock failed")
	require.Equal(t, "error", result.Status)
	require.Contains(t, result.Error, "lock failed")
	require.Equal(t, 1, graph.writes)
	require.True(t, hasDreamWriteQuery(graph.writeQueries, "DreamCycleRun"))
}

func TestDreamServiceReadPaths(t *testing.T) {
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	graph := &cycleRunGraphStub{
		dreamRows: []map[string]any{{"d": dreamTestNode("dream-1", "profile-1", now)}},
		runRows: []map[string]any{{"r": neo4j.Node{Props: map[string]any{
			"run_id":             "run-1",
			"run_date":           "2026-06-11",
			"started_at":         now,
			"completed_at":       now.Add(time.Minute),
			"status":             "completed",
			"reflect_ran":        true,
			"reevaluate_ran":     true,
			"dream_ran":          true,
			"stale_facts":        int64(1),
			"candidate_claims":   int64(2),
			"disputed_claims":    int64(3),
			"clarifications":     int64(4),
			"reevaluated_dreams": int64(5),
			"created_dreams":     int64(6),
		}}}},
	}
	svc := New(Dependencies{
		Graph: graph,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:           true,
			StartTimeLocal:    "03:00",
			Timezone:          "UTC",
			ReflectEnabled:    true,
			ReevaluateEnabled: true,
			DreamEnabled:      true,
			MaxOutputs:        5,
		}},
	})

	dreams, next, err := svc.List(context.Background(), "profile-1", ListOptions{Limit: 500, Status: string(domain.DreamStatusProposed)})
	require.NoError(t, err)
	require.Empty(t, next)
	require.Len(t, dreams, 1)
	require.Equal(t, "dream-1", dreams[0].DreamID)
	require.Equal(t, []domain.DreamSourceRef{{Type: "fact", ID: "fact-1"}}, dreams[0].SourceRefs)

	dream, err := svc.Get(context.Background(), "profile-1", "dream-1")
	require.NoError(t, err)
	require.Equal(t, "dream-1", dream.DreamID)

	recalled, err := svc.Recall(context.Background(), "profile-1", "workflow", 100)
	require.NoError(t, err)
	require.Len(t, recalled, 1)

	emptyRecall, err := svc.Recall(context.Background(), "profile-1", "", 5)
	require.NoError(t, err)
	require.Nil(t, emptyRecall)

	status, err := svc.Status(context.Background(), "profile-1")
	require.NoError(t, err)
	require.Equal(t, 1, status.PendingCount)
	require.NotNil(t, status.LatestRun)
	require.Equal(t, "run-1", status.LatestRun.RunID)
}

func TestDreamServiceResolveFeedback(t *testing.T) {
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	graph := &cycleRunGraphStub{executeWrites: true, dreamRows: []map[string]any{{"d": dreamTestNode("dream-1", "profile-1", now)}}}
	svc := New(Dependencies{Graph: graph})

	res, err := svc.ResolveFeedback(context.Background(), "profile-1", ResolveFeedbackRequest{
		DreamID:  "dream-1",
		Decision: "reject",
		Feedback: "not true",
	})
	require.NoError(t, err)
	require.Equal(t, "dream-1", res.Dream.DreamID)
	require.Equal(t, 1, graph.writes)

	_, err = svc.ResolveFeedback(context.Background(), "profile-1", ResolveFeedbackRequest{
		DreamID:  "dream-1",
		Decision: "stale",
		Feedback: "source disappeared",
	})
	require.NoError(t, err)

	_, err = svc.ResolveFeedback(context.Background(), "profile-1", ResolveFeedbackRequest{
		DreamID:  "dream-1",
		Decision: "reinforce",
		Feedback: "still useful",
	})
	require.NoError(t, err)
	require.Equal(t, 3, graph.writes)

	_, err = svc.ResolveFeedback(context.Background(), "profile-1", ResolveFeedbackRequest{
		DreamID:  "dream-1",
		Decision: "unknown",
	})
	require.ErrorIs(t, err, ErrInvalidDreamStatus)

	_, err = svc.ResolveFeedback(context.Background(), "profile-1", ResolveFeedbackRequest{Decision: "reject"})
	require.ErrorContains(t, err, "dream_id is required")
}

func TestDreamServiceResolveFeedbackPromotesCandidate(t *testing.T) {
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	graph := &cycleRunGraphStub{executeWrites: true, dreamRows: []map[string]any{{"d": dreamTestNode("dream-1", "profile-1", now)}}}
	create := &dreamFragmentCreateStub{}
	svc := New(Dependencies{
		Graph:          graph,
		FragmentCreate: create,
		Now:            func() time.Time { return now },
	})

	res, err := svc.ResolveFeedback(context.Background(), "profile-1", ResolveFeedbackRequest{
		DreamID:  "dream-1",
		Decision: "promote_candidate",
		Feedback: "confirmed by user",
	})

	require.NoError(t, err)
	require.NotNil(t, res.Fragment)
	require.Equal(t, "dream-fragment", res.Fragment.Fragment.FragmentID)
	require.Equal(t, "profile-1", create.lastProfile)
	require.Equal(t, "dream_feedback:dream-1", create.lastReq.Source)
	require.Equal(t, "dream-1", create.lastReq.Metadata["dream_id"])
	require.Equal(t, 2, graph.writes)
	require.True(t, hasDreamWriteQuery(graph.writeQueries, "PROMOTED_TO"))
	require.True(t, hasDreamWriteQuery(graph.writeQueries, "SET d.status = $status"))

	_, err = New(Dependencies{Graph: graph}).ResolveFeedback(context.Background(), "profile-1", ResolveFeedbackRequest{
		DreamID:  "dream-1",
		Decision: "promote_candidate",
	})
	require.ErrorContains(t, err, "fragment create service is required")
}

func TestDreamServiceEffectiveConfigUsesProfileOverrides(t *testing.T) {
	profileID := uuid.New()
	svc := New(Dependencies{
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:           false,
			StartTimeLocal:    "03:00",
			Timezone:          "UTC",
			ReflectEnabled:    true,
			ReevaluateEnabled: true,
			DreamEnabled:      true,
			MaxOutputs:        5,
		}},
		Profiles: dreamProfileStub{profile: &domain.Profile{
			ID: profileID,
			Config: map[string]any{
				"dreaming": map[string]string{
					"enabled":          "true",
					"start_time_local": "04:30",
					"timezone":         "America/New_York",
					"model":            "dream-model",
					"max_outputs":      "3",
				},
			},
		}},
	})

	cfg, err := svc.EffectiveConfig(context.Background(), profileID.String())

	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.True(t, cfg.TeamEnabled)
	require.Equal(t, "team", cfg.Source)
	require.Equal(t, "04:30", cfg.StartTimeLocal)
	require.Equal(t, "America/New_York", cfg.Timezone)
	require.Equal(t, "dream-model", cfg.Model)
	require.Equal(t, 3, cfg.MaxOutputs)
}

func TestDreamServiceNotFoundAndInternalBranches(t *testing.T) {
	ctx := context.Background()
	svc := New(Dependencies{Graph: &cycleRunGraphStub{}}).(*service)

	_, err := svc.Get(ctx, "profile-1", "missing")
	require.ErrorIs(t, err, ErrDreamNotFound)

	err = svc.updateDreamStatus(ctx, "profile-1", "missing", domain.DreamStatus("bad"), "")
	require.ErrorIs(t, err, ErrInvalidDreamStatus)

	err = svc.updateDreamStatus(ctx, "profile-1", "missing", domain.DreamStatusProposed, "")
	require.ErrorIs(t, err, ErrDreamNotFound)

	_, err = New(Dependencies{}).(*service).reevaluateDreams(ctx, "profile-1", time.Now())
	require.ErrorContains(t, err, "graph is required")

	exists, err := New(Dependencies{}).(*service).cycleRunExists(ctx, "profile-1", "2026-06-11")
	require.NoError(t, err)
	require.False(t, exists)

	latest, err := New(Dependencies{}).(*service).latestRun(ctx, "profile-1")
	require.NoError(t, err)
	require.Nil(t, latest)

	require.NoError(t, New(Dependencies{}).(*service).recordRun(ctx, "profile-1", nil))
	_, err = New(Dependencies{}).(*service).loadDreamInputs(ctx, "profile-1", 1)
	require.ErrorContains(t, err, "graph is required")
}

func TestDreamServiceGenerateAndUpsertBranches(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	oneInput := &cycleRunGraphStub{inputRows: []map[string]any{{
		"type": "fact", "id": "fact-1", "subject": "assistant",
	}}}
	svc := New(Dependencies{Graph: oneInput, Now: func() time.Time { return now }}).(*service)

	created, err := svc.generateDreams(ctx, "profile-1", "run-1", EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{MaxOutputs: 1}}, nil)
	require.NoError(t, err)
	require.Zero(t, created)

	dream := &domain.Dream{
		DreamID:         "dream-1",
		Hypothesis:      "A may affect B.",
		WhatIf:          "What if A and B interact?",
		PossibleOutcome: "Review before promotion.",
		Status:          domain.DreamStatusProposed,
		Cycle:           CycleDream,
		CycleRunID:      "run-1",
		SourceRefs:      []domain.DreamSourceRef{{Type: "fact", ID: "fact-1"}},
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	dream.ContentHash = dreamContentHash(dream)

	noRecordGraph := &cycleRunGraphStub{
		executeWrites: true,
		recordsFor: func(query string, params map[string]any) []*neo4j.Record {
			return []*neo4j.Record{}
		},
	}
	inserted, err := New(Dependencies{Graph: noRecordGraph}).(*service).upsertDream(ctx, "profile-1", dream)
	require.NoError(t, err)
	require.False(t, inserted)

	existingGraph := &cycleRunGraphStub{
		executeWrites: true,
		recordsFor: func(query string, params map[string]any) []*neo4j.Record {
			if strings.Contains(query, "RETURN d.dream_id AS dream_id") {
				return []*neo4j.Record{{Keys: []string{"dream_id", "inserted"}, Values: []any{"dream-existing", false}}}
			}
			return []*neo4j.Record{}
		},
	}
	inserted, err = New(Dependencies{Graph: existingGraph}).(*service).upsertDream(ctx, "profile-1", dream)
	require.NoError(t, err)
	require.False(t, inserted)
	require.Equal(t, "dream-existing", dream.DreamID)

	invalidSource := *dream
	invalidSource.DreamID = "dream-invalid"
	invalidSource.SourceRefs = []domain.DreamSourceRef{{Type: "bad", ID: "source-1"}}
	_, err = New(Dependencies{Graph: &cycleRunGraphStub{executeWrites: true}}).(*service).upsertDream(ctx, "profile-1", &invalidSource)
	require.ErrorContains(t, err, "unsupported dream source type")
}

func TestDreamServiceInputRowsAndHelpers(t *testing.T) {
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	graph := &cycleRunGraphStub{inputRows: []map[string]any{{
		"type": "fact", "id": "fact-1", "subject": "assistant", "predicate": "uses", "object": "dense-mem", "content": "", "status": "active",
	}}}
	svc := New(Dependencies{Graph: graph}).(*service)
	inputs, err := svc.loadDreamInputs(context.Background(), "profile-1", 0)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, "fact-1", inputs[0].ID)

	for sourceType, want := range map[string][2]string{
		"fact":      {"Fact", "fact_id"},
		"claim":     {"Claim", "claim_id"},
		"fragment":  {"SourceFragment", "fragment_id"},
		"community": {"Community", "community_id"},
		"dream":     {"Dream", "dream_id"},
	} {
		label, id, err := sourceLabelAndID(sourceType)
		require.NoError(t, err)
		require.Equal(t, want[0], label)
		require.Equal(t, want[1], id)
	}
	_, _, err = sourceLabelAndID("bad")
	require.Error(t, err)

	dream := &domain.Dream{
		DreamID:         "dream-1",
		Hypothesis:      "A may affect B.",
		WhatIf:          "What if A and B interact?",
		PossibleOutcome: "Check B before promotion.",
		SourceRefs:      []domain.DreamSourceRef{{Type: "fact", ID: "fact-1"}},
	}
	require.NotEmpty(t, dreamContentHash(dream))
	require.Contains(t, dreamPromotionContent(dream, "confirmed"), "Human feedback: confirmed")
	require.Equal(t, "2026-06-11", localRunDate(now, EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Timezone: "Nope/Zone"}}))
	require.Equal(t, "stringer-value", stringFromMap(map[string]any{"x": testStringer("stringer-value")}, "x"))
	require.True(t, boolFromMap(map[string]any{"x": true}, "x"))
	explicitFalse := false
	require.False(t, boolValue(&explicitFalse, true))
	require.True(t, boolValue(nil, true))
	require.Equal(t, 7, intFromRow(map[string]any{"x": 7}, "x"))
	require.Equal(t, 2, intFromRow(map[string]any{"x": float64(2)}, "x"))
	require.Equal(t, 2.0, floatFromMap(map[string]any{"x": int64(2)}, "x"))
	require.Equal(t, 2.5, floatFromMap(map[string]any{"x": float32(2.5)}, "x"))
	require.Equal(t, now, timeFromMap(map[string]any{"x": now}, "x"))
	_, err = parseProfileID(uuid.NewString())
	require.NoError(t, err)
	_, err = parseProfileID("not-a-uuid")
	require.ErrorContains(t, err, "invalid profile id")
}

func TestDreamServiceReadErrors(t *testing.T) {
	graph := &cycleRunGraphStub{readErr: errors.New("read failed")}
	svc := New(Dependencies{Graph: graph})

	_, _, err := svc.List(context.Background(), "profile-1", ListOptions{})
	require.ErrorContains(t, err, "read failed")

	_, err = svc.Get(context.Background(), "profile-1", "dream-1")
	require.ErrorContains(t, err, "read failed")

	_, err = svc.Recall(context.Background(), "profile-1", "query", 5)
	require.ErrorContains(t, err, "read failed")

	_, _, err = New(Dependencies{}).List(context.Background(), "profile-1", ListOptions{})
	require.ErrorContains(t, err, "graph is required")
}

type cycleAppConfigStub struct {
	cfg domain.DreamingRuntimeConfig
}

func (s cycleAppConfigStub) DreamingRuntimeConfig(context.Context) (domain.DreamingRuntimeConfig, error) {
	return s.cfg, nil
}

type dreamLockerStub struct {
	err error
}

func (s dreamLockerStub) WithCycleLock(context.Context, *gorm.DB, string, string, time.Duration, func(tx *gorm.DB) error) error {
	return s.err
}

type dreamProfileStub struct {
	profile *domain.Profile
}

func (s dreamProfileStub) GetByID(context.Context, uuid.UUID) (*domain.Profile, error) {
	return s.profile, nil
}

func (s dreamProfileStub) List(context.Context, int, int) ([]*domain.Profile, error) {
	return nil, nil
}

type cycleRunGraphStub struct {
	existingRun   bool
	writes        int
	readErr       error
	writeErr      error
	executeWrites bool
	writeQueries  []string
	dreamRows     []map[string]any
	runRows       []map[string]any
	inputRows     []map[string]any
	recordsFor    func(query string, params map[string]any) []*neo4j.Record
}

func (s *cycleRunGraphStub) ScopedRead(_ context.Context, _ string, query string, _ map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	if s.readErr != nil {
		return nil, nil, s.readErr
	}
	if s.existingRun && strings.Contains(query, "DreamCycleRun") {
		return nil, []map[string]any{{"run_id": "run-1"}}, nil
	}
	if strings.Contains(query, "DreamCycleRun") {
		return nil, s.runRows, nil
	}
	if strings.Contains(query, "RETURN type, id, subject") {
		return nil, s.inputRows, nil
	}
	if strings.Contains(query, "Dream") || strings.Contains(query, "dream_recall_idx") {
		return nil, s.dreamRows, nil
	}
	return nil, nil, nil
}

func (s *cycleRunGraphStub) ScopedWriteTx(_ context.Context, _ string, fn func(tx neo4j.ManagedTransaction) error) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	if s.executeWrites {
		return fn(&dreamTxStub{queries: &s.writeQueries, recordsFor: s.recordsFor})
	}
	return nil
}

type dreamTxStub struct {
	neo4j.ManagedTransaction
	queries    *[]string
	recordsFor func(query string, params map[string]any) []*neo4j.Record
}

func (s *dreamTxStub) Run(_ context.Context, query string, params map[string]any) (neo4j.ResultWithContext, error) {
	if s.queries != nil {
		*s.queries = append(*s.queries, query)
	}
	var records []*neo4j.Record
	if s.recordsFor != nil {
		records = s.recordsFor(query, params)
	}
	if records == nil {
		records = defaultDreamWriteRecords(query, params)
	}
	return &dreamResultStub{records: records, open: true}, nil
}

type dreamResultStub struct {
	neo4j.ResultWithContext
	records []*neo4j.Record
	index   int
	current *neo4j.Record
	err     error
	open    bool
}

func (s *dreamResultStub) Keys() ([]string, error) {
	if len(s.records) == 0 {
		return nil, nil
	}
	return s.records[0].Keys, nil
}

func (s *dreamResultStub) NextRecord(ctx context.Context, record **neo4j.Record) bool {
	ok := s.Next(ctx)
	if ok && record != nil {
		*record = s.current
	}
	return ok
}

func (s *dreamResultStub) Next(_ context.Context) bool {
	if s.index >= len(s.records) {
		s.current = nil
		return false
	}
	s.current = s.records[s.index]
	s.index++
	return true
}

func (s *dreamResultStub) PeekRecord(_ context.Context, record **neo4j.Record) bool {
	if s.index >= len(s.records) {
		return false
	}
	if record != nil {
		*record = s.records[s.index]
	}
	return true
}

func (s *dreamResultStub) Peek(_ context.Context) bool {
	return s.index < len(s.records)
}

func (s *dreamResultStub) Err() error {
	return s.err
}

func (s *dreamResultStub) Record() *neo4j.Record {
	return s.current
}

func (s *dreamResultStub) Collect(_ context.Context) ([]*neo4j.Record, error) {
	if s.index >= len(s.records) {
		return nil, s.err
	}
	remaining := s.records[s.index:]
	s.index = len(s.records)
	return remaining, s.err
}

func (s *dreamResultStub) Records(ctx context.Context) func(yield func(*neo4j.Record, error) bool) {
	return func(yield func(*neo4j.Record, error) bool) {
		for s.Next(ctx) {
			if !yield(s.Record(), nil) {
				return
			}
		}
		if s.err != nil {
			yield(nil, s.err)
		}
	}
}

func (s *dreamResultStub) Single(ctx context.Context) (*neo4j.Record, error) {
	records, err := s.Collect(ctx)
	if err != nil {
		return nil, err
	}
	if len(records) != 1 {
		return nil, errors.New("expected exactly one record")
	}
	s.current = records[0]
	return records[0], nil
}

func (s *dreamResultStub) Consume(_ context.Context) (neo4j.ResultSummary, error) {
	s.index = len(s.records)
	s.open = false
	return nil, s.err
}

func (s *dreamResultStub) IsOpen() bool {
	return s.open
}

func defaultDreamWriteRecords(query string, _ map[string]any) []*neo4j.Record {
	switch {
	case strings.Contains(query, "RETURN d.dream_id AS dream_id"):
		return []*neo4j.Record{{Keys: []string{"dream_id", "inserted"}, Values: []any{"dream-written", true}}}
	case strings.Contains(query, "RETURN count(d) AS updated"):
		return []*neo4j.Record{{Keys: []string{"updated"}, Values: []any{int64(2)}}}
	default:
		return []*neo4j.Record{}
	}
}

func hasDreamWriteQuery(queries []string, substr string) bool {
	for _, query := range queries {
		if strings.Contains(query, substr) {
			return true
		}
	}
	return false
}

type dreamGeneratorStub struct {
	model     string
	generated []GeneratedDream
	err       error
	lastReq   GenerateRequest
}

func (s *dreamGeneratorStub) Generate(_ context.Context, _ string, req GenerateRequest) ([]GeneratedDream, error) {
	s.lastReq = req
	return s.generated, s.err
}

func (s *dreamGeneratorStub) Model() string {
	if s.model != "" {
		return s.model
	}
	return "stub-model"
}

type dreamMemoryStub struct {
	result   *memoryservice.ReflectResult
	err      error
	reflects int
}

func (s *dreamMemoryStub) Remember(context.Context, string, memoryservice.RememberRequest) (*memoryservice.RememberResult, error) {
	return &memoryservice.RememberResult{}, nil
}

func (s *dreamMemoryStub) ImportMemories(context.Context, string, memoryservice.ImportRequest) (*memoryservice.RememberResult, error) {
	return &memoryservice.RememberResult{}, nil
}

func (s *dreamMemoryStub) Reflect(context.Context, string, memoryservice.ReflectRequest) (*memoryservice.ReflectResult, error) {
	s.reflects++
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &memoryservice.ReflectResult{}, nil
}

func (s *dreamMemoryStub) ConfirmMemory(context.Context, string, memoryservice.ConfirmRequest) (*memoryservice.ConfirmResult, error) {
	return &memoryservice.ConfirmResult{}, nil
}

type dreamFragmentCreateStub struct {
	lastProfile string
	lastReq     *dto.CreateFragmentRequest
}

func (s *dreamFragmentCreateStub) Create(_ context.Context, profileID string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
	s.lastProfile = profileID
	s.lastReq = req
	return &fragmentservice.CreateResult{
		Fragment: &domain.Fragment{FragmentID: "dream-fragment", ProfileID: profileID, Content: req.Content},
	}, nil
}

func dreamTestNode(dreamID, profileID string, now time.Time) neo4j.Node {
	return neo4j.Node{Props: map[string]any{
		"dream_id":           dreamID,
		"team_id":            profileID,
		"hypothesis":         "A may affect B.",
		"what_if":            "What if A and B interact?",
		"possible_outcome":   "Check B before promotion.",
		"rationale":          "Paired related knowledge.",
		"likelihood":         0.4,
		"confidence":         0.5,
		"status":             string(domain.DreamStatusProposed),
		"cycle":              CycleDream,
		"cycle_run_id":       "run-1",
		"generator_model":    "model",
		"content_hash":       "hash",
		"source_refs_json":   `[{"type":"fact","id":"fact-1"}]`,
		"invalidated_reason": "",
		"created_at":         now,
		"updated_at":         now,
		"last_evaluated_at":  now,
	}}
}

type testStringer string

func (s testStringer) String() string {
	return string(s)
}
