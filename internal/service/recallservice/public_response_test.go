package recallservice

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRenderPublicRecallReturnsEvidenceResultsAndDiscoveryPaths(t *testing.T) {
	hits := []RecallHit{{
		Evidence: &domain.SemanticEvidenceFragment{
			TeamID:         "team-internal",
			FragmentID:     "evidence-1",
			OwnerProfileID: "profile-1",
			Content:        "Dense-Mem uses PostgreSQL.",
			ContentHash:    "internal-hash",
		},
		Relationships: []domain.SemanticRelationship{{
			TeamID:             "team-internal",
			RelationshipID:     "rel-1",
			OwnerProfileID:     "profile-1",
			OwnerProfileName:   "Mark",
			SubjectEntityID:    "entity-1",
			SubjectEntityName:  "Dense-Mem",
			SubjectEntityKind:  domain.SemanticEntityProject,
			Predicate:          "uses",
			ObjectEntityID:     "entity-2",
			ObjectEntityName:   "PostgreSQL",
			ObjectEntityKind:   domain.SemanticEntityProduct,
			Tier:               domain.SemanticTierFact,
			Status:             domain.SemanticStatusActive,
			Confidence:         0.99,
			SupportCount:       3,
			SourceGroupCount:   2,
			SemanticGroupKey:   "internal-group",
			PrimarySourceGroup: "internal-source-group",
			Version:            4,
		}},
		Supports: []domain.SemanticRelationshipSupport{{
			RelationshipID: "rel-1",
			FragmentID:     "evidence-1",
			Quote:          "internal evidence",
		}},
		Score: 0.97,
	}}

	got, err := RenderPublicRecall(RecallRequest{Query: "database"}, hits)
	if err != nil {
		t.Fatalf("RenderPublicRecall() error = %v", err)
	}
	if got.DiscoveryGuidance != DiscoveryGuidance {
		t.Fatalf("response guidance = %q", got.DiscoveryGuidance)
	}
	if len(got.Results) != 1 || got.Results[0].EvidenceID != "evidence-1" || got.Results[0].Context == "" {
		t.Fatalf("results = %#v", got.Results)
	}
	if len(got.DiscoveryPaths) != 1 || got.DiscoveryPaths[0].Relationships[0].RelationshipID != "rel-1" {
		t.Fatalf("discovery paths = %#v", got.DiscoveryPaths)
	}
	if got.DiscoveryPaths[0].Relationships[0].Subject.Name != "Dense-Mem" || got.DiscoveryPaths[0].Relationships[0].Object.Name != "PostgreSQL" {
		t.Fatalf("path relationship = %#v", got.DiscoveryPaths[0].Relationships[0])
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, forbidden := range []string{
		"contract_version", "embedding_model", "embedding_contract", "team_id",
		"confidence", "semantic_rank", "keyword_rank", "final_score", "score",
		"semantic_group_key", "primary_source_group", "support_count", "source_group_count",
		"trace_available", "\"version\"", "internal-hash",
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("public response leaked %q: %s", forbidden, raw)
		}
	}
}

func TestRenderPublicRecallAppliesEvidenceBytePrefix(t *testing.T) {
	large := strings.Repeat("a", MaxRecallEvidenceBytes)
	got, err := RenderPublicRecall(RecallRequest{Query: "source"}, []RecallHit{
		{Evidence: &domain.SemanticEvidenceFragment{FragmentID: "large", Content: large}},
		{Evidence: &domain.SemanticEvidenceFragment{FragmentID: "later", Content: "must not skip past large"}},
	})
	if err != nil {
		t.Fatalf("RenderPublicRecall() error = %v", err)
	}
	if len(got.Results) != 1 || got.Results[0].EvidenceID != "large" {
		t.Fatalf("results = %#v", got.Results)
	}
}

