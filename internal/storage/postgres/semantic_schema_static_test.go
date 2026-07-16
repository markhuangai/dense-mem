package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestSemanticRelationshipMigrationStaticInvariants(t *testing.T) {
	body, err := os.ReadFile("../../../migrations/postgres/2026071001_semantic_relationships.sql")
	if err != nil {
		t.Fatalf("read semantic migration: %v", err)
	}
	sql := string(body)
	lower := strings.ToLower(sql)

	for _, forbidden := range []string{"semantic_assertions", "policy_family", "'dream'"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("semantic migration contains forbidden legacy vocabulary %q", forbidden)
		}
	}

	for _, table := range []string{
		"semantic_evidence_fragments",
		"semantic_entities",
		"semantic_relationship_records",
		"semantic_relationship_supports",
		"semantic_relationship_observations",
		"semantic_verification_events",
		"semantic_relationship_events",
		"semantic_search_documents",
		"semantic_embedding_jobs",
		"semantic_hypotheses",
	} {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("semantic migration missing table %s", table)
		}
		if !strings.Contains(sql, "ALTER TABLE "+table+" ENABLE ROW LEVEL SECURITY") &&
			!strings.Contains(sql, "'"+table+"'") {
			t.Fatalf("semantic migration does not enable RLS for %s", table)
		}
	}

	if !strings.Contains(sql, "PRIMARY KEY (team_id, relationship_id)") {
		t.Fatal("relationship records must use a compound team primary key")
	}
	if !strings.Contains(sql, "polarity TEXT NOT NULL DEFAULT '+'") ||
		!strings.Contains(sql, "CHECK (polarity IN ('+', '-'))") {
		t.Fatal("semantic relationships must persist explicit positive or negative polarity")
	}
	for _, status := range []string{
		"pending_evidence", "active", "needs_review", "quarantined",
		"disputed", "rejected", "retracted", "superseded",
	} {
		if !strings.Contains(sql, "'"+status+"'") {
			t.Fatalf("semantic relationship lifecycle is missing status %q", status)
		}
	}
	if !strings.Contains(sql, "status TEXT NOT NULL DEFAULT 'pending_evidence'") ||
		!strings.Contains(sql, "(tier = 'candidate' AND status <> 'active')") ||
		!strings.Contains(sql, "(tier IN ('validated_claim', 'fact') AND status = 'active')") {
		t.Fatal("semantic relationship tier/status invariants must be valid by default")
	}
	if !strings.Contains(sql, "status TEXT NOT NULL DEFAULT 'active'") ||
		!strings.Contains(sql, "CONSTRAINT semantic_entities_status_check CHECK (status IN ('active', 'merged', 'retracted'))") {
		t.Fatal("semantic entities must default to an allowed active status")
	}
	if !strings.Contains(sql, "CREATE UNIQUE INDEX IF NOT EXISTS semantic_relationship_identity_unique") {
		t.Fatal("semantic relationship identity must prevent duplicate lifecycle rows")
	}
	if !strings.Contains(sql, "CREATE INDEX IF NOT EXISTS semantic_relationship_supports_evidence_idx") ||
		!strings.Contains(sql, "ON semantic_relationship_supports(team_id, fragment_id, relationship_id)") {
		t.Fatal("semantic relationship supports must support evidence-id hydration lookups")
	}
	if !strings.Contains(sql, "CREATE OR REPLACE VIEW semantic_edges") {
		t.Fatal("semantic_edges view is required for graph-shaped reads")
	}
	for _, sourceType := range []string{"evidence", "relationship", "entity"} {
		if !strings.Contains(sql, "source_type = '"+sourceType+"'") {
			t.Fatalf("semantic search documents must define a source-specific HNSW index for %s", sourceType)
		}
	}
	if strings.Count(sql, "USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops)") != 3 {
		t.Fatal("semantic search must define one halfvec HNSW candidate index per source type")
	}
	if !strings.Contains(sql, "source_group TEXT NOT NULL DEFAULT ''") {
		t.Fatal("semantic schema must preserve source_group provenance")
	}
	if strings.Count(sql, "to_status TEXT NOT NULL DEFAULT ''") != 1 {
		t.Fatal("semantic relationship events must define to_status exactly once")
	}
	if !strings.Contains(sql, "CREATE UNIQUE INDEX IF NOT EXISTS semantic_embedding_jobs_active_unique") {
		t.Fatal("semantic embedding jobs must coalesce active jobs")
	}
	if !strings.Contains(sql, "CREATE INDEX IF NOT EXISTS semantic_embedding_jobs_expired_lease_idx") ||
		!strings.Contains(sql, "WHERE status = 'processing'") {
		t.Fatal("semantic embedding jobs must index expired processing leases")
	}
}
