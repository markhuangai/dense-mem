package registry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

func TestBuildDefault_MemoryToolDescriptionsIncludeRecallAffordances(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})
	cases := []struct {
		name string
		want []string
	}{
		{
			name: "recall_memory",
			want: []string{"prior user preferences", "project decisions", "active goals", "recall_id", "related_hypotheses"},
		},
		{
			name: "remember",
			want: []string{"durable memory evidence", "project decision", "Dense-Mem decides"},
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

func TestRecallMemorySchemaSupportsBoundedFollowUpContext(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})
	tool, _ := reg.Get("recall_memory")
	properties := tool.InputSchema["properties"].(map[string]any)
	known, ok := properties["known_relationship_ids"].(map[string]any)
	if !ok {
		t.Fatal("recall_memory schema missing known_relationship_ids")
	}
	if known["maxItems"] != recallservice.MaxKnownRelationshipIDs || known["uniqueItems"] != true {
		t.Fatalf("known_relationship_ids schema = %#v", known)
	}
	knownEvidence, ok := properties["known_evidence_ids"].(map[string]any)
	if !ok {
		t.Fatal("recall_memory schema missing known_evidence_ids")
	}
	if knownEvidence["maxItems"] != recallservice.MaxKnownEvidenceIDs || knownEvidence["uniqueItems"] != true {
		t.Fatalf("known_evidence_ids schema = %#v", knownEvidence)
	}
	expand, ok := properties["expand_from_entity_ids"].(map[string]any)
	if !ok {
		t.Fatal("recall_memory schema missing expand_from_entity_ids")
	}
	if expand["maxItems"] != recallservice.MaxExpandFromEntityIDs || expand["uniqueItems"] != true {
		t.Fatalf("expand_from_entity_ids schema = %#v", expand)
	}
	for _, removed := range []string{"mode", "include_evidence", "use_communities", "exclude_relationship_ids", "include_hypotheses"} {
		if _, ok := properties[removed]; ok {
			t.Fatalf("recall_memory schema retained removed input %q", removed)
		}
	}
}

func TestBuildDefault_RecallInvokerRendersCompactContext(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{Recall: stubRecallWithTieredHits{}})
	tool, _ := reg.Get("recall_memory")

	out, err := tool.Invoke(context.Background(), "profile-tiered", map[string]any{"query": "preferences"})
	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	results := out["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("results length = %d, want 3: %v", len(results), results)
	}
	fact := results[0].(map[string]any)
	if fact["evidence_id"] != "fact-hit" || !strings.Contains(fact["context"].(string), "active MCP recall") {
		t.Fatalf("fact result = %v", fact)
	}
	if _, ok := fact["score"]; ok {
		t.Fatalf("fact result leaked score: %v", fact)
	}
	if _, ok := fact["trace_available"]; ok {
		t.Fatalf("fact result leaked trace_available: %v", fact)
	}
	claim := results[1].(map[string]any)
	if claim["evidence_id"] != "claim-hit" || !strings.Contains(claim["context"].(string), "active memory usage") {
		t.Fatalf("claim result = %v", claim)
	}
	if _, ok := claim["last_verifier_response"]; ok {
		t.Fatalf("claim result leaked verifier response: %v", claim)
	}
	fragment := results[2].(map[string]any)
	if fragment["evidence_id"] != "fragment-hit" || fragment["context"] != "preferences" {
		t.Fatalf("fragment result = %v", fragment)
	}
	if out["recall_id"] == "" || out["discovery_guidance"] != recallservice.DiscoveryGuidance {
		t.Fatalf("recall_id/guidance = %v/%v", out["recall_id"], out["discovery_guidance"])
	}
	if paths, ok := out["discovery_paths"].([]any); !ok || len(paths) != 0 {
		t.Fatalf("discovery_paths = %v, want empty", out["discovery_paths"])
	}
	for _, removed := range []string{"mode", "evidences", "connections", "fragments", "frontier_hints", "related_dreams", "clarifications", "next_page_cursor"} {
		if _, ok := out[removed]; ok {
			t.Fatalf("compact response retained removed field %q: %v", removed, out)
		}
	}
}

func TestRecallMemoryOutputSchemaIsCompactAndMetadataFree(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})
	tool, _ := reg.Get("recall_memory")
	raw, err := json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatalf("json.Marshal(OutputSchema) error = %v", err)
	}
	for _, required := range []string{"recall_id", "results", "discovery_paths", "discovery_guidance", "related_hypotheses"} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("output schema missing %q: %s", required, raw)
		}
	}
	for _, forbidden := range []string{"evidences", "connections", "clarifications", "fragments", "frontier_hints", "related_dreams", "contract_version", "embedding", "semantic_rank", "keyword_rank", "final_score"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("output schema contains %q: %s", forbidden, raw)
		}
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
				ClaimID:              "claim-hit",
				ProfileID:            profileID,
				Subject:              "user",
				Predicate:            "prefers",
				Object:               "active memory usage",
				Modality:             domain.ModalityAssertion,
				Polarity:             domain.PolarityPlus,
				RecordedAt:           now,
				ExtractConf:          0.82,
				ResolutionConf:       0.9,
				EntailmentVerdict:    domain.VerdictEntailed,
				LastVerifierResponse: "internal verifier trace",
				Status:               domain.StatusValidated,
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
