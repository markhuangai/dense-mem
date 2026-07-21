package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestEvalV2KnowledgeToolsUseTeamScopeAndStripPayloads(t *testing.T) {
	audit := &evaluationAuditStub{}
	v2Evaluation := &evalV2EvaluationStore{
		listPage: &repository.V2EvaluationPage{
			Items: []map[string]any{{
				"type":             "evidence",
				"id":               "00000000-0000-0000-0000-00000000e001",
				"status":           "active",
				"owner_profile_id": "00000000-0000-0000-0000-000000000606",
				"content":          "evidence content must be stripped",
			}},
			NextCursor: "v2-next",
			HasMore:    true,
		},
		item: map[string]any{
			"type":          "relationship",
			"id":            "00000000-0000-0000-0000-00000000e002",
			"status":        "active",
			"predicate_key": "works_at",
		},
	}
	reg, err := BuildDefault(Dependencies{
		EvaluationAudit:     audit,
		V2Evaluation:        v2Evaluation,
		V2EvaluationEnabled: true,
	})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		TeamID: uuid.MustParse("00000000-0000-0000-0000-000000000101"),
	})

	listTool, _ := reg.Get("eval_list_knowledge_refs")
	if err := ValidateInput(listTool, map[string]any{"type": "evidence"}); err != nil {
		t.Fatalf("ValidateInput V2 evidence: %v", err)
	}
	evidencePage, err := listTool.Invoke(ctx, "profile-eval", map[string]any{
		"type":          "evidence",
		"status":        "active",
		"cursor":        "v2-cursor",
		"limit":         6,
		"metadata_only": true,
	})
	if err != nil {
		t.Fatalf("eval_list_knowledge_refs evidence Invoke: %v", err)
	}
	if evidencePage["next_cursor"] != "v2-next" || evidencePage["has_more"] != true {
		t.Fatalf("V2 evidence page = %v", evidencePage)
	}
	evidence := firstEvalItem(t, evidencePage)
	if _, ok := evidence["content"]; ok {
		t.Fatalf("metadata-only evidence returned content: %v", evidence)
	}
	if v2Evaluation.lastList.TeamID != "00000000-0000-0000-0000-000000000101" ||
		v2Evaluation.lastList.Type != "evidence" ||
		v2Evaluation.lastList.Status != "active" ||
		v2Evaluation.lastList.Cursor != "v2-cursor" ||
		v2Evaluation.lastList.Limit != 6 {
		t.Fatalf("V2 list input = %+v", v2Evaluation.lastList)
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
	if relationship["predicate_key"] != "works_at" || v2Evaluation.lastGet.TeamID != "00000000-0000-0000-0000-000000000101" {
		t.Fatalf("relationship item/input = %v/%+v", relationship, v2Evaluation.lastGet)
	}
	v2Evaluation.item = map[string]any{
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

func TestEvalV2KnowledgeToolsRequireExplicitGate(t *testing.T) {
	v2Evaluation := &evalV2EvaluationStore{}
	reg, err := BuildDefault(Dependencies{
		EvaluationAudit: &evaluationAuditStub{},
		V2Evaluation:    v2Evaluation,
	})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}

	listTool, _ := reg.Get("eval_list_knowledge_refs")
	if err := ValidateInput(listTool, map[string]any{"type": "evidence"}); err == nil {
		t.Fatal("eval_list_knowledge_refs accepted V2 type without explicit gate")
	}
	if _, err := listTool.Invoke(context.Background(), "profile-eval", map[string]any{"type": "evidence"}); !errors.Is(err, ErrToolDisabled) {
		t.Fatalf("eval_list_knowledge_refs err = %v; want ErrToolDisabled", err)
	}

	getTool, _ := reg.Get("eval_get_knowledge_item")
	if err := ValidateInput(getTool, map[string]any{"type": "relationship", "id": "rel-1"}); err == nil {
		t.Fatal("eval_get_knowledge_item accepted V2 type without explicit gate")
	}
	if _, err := getTool.Invoke(context.Background(), "profile-eval", map[string]any{"type": "relationship", "id": "rel-1"}); !errors.Is(err, ErrToolDisabled) {
		t.Fatalf("eval_get_knowledge_item err = %v; want ErrToolDisabled", err)
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

type evalV2EvaluationStore struct {
	lastList repository.V2EvaluationListInput
	lastGet  repository.V2EvaluationGetInput
	listPage *repository.V2EvaluationPage
	item     map[string]any
}

func (s *evalV2EvaluationStore) ListV2EvaluationRefs(_ context.Context, input repository.V2EvaluationListInput) (*repository.V2EvaluationPage, error) {
	s.lastList = input
	if s.listPage != nil {
		return s.listPage, nil
	}
	return &repository.V2EvaluationPage{}, nil
}

func (s *evalV2EvaluationStore) GetV2EvaluationItem(_ context.Context, input repository.V2EvaluationGetInput) (map[string]any, error) {
	s.lastGet = input
	if s.item != nil {
		return s.item, nil
	}
	return nil, errors.New("v2 evaluation item not found")
}
