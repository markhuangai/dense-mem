//go:build evaluation

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
	}
	reg, err := BuildActive(Dependencies{
		EvaluationAudit: audit,
		Evaluation:      evaluation,
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
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

	if len(audit.entries) != 1 {
		t.Fatalf("audit entries = %d; want 1", len(audit.entries))
	}
}

func TestEvalKnowledgeToolRequiresRepository(t *testing.T) {
	reg, err := BuildActive(Dependencies{
		EvaluationAudit: &evaluationAuditStub{},
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	tool, ok := reg.Get("eval_list_knowledge_refs")
	if !ok {
		t.Fatal("evaluation build omitted eval_list_knowledge_refs")
	}
	_, err = tool.Invoke(context.Background(), "00000000-0000-0000-0000-000000000101", map[string]any{"type": "evidence"})
	if !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("Invoke error = %v; want ErrToolUnavailable", err)
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
