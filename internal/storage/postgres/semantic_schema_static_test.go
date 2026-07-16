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
	flexBody, err := os.ReadFile("../../../migrations/postgres/2026071602_semantic_search_embedding_flexible_dimensions.sql")
	if err != nil {
		t.Fatalf("read semantic flexible embedding migration: %v", err)
	}
	securityBody, err := os.ReadFile("../../../migrations/postgres/2026071603_semantic_evidence_security_events.sql")
	if err != nil {
		t.Fatalf("read semantic evidence security migration: %v", err)
	}
	valueBackfillBody, err := os.ReadFile("../../../migrations/postgres/2026071604_semantic_values_backfill.sql")
	if err != nil {
		t.Fatalf("read semantic values backfill migration: %v", err)
	}
	sql := string(body)
	lower := strings.ToLower(sql)
	flexSQL := string(flexBody)
	valueBackfillSQL := string(valueBackfillBody)
	flexParts := strings.SplitN(flexSQL, "-- +goose Down", 2)
	if len(flexParts) != 2 {
		t.Fatal("semantic flexible embedding migration must define up and down sections")
	}
	flexUp := flexParts[0]

	for _, forbidden := range []string{"semantic_assertions", "policy_family", "'dream'"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("semantic migration contains forbidden legacy vocabulary %q", forbidden)
		}
	}

	for _, table := range []string{
		"semantic_evidence_fragments",
		"semantic_entities",
		"semantic_values",
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
	if !strings.Contains(sql, "embedding vector(3072),") ||
		!strings.Contains(sql, "vector_dims(embedding) = 3072") ||
		strings.Count(sql, "USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops)") != 4 {
		t.Fatal("deployed semantic search migration must keep its original 3072-dimensional shape")
	}
	if !strings.Contains(flexUp, "ALTER COLUMN embedding TYPE vector USING embedding::vector") ||
		!strings.Contains(flexUp, "vector_dims(embedding) BETWEEN 1 AND 16000") {
		t.Fatal("semantic flexible embedding migration must convert search embeddings to configured dimensions")
	}
	for _, indexName := range []string{
		"semantic_search_documents_evidence_hnsw_idx",
		"semantic_search_documents_relationship_hnsw_idx",
		"semantic_search_documents_entity_hnsw_idx",
		"semantic_search_documents_value_hnsw_idx",
	} {
		if !strings.Contains(flexUp, "DROP INDEX IF EXISTS "+indexName) {
			t.Fatalf("semantic flexible embedding migration must drop fixed-dimension index %s", indexName)
		}
	}
	if strings.Contains(flexUp, "vector(3072)") || strings.Contains(flexUp, "halfvec(3072)") {
		t.Fatal("semantic flexible embedding migration up must not create fixed-dimension vector indexes")
	}
	if !strings.Contains(flexSQL, "cannot restore semantic_search_documents.embedding to vector(3072)") {
		t.Fatal("semantic flexible embedding migration down must guard non-3072 embeddings")
	}
	securitySQL := string(securityBody)
	for _, table := range []string{
		"semantic_evidence_security_events",
		"semantic_evidence_security_signals",
	} {
		if !strings.Contains(securitySQL, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("semantic evidence security migration missing table %s", table)
		}
		if !strings.Contains(securitySQL, "ALTER TABLE %I ENABLE ROW LEVEL SECURITY") ||
			!strings.Contains(securitySQL, "'"+table+"'") {
			t.Fatalf("semantic evidence security migration does not enable RLS for %s", table)
		}
		if !strings.Contains(securitySQL, table+"_append_only") {
			t.Fatalf("semantic evidence security migration missing append-only trigger for %s", table)
		}
	}
	for _, token := range []string{
		"'deterministic_scan'",
		"'reviewer_signal'",
		"'verifier_signal'",
		"'quarantine_release'",
		"'role_control_spoofing'",
		"'instruction_override'",
		"'prompt_secret_extraction'",
		"'tool_exfiltration'",
		"'obfuscated_instruction'",
		"'hidden_control_markup'",
	} {
		if !strings.Contains(securitySQL, token) {
			t.Fatalf("semantic evidence security migration missing allowlist token %s", token)
		}
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
	for _, token := range []string{
		"CREATE TABLE IF NOT EXISTS semantic_values",
		"ADD COLUMN IF NOT EXISTS object_value_id",
		"semantic_relationship_object_exactly_one",
		"CHECK (source_type IN ('evidence', 'relationship', 'entity', 'value'))",
		"DROP COLUMN IF EXISTS object_value",
		"DROP COLUMN IF EXISTS object_kind",
	} {
		if !strings.Contains(valueBackfillSQL, token) {
			t.Fatalf("semantic values backfill migration missing %s", token)
		}
	}
}
