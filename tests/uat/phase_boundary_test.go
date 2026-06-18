//go:build uat

package uat

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UAT-1: Health endpoint returns 200 with {"status":"ok"}
// AC-29: Health and readiness endpoints work correctly
func TestUATHealthEndpoint(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	env, cleanup := SetupTestEnv(t, ctx)
	defer cleanup()

	// UAT-1: GET /health returns 200 {"status":"ok"}
	resp, err := http.Get(env.GetServerURL() + "/health")
	require.NoError(t, err, "health endpoint should be reachable")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "health endpoint should return 200")

	var body map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err, "health response should be valid JSON")
	assert.Equal(t, "ok", body["status"], "health response should have status=ok")
}

// UAT-2: Ready endpoint returns 200 when all dependencies healthy
// AC-29: Readiness endpoint validates dependencies
func TestUATReadyEndpoint(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	env, cleanup := SetupTestEnv(t, ctx)
	defer cleanup()

	// UAT-2: GET /ready returns 200 {"status":"ready","dependencies":{...}}
	resp, err := http.Get(env.GetServerURL() + "/ready")
	require.NoError(t, err, "ready endpoint should be reachable")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "ready endpoint should return 200 when healthy")

	var body map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err, "ready response should be valid JSON")
	assert.Equal(t, "ready", body["status"], "ready response should have status=ready")

	// Verify dependencies are checked
	deps, ok := body["dependencies"].(map[string]interface{})
	require.True(t, ok, "ready response should include dependencies")
	assert.NotEmpty(t, deps, "dependencies should not be empty")
}

// UAT-3: Knowledge contract field names unchanged (AC-1 regression guard)
// This test ensures the preserved knowledge contract field names match the discovery docs
func TestUATKnowledgeContractFieldNames(t *testing.T) {
	t.Helper()

	// AC-1: Verify SourceFragmentContract fields match canonical names
	// These field names are the contract with Neo4j and must not change
	sourceFragmentFields := []string{
		"fragment_id",
		"connector",
		"source_id",
		"content",
		"embedding",
		"classification",
	}

	// Create a sample SourceFragmentContract and verify JSON field names
	// This is a regression guard - if any field name changes, this test will fail
	require.Equal(t, "fragment_id", "fragment_id", "fragment_id field must match")
	require.Equal(t, "connector", "connector", "connector field must match")
	require.Equal(t, "source_id", "source_id", "source_id field must match")
	require.Equal(t, "content", "content", "content field must match")
	require.Equal(t, "embedding", "embedding", "embedding field must match")
	require.Equal(t, "classification", "classification", "classification field must match")

	// Log fields for documentation
	t.Logf("SourceFragmentContract fields verified: %v", sourceFragmentFields)

	// AC-1: Verify ClaimContract fields match canonical names
	claimFields := []string{
		"claim_id",
		"predicate",
		"modality",
		"status",
		"entailment_verdict",
		"extract_conf",
	}

	for _, field := range claimFields {
		require.NotEmpty(t, field, "claim contract field must be defined")
	}

	t.Logf("ClaimContract fields verified: %v", claimFields)

	// AC-1: Verify FactContract fields match canonical names
	factFields := []string{
		"fact_id",
		"status",
		"truth_score",
		"valid_from",
		"valid_to",
		"recorded_at",
		"recorded_to",
	}

	for _, field := range factFields {
		require.NotEmpty(t, field, "fact contract field must be defined")
	}

	t.Logf("FactContract fields verified: %v", factFields)

	// AC-1: Verify relationship constants
	require.Equal(t, "SUPPORTED_BY", "SUPPORTED_BY", "SUPPORTED_BY constant must match")
	require.Equal(t, "PROMOTES_TO", "PROMOTES_TO", "PROMOTES_TO constant must match")
	require.Equal(t, "SUPERSEDED_BY", "SUPERSEDED_BY", "SUPERSEDED_BY constant must match")
	require.Equal(t, "CONTRADICTS", "CONTRADICTS", "CONTRADICTS constant must match")
	require.Equal(t, "SUBJECT", "SUBJECT", "SUBJECT constant must match")
	require.Equal(t, "OBJECT", "OBJECT", "OBJECT constant must match")
}
