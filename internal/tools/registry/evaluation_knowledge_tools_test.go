package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestEvalKnowledgeToolsUseTeamScopeAndStripPayloads(t *testing.T) {
	audit := &evaluationAuditStub{}
	evaluation := &evalEvaluationStore{
		listPage: &repository.EvaluationPage{
			Items: []map[string]any{{
				"type":             "evidence",
				"id":               "00000000-0000-0000-0000-00000000e001",
				"status":           "active",
				"owner_profile_id": "00000000-0000-0000-0000-000000000606",
				"content":          "evidence content must be stripped",
			}},
			NextCursor: "next-page",
			HasMore:    true,
		},
		item: map[string]any{
			"type":          "relationship",
			"id":            "00000000-0000-0000-0000-00000000e002",
			"status":        "active",
			"predicate_key": "works_at",
		},
	}
	reg, err := BuildActive(Dependencies{
		EvaluationAudit:   audit,
		Evaluation:        evaluation,
		EvaluationEnabled: true,
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		TeamID: uuid.MustParse("00000000-0000-0000-0000-000000000101"),
	})

	listTool, _ := reg.Get("eval_list_knowledge_refs")
	if err := ValidateInput(listTool, map[string]any{"type": "evidence"}); err != nil {
		t.Fatalf("ValidateInput evidence: %v", err)
	}
	evidencePage, err := listTool.Invoke(ctx, "profile-eval", map[string]any{
		"type":          "evidence",
		"status":        "active",
		"cursor":        "cursor-page",
		"limit":         6,
		"metadata_only": true,
	})
	if err != nil {
		t.Fatalf("eval_list_knowledge_refs evidence Invoke: %v", err)
	}
	if evidencePage["next_cursor"] != "next-page" || evidencePage["has_more"] != true {
		t.Fatalf("evidence page = %v", evidencePage)
	}
	evidence := firstEvalItem(t, evidencePage)
	if _, ok := evidence["content"]; ok {
		t.Fatalf("metadata-only evidence returned content: %v", evidence)
	}
	if evaluation.lastList.TeamID != "00000000-0000-0000-0000-000000000101" ||
		evaluation.lastList.Type != "evidence" ||
		evaluation.lastList.Status != "active" ||
		evaluation.lastList.Cursor != "cursor-page" ||
		evaluation.lastList.Limit != 6 {
		t.Fatalf("list input = %+v", evaluation.lastList)
	}

	getTool, _ := reg.Get("eval_get_knowledge_item")
	itemOut, err := getTool.Invoke(ctx, "profile-eval", map[string]any{
		"type": "relationship",
		"id":   "00000000-0000-0000-0000-00000000e002",
	})
	if err != nil {
		t.Fatalf("eval_get_knowledge_item relationship Invoke: %v", err)
	}
	relationship := itemOut["item"].(map[string]any)
	if relationship["predicate_key"] != "works_at" || evaluation.lastGet.TeamID != "00000000-0000-0000-0000-000000000101" {
		t.Fatalf("relationship item/input = %v/%+v", relationship, evaluation.lastGet)
	}
	evaluation.item = map[string]any{
		"type":    "hypothesis",
		"id":      "00000000-0000-0000-0000-00000000e003",
		"status":  "candidate",
		"payload": map[string]any{"hypothesis": "must be stripped"},
	}
	itemOut, err = getTool.Invoke(ctx, "profile-eval", map[string]any{
		"type":          "hypothesis",
		"id":            "00000000-0000-0000-0000-00000000e003",
		"metadata_only": true,
	})
	if err != nil {
		t.Fatalf("eval_get_knowledge_item hypothesis Invoke: %v", err)
	}
	hypothesis := itemOut["item"].(map[string]any)
	if _, ok := hypothesis["payload"]; ok {
		t.Fatalf("metadata-only hypothesis returned payload: %v", hypothesis)
	}
	if len(audit.entries) != 3 {
		t.Fatalf("audit entries = %d; want 3", len(audit.entries))
	}
}

func TestEvalKnowledgeToolsRequireExplicitGate(t *testing.T) {
	evaluation := &evalEvaluationStore{}
	reg, err := BuildActive(Dependencies{
		EvaluationAudit: &evaluationAuditStub{},
		Evaluation:      evaluation,
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	for _, name := range []string{"eval_list_knowledge_refs", "eval_get_knowledge_item"} {
		if _, ok := reg.Get(name); ok {
			t.Fatalf("%s registered without explicit evaluation gate", name)
		}
	}
}

func TestEvalActorTeamIDFallbackAndError(t *testing.T) {
	teamID := "00000000-0000-0000-0000-000000000707"
	got, err := evalActorTeamID(context.Background(), teamID)
	if err != nil {
		t.Fatalf("evalActorTeamID fallback: %v", err)
	}
	if got != teamID {
		t.Fatalf("fallback team id = %q; want %q", got, teamID)
	}
	if _, err := evalActorTeamID(context.Background(), "not-a-uuid"); err == nil {
		t.Fatal("evalActorTeamID accepted missing actor and invalid fallback")
	}
}

type evalEvaluationStore struct {
	lastList repository.EvaluationListInput
	lastGet  repository.EvaluationGetInput
	listPage *repository.EvaluationPage
	item     map[string]any
}

func (s *evalEvaluationStore) ListEvaluationRefs(_ context.Context, input repository.EvaluationListInput) (*repository.EvaluationPage, error) {
	s.lastList = input
	if s.listPage != nil {
		return s.listPage, nil
	}
	return &repository.EvaluationPage{}, nil
}

func (s *evalEvaluationStore) GetEvaluationItem(_ context.Context, input repository.EvaluationGetInput) (map[string]any, error) {
	s.lastGet = input
	if s.item != nil {
		return s.item, nil
	}
	return nil, errors.New("evaluation item not found")
}