func TestRenderPublicRecallBoundsAndDeduplicatesDiscoveryPaths(t *testing.T) {
	hits := make([]RecallHit, 0, 10)
	for hitIndex := 0; hitIndex < 10; hitIndex++ {
		evidenceID := "evidence-" + strings.Repeat("x", hitIndex+1)
		hits = append(hits, RecallHit{
			Evidence: &domain.SemanticEvidenceFragment{
				FragmentID: evidenceID,
				Content:    "context",
			},
			Relationships: []domain.SemanticRelationship{{
				RelationshipID:    "rel-" + strings.Repeat("r", hitIndex+1),
				SubjectEntityName: "subject",
				Predicate:         "related_to",
				ObjectValue:       "object",
				ObjectKind:        "text",
			}},
			Supports: []domain.SemanticRelationshipSupport{{RelationshipID: "rel", FragmentID: evidenceID}},
		})
	}
	got, err := RenderPublicRecall(RecallRequest{Query: "q"}, hits)
	if err != nil {
		t.Fatalf("RenderPublicRecall() error = %v", err)
	}
	if len(got.DiscoveryPaths) != MaxDiscoveryPaths {
		t.Fatalf("path count = %d, want %d", len(got.DiscoveryPaths), MaxDiscoveryPaths)
	}
	for _, path := range got.DiscoveryPaths {
		if len(path.Relationships) > MaxDiscoveryPathRelationships {
			t.Fatalf("path relationships = %#v", path.Relationships)
		}
	}
}

func TestRenderPublicRecallAllowsDiscoveryPathToUnseenEvidence(t *testing.T) {
	got, err := RenderPublicRecall(RecallRequest{Query: "q"}, []RecallHit{
		{Evidence: &domain.SemanticEvidenceFragment{FragmentID: "evidence-1", Content: "Initial answer context."}},
		{
			Relationships: []domain.SemanticRelationship{{
				RelationshipID:    "rel-2",
				SubjectEntityName: "Dense-Mem",
				Predicate:         "documents",
				ObjectEntityName:  "ADR",
			}},
			Supports: []domain.SemanticRelationshipSupport{{
				RelationshipID: "rel-2",
				FragmentID:     "evidence-2",
			}},
		},
	})
	if err != nil {
		t.Fatalf("RenderPublicRecall() error = %v", err)
	}
	if len(got.Results) != 1 || got.Results[0].EvidenceID != "evidence-1" {
		t.Fatalf("results = %#v, want only initial evidence", got.Results)
	}
	if len(got.DiscoveryPaths) != 1 {
		t.Fatalf("discovery paths = %#v, want one unseen path", got.DiscoveryPaths)
	}
	if got.DiscoveryPaths[0].EvidenceIDs[0] != "evidence-2" {
		t.Fatalf("path evidence ids = %#v, want unseen evidence-2", got.DiscoveryPaths[0].EvidenceIDs)
	}
}

func TestRenderPublicRecallRejectsMissingPayload(t *testing.T) {
	if _, err := RenderPublicRecall(RecallRequest{Query: "q"}, []RecallHit{{}}); err == nil || !strings.Contains(err.Error(), "missing payload") {
		t.Fatalf("RenderPublicRecall() error = %v", err)
	}
}

func TestRenderPublicRecallMapsLegacyHitsToEvidenceResults(t *testing.T) {
	response, err := RenderPublicRecall(RecallRequest{Query: "q"}, []RecallHit{
		{Fact: &domain.Fact{
			FactID:    "fact-1",
			ProfileID: "profile-1",
			Subject:   "Dense-Mem",
			Predicate: "uses",
			Object:    "PostgreSQL",
		}},
		{Claim: &domain.Claim{
			ClaimID:   "claim-1",
			ProfileID: "profile-1",
			Subject:   "PostgreSQL",
			Predicate: "supports",
			Object:    "pgvector",
			Polarity:  domain.PolarityPlus,
		}},
		{Fragment: &domain.Fragment{
			FragmentID: "fragment-1",
			ProfileID:  "profile-1",
			Content:    "Dense-Mem stores semantic relationships in PostgreSQL.",
			SourceType: domain.SourceTypeDocument,
		}},
	})
	if err != nil {
		t.Fatalf("RenderPublicRecall() error = %v", err)
	}
	if len(response.Results) != 3 {
		t.Fatalf("results = %#v", response.Results)
	}
	if response.Results[0].EvidenceID != "fact-1" || !strings.Contains(response.Results[0].Context, "PostgreSQL") {
		t.Fatalf("legacy fact = %#v", response.Results[0])
	}
	if response.Results[1].EvidenceID != "claim-1" || !strings.Contains(response.Results[1].Context, "pgvector") {
		t.Fatalf("legacy claim = %#v", response.Results[1])
	}
	if response.Results[2].EvidenceID != "fragment-1" || response.Results[2].Context == "" {
		t.Fatalf("legacy fragment = %#v", response.Results[2])
	}
}
