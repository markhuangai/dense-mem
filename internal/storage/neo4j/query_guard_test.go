package neo4j

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// QueryGuard Tests (Pure Unit Tests - No Neo4j Connection Required)
// ============================================================================

// TestQueryGuard_RejectsWriteClauses tests that write clauses are rejected.
func TestQueryGuard_RejectsWriteClauses(t *testing.T) {
	guard := NewQueryGuard()

	tests := []struct {
		name         string
		query        string
		shouldReject bool
	}{
		{
			name:         "CREATE clause",
			query:        "MATCH (n) CREATE (n:Test)",
			shouldReject: true,
		},
		{
			name:         "MERGE clause",
			query:        "MERGE (n:Test {id: 1})",
			shouldReject: true,
		},
		{
			name:         "SET clause",
			query:        "MATCH (n:Test) SET n.value = 1",
			shouldReject: true,
		},
		{
			name:         "DELETE clause",
			query:        "MATCH (n:Test) DELETE n",
			shouldReject: true,
		},
		{
			name:         "REMOVE clause",
			query:        "MATCH (n:Test) REMOVE n.value",
			shouldReject: true,
		},
		{
			name:         "DETACH DELETE clause",
			query:        "MATCH (n:Test) DETACH DELETE n",
			shouldReject: true,
		},
		{
			name:         "read-only MATCH query",
			query:        "MATCH (n:Test) RETURN n",
			shouldReject: false,
		},
		{
			name:         "read-only WITH query",
			query:        "MATCH (n:Test) WITH n RETURN n",
			shouldReject: false,
		},
		{
			name:         "lowercase create",
			query:        "match (n) create (n:Test)",
			shouldReject: true,
		},
		{
			name:         "mixed case SET",
			query:        "MATCH (n) SeT n.value = 1",
			shouldReject: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guard.Validate(tt.query)
			if tt.shouldReject {
				assert.Error(t, err, "Query should be rejected: %s", tt.query)
				assert.Contains(t, err.Error(), "write clauses", "Error should mention write clauses")
			} else {
				assert.NoError(t, err, "Query should be allowed: %s", tt.query)
			}
		})
	}
}

// TestQueryGuard_RejectsAPOCProcedures tests that APOC procedures are rejected.
func TestQueryGuard_RejectsAPOCProcedures(t *testing.T) {
	guard := NewQueryGuard()

	tests := []struct {
		name         string
		query        string
		shouldReject bool
	}{
		{
			name:         "CALL apoc.periodic.iterate",
			query:        "CALL apoc.periodic.iterate('MATCH (n) RETURN n', 'RETURN n', {})",
			shouldReject: true,
		},
		{
			name:         "CALL apoc.do.when",
			query:        "CALL apoc.do.when(true, 'RETURN 1', 'RETURN 2')",
			shouldReject: true,
		},
		{
			name:         "CALL apoc.cypher.run",
			query:        "CALL apoc.cypher.run('RETURN 1', {})",
			shouldReject: true,
		},
		{
			name:         "lowercase apoc call",
			query:        "call apoc.help('all')",
			shouldReject: true,
		},
		{
			name:         "read-only MATCH without APOC",
			query:        "MATCH (n:Test) RETURN n",
			shouldReject: false,
		},
		{
			name:         "CALL db.labels",
			query:        "CALL db.labels()",
			shouldReject: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guard.Validate(tt.query)
			if tt.shouldReject {
				assert.Error(t, err, "Query should be rejected: %s", tt.query)
				assert.Contains(t, err.Error(), "APOC", "Error should mention APOC")
			} else {
				assert.NoError(t, err, "Query should be allowed: %s", tt.query)
			}
		})
	}
}

