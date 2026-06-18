package skillpackservice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
)

func TestOpenAIConflictDecider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		var req openAIConflictDecisionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "gpt-test" || req.ResponseFormat.Type != "json_schema" {
			t.Fatalf("request = %+v, want model and json schema response format", req)
		}
		if req.Temperature == nil || *req.Temperature != 0 {
			t.Fatalf("temperature = %v, want 0", req.Temperature)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"skip\",\"confidence\":0.91,\"rationale\":\"stale superseded fact\"}"}}]}`))
	}))
	defer srv.Close()

	decider := NewOpenAIConflictDecider(&config.Config{
		AIVerifierAPIURL: srv.URL,
		AIVerifierAPIKey: "sk-test",
		AIVerifierModel:  "gpt-test",
	}, srv.Client())
	res, err := decider.Decide(context.Background(), ConflictDecisionRequest{
		ProfileID:    "team-1",
		ArtifactHash: "hash",
		Mode:         ModeTrusted,
		Item:         SkillPackItem{Subject: "assistant", Predicate: "has_skill", Object: "old", SourceKind: SourceKindFact},
		Prompt: ConflictPrompt{
			Index:          0,
			AllowedActions: []string{DecisionSkip, DecisionDemoteToClaim},
		},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Action != DecisionSkip || res.Confidence != 0.91 || res.Model != "gpt-test" || res.RawJSON == "" {
		t.Fatalf("result = %+v, want parsed skip recommendation", res)
	}
}

func TestOpenAIConflictDecider_DisableTemperatureOmitsField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := raw["temperature"]; ok {
			t.Fatal("temperature field was present, want omitted")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"skip\",\"confidence\":0.91,\"rationale\":\"stale superseded fact\"}"}}]}`))
	}))
	defer srv.Close()

	decider := NewOpenAIConflictDecider(&config.Config{
		AIVerifierAPIURL:             srv.URL,
		AIVerifierAPIKey:             "sk-test",
		AIVerifierModel:              "gpt-test",
		AIVerifierDisableTemperature: true,
	}, srv.Client())

	_, err := decider.Decide(context.Background(), ConflictDecisionRequest{
		ProfileID:    "team-1",
		ArtifactHash: "hash",
		Mode:         ModeTrusted,
		Item:         SkillPackItem{Subject: "assistant", Predicate: "has_skill", Object: "old", SourceKind: SourceKindFact},
		Prompt: ConflictPrompt{
			Index:          0,
			AllowedActions: []string{DecisionSkip, DecisionDemoteToClaim},
		},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
}
