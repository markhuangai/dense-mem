package registry

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

func TestBuildDefault_MemoryToolDescriptionsIncludeUsageTriggers(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})
	cases := []struct {
		name string
		want []string
	}{
		{
			name: "recall_memory",
			want: []string{"Use before answering", "prior user preferences", "project decisions", "active goals"},
		},
		{
			name: "remember",
			want: []string{"Use after the user states", "durable preference", "correction", "project decision"},
		},
		{
			name: "import_memories",
			want: []string{"historical conversations", "not normal live chat turns"},
		},
		{
			name: "reflect_memories",
			want: []string{"memory health", "clarifications"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := reg.Get(tc.name)
			if !ok {
				t.Fatalf("%s not registered", tc.name)
			}
			for _, phrase := range tc.want {
				if !strings.Contains(tool.Description, phrase) {
					t.Fatalf("%s description missing %q: %s", tc.name, phrase, tool.Description)
				}
			}
		})
	}
}

func TestBuildDefault_RecallInvokerMapsTieredHits(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{Recall: stubRecallWithTieredHits{}})
	tool, _ := reg.Get("recall_memory")

	out, err := tool.Invoke(context.Background(), "profile-tiered", map[string]any{"query": "preferences"})
	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	results := out["results"].([]map[string]any)
	if len(results) != 3 {
		t.Fatalf("results length = %d, want 3: %v", len(results), results)
	}

	fact := results[0]
	if fact["tier"] != recallservice.TierActiveFact {
		t.Fatalf("fact tier = %v, want %s", fact["tier"], recallservice.TierActiveFact)
	}
	factPayload, ok := fact["fact"].(map[string]any)
	if !ok || factPayload["fact_id"] != "fact-hit" || factPayload["subject"] != "Dense-Mem project" {
		t.Fatalf("fact payload = %v, want fact-hit Dense-Mem project", fact["fact"])
	}

	claim := results[1]
	if claim["tier"] != recallservice.TierValidatedClaim {
		t.Fatalf("claim tier = %v, want %s", claim["tier"], recallservice.TierValidatedClaim)
	}
	claimPayload, ok := claim["claim"].(map[string]any)
	if !ok || claimPayload["claim_id"] != "claim-hit" || claimPayload["predicate"] != "prefers" {
		t.Fatalf("claim payload = %v, want claim-hit prefers", claim["claim"])
	}

	fragment := results[2]
	if fragment["tier"] != recallservice.TierFragment || fragment["id"] != "fragment-hit" {
		t.Fatalf("fragment result = %v, want tier-2 flat fragment-hit", fragment)
	}
	if fragment["score"] != 0.25 {
		t.Fatalf("fragment score = %v, want 0.25", fragment["score"])
	}
}

func TestRecallHitToMapInfersTierAndScore(t *testing.T) {
	fragmentHit, err := recallHitToMap(recallservice.RecallHit{
		Fragment:   &domain.Fragment{FragmentID: "fragment-inferred", ProfileID: "profile-1"},
		FinalScore: 0.33,
	})
	if err != nil {
		t.Fatalf("fragment recallHitToMap: %v", err)
	}
	if fragmentHit["tier"] != recallservice.TierFragment || fragmentHit["score"] != 0.33 {
		t.Fatalf("fragment hit = %v, want inferred tier and final score", fragmentHit)
	}

	claimHit, err := recallHitToMap(recallservice.RecallHit{
		Claim: &domain.Claim{
			ClaimID:           "claim-inferred",
			ProfileID:         "profile-1",
			EntailmentVerdict: domain.VerdictEntailed,
			Status:            domain.StatusValidated,
		},
	})
	if err != nil {
		t.Fatalf("claim recallHitToMap: %v", err)
	}
	if claimHit["tier"] != recallservice.TierValidatedClaim {
		t.Fatalf("claim hit tier = %v, want %s", claimHit["tier"], recallservice.TierValidatedClaim)
	}

	factHit, err := recallHitToMap(recallservice.RecallHit{
		Fact: &domain.Fact{
			FactID:    "fact-inferred",
			ProfileID: "profile-1",
			Status:    domain.FactStatusActive,
		},
	})
	if err != nil {
		t.Fatalf("fact recallHitToMap: %v", err)
	}
	if factHit["tier"] != recallservice.TierActiveFact {
		t.Fatalf("fact hit tier = %v, want %s", factHit["tier"], recallservice.TierActiveFact)
	}
}

func TestRecallHitToMapReturnsSerializationError(t *testing.T) {
	_, err := recallHitToMap(recallservice.RecallHit{
		Fact: &domain.Fact{
			FactID:    "bad-fact",
			ProfileID: "profile-1",
			Metadata:  map[string]any{"bad": func() {}},
		},
	})
	if err == nil {
		t.Fatal("recallHitToMap error = nil, want serialization error")
	}
}

type stubRecallWithTieredHits struct{}

func (stubRecallWithTieredHits) Recall(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	now := time.Date(2026, 6, 2, 11, 30, 0, 0, time.UTC)
	return []recallservice.RecallHit{
		{
			Fact: &domain.Fact{
				FactID:              "fact-hit",
				ProfileID:           profileID,
				Subject:             "Dense-Mem project",
				Predicate:           "has_goal",
				Object:              "active MCP recall",
				Status:              domain.FactStatusActive,
				TruthScore:          0.91,
				RecordedAt:          now,
				PromotedFromClaimID: "claim-source",
			},
			Tier:  recallservice.TierActiveFact,
			Score: 0.91,
		},
		{
			Claim: &domain.Claim{
				ClaimID:           "claim-hit",
				ProfileID:         profileID,
				Subject:           "user",
				Predicate:         "prefers",
				Object:            "active memory usage",
				Modality:          domain.ModalityAssertion,
				Polarity:          domain.PolarityPlus,
				RecordedAt:        now,
				ExtractConf:       0.82,
				ResolutionConf:    0.9,
				EntailmentVerdict: domain.VerdictEntailed,
				Status:            domain.StatusValidated,
			},
			Tier:  recallservice.TierValidatedClaim,
			Score: 0.41,
		},
		{
			Fragment: &domain.Fragment{
				FragmentID: "fragment-hit",
				ProfileID:  profileID,
				Content:    req.Query,
			},
			Tier:         recallservice.TierFragment,
			Score:        0.25,
			SemanticRank: 3,
			KeywordRank:  4,
			FinalScore:   0.25,
		},
	}, nil
}
