package registry

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestEvaluationManifestAndScoringTools(t *testing.T) {
	audit := &evaluationAuditStub{}
	reg, err := BuildActive(Dependencies{EvaluationAudit: audit, EvaluationEnabled: true})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	ctx := evaluationToolContext("00000000-0000-0000-0000-000000000101")

	manifestTool, _ := reg.Get("eval_get_manifest")
	manifest, err := manifestTool.Invoke(ctx, "ignored-profile", map[string]any{})
	if err != nil {
		t.Fatalf("eval_get_manifest Invoke: %v", err)
	}
	team := manifest["team"].(map[string]any)
	if team["id"] != "00000000-0000-0000-0000-000000000101" {
		t.Fatalf("manifest team = %v", team)
	}
	if len(manifest["capabilities"].([]string)) == 0 {
		t.Fatalf("manifest capabilities empty: %v", manifest)
	}

	scoreTool, _ := reg.Get("eval_score_retrieval_case")
	score, err := scoreTool.Invoke(ctx, "ignored-profile", map[string]any{
		"k": float64(3),
		"ranked_refs": []any{
			map[string]any{"type": "relationship", "id": "rel-1"},
			map[string]any{"type": "evidence", "id": "ev-bad"},
			map[string]any{"type": "relationship", "id": "rel-2"},
		},
		"context_refs":           []map[string]any{{"type": "relationship", "id": "rel-2"}},
		"evidence_refs":          []map[string]any{{"type": "evidence", "id": "ev-1"}},
		"dream_refs":             []map[string]any{{"type": "hypothesis", "id": "hyp-1"}},
		"required_refs":          []map[string]any{{"type": "relationship", "id": "rel-1", "grade": 2.0}, {"type": "relationship", "id": "rel-2"}},
		"bad_refs":               []map[string]any{{"type": "evidence", "id": "ev-bad"}},
		"required_evidence_refs": []map[string]any{{"type": "evidence", "id": "ev-1"}},
		"required_dream_refs":    []map[string]any{{"type": "hypothesis", "id": "hyp-1"}},
	})
	if err != nil {
		t.Fatalf("eval_score_retrieval_case Invoke: %v", err)
	}
	if score["relevant_at_k"] != float64(2) || score["bad_at_k"] != float64(1) || score["context_scored"] != true || score["evidence_scored"] != true || score["dream_scored"] != true {
		t.Fatalf("score output = %v", score)
	}
	if len(audit.entries) != 2 || audit.entries[1].EntityID != "eval_score_retrieval_case" {
		t.Fatalf("audit entries = %+v", audit.entries)
	}
}