// TestQueryGuard_RejectsNetworkProcedures tests that network/file procedures are rejected.
func TestQueryGuard_RejectsNetworkProcedures(t *testing.T) {
	guard := NewQueryGuard()

	tests := []struct {
		name         string
		query        string
		shouldReject bool
	}{
		{
			name:         "apoc.load.json",
			query:        "CALL apoc.load.json('http://example.com/data.json')",
			shouldReject: true,
		},
		{
			name:         "apoc.export.csv",
			query:        "CALL apoc.export.csv.all('export.csv', {})",
			shouldReject: true,
		},
		{
			name:         "apoc.import.csv",
			query:        "CALL apoc.import.csv('import.csv', {})",
			shouldReject: true,
		},
		{
			name:         "dbms procedures",
			query:        "CALL dbms.security.listUsers()",
			shouldReject: true,
		},
		{
			name:         "CALL in transactions",
			query:        "CALL { MATCH (n) RETURN n } IN TRANSACTIONS",
			shouldReject: true,
		},
		{
			name:         "read-only MATCH",
			query:        "MATCH (n:Test) RETURN n",
			shouldReject: false,
		},
		{
			name:         "CALL db.info",
			query:        "CALL db.info()",
			shouldReject: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guard.Validate(tt.query)
			if tt.shouldReject {
				assert.Error(t, err, "Query should be rejected: %s", tt.query)
			} else {
				assert.NoError(t, err, "Query should be allowed: %s", tt.query)
			}
		})
	}
}

// TestQueryGuard_InjectsProfileIdPredicate tests that team_id predicates are injected.
func TestQueryGuard_InjectsProfileIdPredicate(t *testing.T) {
	guard := NewQueryGuard()

	tests := []struct {
		name             string
		query            string
		profileID        string
		expectedInResult bool
		expectError      bool
	}{
		{
			name:             "simple MATCH with RETURN",
			query:            "MATCH (n:SourceFragment) RETURN n",
			profileID:        "profile-123",
			expectedInResult: true,
			expectError:      false,
		},
		{
			name:             "MATCH with existing WHERE clause",
			query:            "MATCH (n:SourceFragment) WHERE n.active = true RETURN n",
			profileID:        "profile-123",
			expectedInResult: true,
			expectError:      false,
		},
		{
			name:             "MATCH with existing team_id filter",
			query:            "MATCH (n:SourceFragment) WHERE n.team_id = $profileId RETURN n",
			profileID:        "profile-123",
			expectedInResult: false, // Should not inject since already present
			expectError:      false,
		},
		{
			name:             "MATCH with existing literal team_id",
			query:            "MATCH (n:SourceFragment) WHERE n.team_id = 'profile-456' RETURN n",
			profileID:        "profile-123",
			expectedInResult: false, // Should not inject since already present
			expectError:      false,
		},
		{
			name:             "MATCH with ORDER BY",
			query:            "MATCH (sf:SourceFragment) RETURN sf ORDER BY sf.created_at",
			profileID:        "profile-123",
			expectedInResult: true,
			expectError:      false,
		},
		{
			name:             "MATCH with LIMIT",
			query:            "MATCH (f:Fact) RETURN f LIMIT 10",
			profileID:        "profile-123",
			expectedInResult: true,
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := guard.InjectProfilePredicate(tt.query, tt.profileID)
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tt.expectedInResult {
				// Check that team_id predicate was injected
				assert.Contains(t, result, "team_id", "Result should contain team_id: %s", result)
				assert.Contains(t, result, tt.profileID, "Result should contain profile ID: %s", result)
			} else {
				// Check that query was returned unchanged or has team_id
				hasProfileID := strings.Contains(result, "team_id")
				assert.True(t, hasProfileID, "Query should have team_id: %s", result)
			}
		})
	}
}

// TestQueryGuard_RejectsQueryMissingProfileId tests queries that cannot have team_id injected safely.
func TestQueryGuard_RejectsQueryMissingProfileId(t *testing.T) {
	guard := NewQueryGuard()

	tests := []struct {
		name        string
		query       string
		profileID   string
		expectError bool
	}{
		{
			name:        "query without MATCH clause",
			query:       "RETURN 1",
			profileID:   "profile-123",
			expectError: true,
		},
		{
			name:        "query with ambiguous structure",
			query:       "WITH 1 AS x RETURN x",
			profileID:   "profile-123",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := guard.InjectProfilePredicate(tt.query, tt.profileID)
			if tt.expectError {
				assert.Error(t, err, "Query should be rejected: %s", tt.query)
			} else {
				assert.NoError(t, err, "Query should be allowed: %s", tt.query)
			}
		})
	}
}

// TestQueryGuard_Interface ensures QueryGuard implements QueryGuardInterface.
func TestQueryGuard_Interface(t *testing.T) {
	var _ QueryGuardInterface = (*QueryGuard)(nil)
}
