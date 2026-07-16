package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRecallMemoryIgnoresDreamRecallFailure(t *testing.T) {
	dreams := &stubDreamService{recallErr: errors.New("dream recall failed")}
	reg, _ := BuildDefault(Dependencies{
		Recall: stubRecallWithHit{},
		Dreams: dreams,
	})
	tool, _ := reg.Get("recall_memory")

	out, err := tool.Invoke(context.Background(), "profile-search", map[string]any{"query": "hello"})

	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	results := out["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["evidence_id"] != "fragment-hit" {
		t.Fatalf("recall_memory results = %v, want base recall hit", results)
	}
	if dreams.recallQuery != "hello" {
		t.Fatalf("dream recall query = %q, want hello", dreams.recallQuery)
	}
	related := out["related_hypotheses"].([]any)
	if len(related) != 0 {
		t.Fatalf("related_hypotheses = %v, want empty on dream recall error", related)
	}
}

func TestRecallMemoryUsesPostgresHypothesisReaderWhenWired(t *testing.T) {
	dreams := &stubDreamService{recallErr: errors.New("legacy dream recall should not be called")}
	hypotheses := &stubHypothesisRecall{}
	reg, _ := BuildDefault(Dependencies{
		Recall:     stubRecallWithHit{},
		Dreams:     dreams,
		Hypotheses: hypotheses,
	})
	tool, _ := reg.Get("recall_memory")

	out, err := tool.Invoke(context.Background(), "profile-search", map[string]any{"query": "hello"})

	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	if dreams.recallQuery != "" {
		t.Fatalf("legacy dream recall query = %q, want unused", dreams.recallQuery)
	}
	if hypotheses.profileID != "profile-search" || hypotheses.query != "hello" {
		t.Fatalf("hypothesis recall = %q/%q", hypotheses.profileID, hypotheses.query)
	}
	related := out["related_hypotheses"].([]any)
	if len(related) != 1 || related[0].(map[string]any)["dream_id"] != "hypothesis-1" {
		t.Fatalf("related_hypotheses = %v, want hypothesis-1", related)
	}
}

type stubHypothesisRecall struct {
	profileID string
	query     string
}

func (s *stubHypothesisRecall) RecallHypotheses(ctx context.Context, profileID, query string, limit int) ([]*domain.Dream, error) {
	s.profileID = profileID
	s.query = query
	return []*domain.Dream{{
		DreamID:    "hypothesis-1",
		ProfileID:  profileID,
		Hypothesis: "PG hypothesis",
		Status:     domain.DreamStatusProposed,
	}}, nil
}