func TestEvaluationDreamTools(t *testing.T) {
	dreams := &stubDreamService{}
	reg, err := BuildActive(Dependencies{
		Dreams:            dreams,
		EvaluationAudit:   &evaluationAuditStub{},
		EvaluationEnabled: true,
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	ctx := evaluationToolContext("00000000-0000-0000-0000-000000000202")

	runTool, _ := reg.Get("eval_run_dream_cycle")
	run, err := runTool.Invoke(ctx, "profile-dream", map[string]any{
		"manual":             false,
		"reflect_enabled":    true,
		"reevaluate_enabled": false,
		"dream_enabled":      true,
		"max_outputs":        float64(7),
		"seed_dreams": []any{map[string]any{
			"hypothesis":       " Dense-Mem uses PostgreSQL ",
			"what_if":          " authority ",
			"possible_outcome": " cleaner runtime ",
			"rationale":        " architecture decision ",
			"likelihood":       0.7,
			"confidence":       0.8,
			"source_refs": []any{map[string]any{
				"type": "relationship",
				"id":   "rel-1",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("eval_run_dream_cycle Invoke: %v", err)
	}
	if run["run_id"] != "run-1" || dreams.lastRunReq.Manual || dreams.lastRunReq.MaxOutputs != 7 {
		t.Fatalf("run output/request = %v/%+v", run, dreams.lastRunReq)
	}
	if dreams.lastRunReq.ReflectEnabled == nil || !*dreams.lastRunReq.ReflectEnabled ||
		dreams.lastRunReq.ReevaluateEnabled == nil || *dreams.lastRunReq.ReevaluateEnabled ||
		dreams.lastRunReq.DreamEnabled == nil || !*dreams.lastRunReq.DreamEnabled {
		t.Fatalf("run boolean overrides = %+v", dreams.lastRunReq)
	}
	if len(dreams.lastRunReq.SeedDreams) != 1 || dreams.lastRunReq.SeedDreams[0].Hypothesis != "Dense-Mem uses PostgreSQL" {
		t.Fatalf("seed dreams = %+v", dreams.lastRunReq.SeedDreams)
	}

	listTool, _ := reg.Get("eval_list_knowledge_refs")
	page, err := listTool.Invoke(ctx, "profile-dream", map[string]any{
		"type":          "dream",
		"limit":         float64(2),
		"cursor":        "cursor-1",
		"status":        "proposed",
		"metadata_only": true,
	})
	if err != nil {
		t.Fatalf("eval_list_knowledge_refs dream Invoke: %v", err)
	}
	item := firstEvalItem(t, page)
	if _, ok := item["hypothesis"]; ok || dreams.lastListOpts.Cursor != "cursor-1" || dreams.lastListOpts.Status != "proposed" || dreams.lastListOpts.Limit != 2 {
		t.Fatalf("dream list item/opts = %v/%+v", item, dreams.lastListOpts)
	}

	getTool, _ := reg.Get("eval_get_knowledge_item")
	got, err := getTool.Invoke(ctx, "profile-dream", map[string]any{
		"type":          "dream",
		"id":            "dream-2",
		"metadata_only": true,
	})
	if err != nil {
		t.Fatalf("eval_get_knowledge_item dream Invoke: %v", err)
	}
	dream := got["item"].(map[string]any)
	if dream["dream_id"] != "dream-2" {
		t.Fatalf("dream item = %v", dream)
	}
	if refs := dreamRefs([]*domain.Dream{{DreamID: "dream-1", Status: domain.DreamStatusProposed}, nil, {}}); len(refs) != 1 || refs[0]["id"] != "dream-1" {
		t.Fatalf("dream refs = %v", refs)
	}
}

func TestEvaluationRecallFeedbackEventTools(t *testing.T) {
	teamID := uuid.MustParse("00000000-0000-0000-0000-000000000303")
	createdAt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	reader := &evalRecallFeedbackReader{
		page: &domain.RecallFeedbackEventPage{
			Items: []domain.RecallFeedbackEvent{{
				RecallID:      "rec-1",
				TeamID:        &teamID,
				CreatedAt:     createdAt,
				UpdatedAt:     createdAt,
				SnapshotState: domain.RecallFeedbackSnapshotCaptured,
			}},
			Total: 1,
		},
		event: &domain.RecallFeedbackEvent{
			RecallID:      "rec-1",
			TeamID:        &teamID,
			CreatedAt:     createdAt,
			UpdatedAt:     createdAt,
			SnapshotState: domain.RecallFeedbackSnapshotCaptured,
			ResultRefs: []domain.RecallFeedbackResultRef{{
				Type: domain.RecallFeedbackResultTypeRelationship,
				ID:   "rel-1",
				Rank: 1,
			}},
		},
	}
	audit := &evaluationAuditStub{}
	reg, err := BuildActive(Dependencies{
		RecallFeedbackEvents: reader,
		EvaluationAudit:      audit,
		EvaluationEnabled:    true,
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	ctx := evaluationToolContext(teamID.String())

	listTool, _ := reg.Get(EvalListRecallFeedbackEventsToolName)
	page, err := listTool.Invoke(ctx, "ignored-profile", map[string]any{
		"limit":           float64(4),
		"offset":          float64(2),
		"quality":         "low",
		"include_pending": true,
		"missing_context": true,
		"irrelevant":      false,
		"from":            "2026-07-22T00:00:00Z",
		"to":              "2026-07-23T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("eval_list_recall_feedback_events Invoke: %v", err)
	}
	if page["total"] != float64(1) || reader.lastFilter.Limit != 4 || reader.lastFilter.Offset != 2 || reader.lastFilter.Quality != "low" ||
		reader.lastFilter.TeamID == nil || *reader.lastFilter.TeamID != teamID || reader.lastFilter.MissingContext == nil || !*reader.lastFilter.MissingContext ||
		reader.lastFilter.Irrelevant == nil || *reader.lastFilter.Irrelevant || reader.lastFilter.From == nil || reader.lastFilter.To == nil {
		t.Fatalf("page/filter = %v/%+v", page, reader.lastFilter)
	}

	getTool, _ := reg.Get(EvalGetRecallFeedbackEventToolName)
	got, err := getTool.Invoke(ctx, "ignored-profile", map[string]any{"recall_id": "rec-1"})
	if err != nil {
		t.Fatalf("eval_get_recall_feedback_event Invoke: %v", err)
	}
	event := got["event"].(map[string]any)
	if event["recall_id"] != "rec-1" || len(audit.entries) != 2 {
		t.Fatalf("event/audit = %v/%+v", event, audit.entries)
	}

	if _, err := evalRecallFeedbackFilter(context.Background(), "not-a-uuid", map[string]any{"from": "bad-time"}, 1); err == nil {
		t.Fatal("evalRecallFeedbackFilter accepted invalid fallback")
	}
	if evalEventInScope(ctx, "ignored-profile", nil) {
		t.Fatal("nil recall feedback event is in scope")
	}
}

func evaluationToolContext(teamID string) context.Context {
	ctx := contractInvokeContext("read", "write", "feedback:read")
	return requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
		TeamID:   uuid.MustParse(teamID),
		TeamName: "Eval Team",
	})
}

type evalRecallFeedbackReader struct {
	lastFilter domain.RecallFeedbackEventFilter
	page       *domain.RecallFeedbackEventPage
	event      *domain.RecallFeedbackEvent
}

func (s *evalRecallFeedbackReader) RecordRecallSnapshot(context.Context, domain.RecallFeedbackEvent) error {
	return nil
}

func (s *evalRecallFeedbackReader) RecordRecallFeedback(context.Context, domain.RecallFeedbackSubmission) error {
	return nil
}

func (s *evalRecallFeedbackReader) ListRecallFeedbackEvents(_ context.Context, filter domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error) {
	s.lastFilter = filter
	return s.page, nil
}

func (s *evalRecallFeedbackReader) GetRecallFeedbackEvent(context.Context, string) (*domain.RecallFeedbackEvent, error) {
	return s.event, nil
}
