package graphquery

import (
	"context"
	"errors"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockScopedReader implements ScopedReaderInterface for testing.
type mockScopedReader struct {
	scopedReadFunc func(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error)
}

func (m *mockScopedReader) ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	if m.scopedReadFunc != nil {
		return m.scopedReadFunc(ctx, profileID, query, params)
	}
	return nil, nil, nil
}

// mockValidator implements CypherValidator for testing.
type mockValidator struct {
	validateFunc func(query string) error
}

func (m *mockValidator) Validate(query string) error {
	if m.validateFunc != nil {
		return m.validateFunc(query)
	}
	return nil
}

// TestGraphQueryReturnsRows tests that query executes and returns the specified response shape.
func TestGraphQueryReturnsRows(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		params         map[string]any
		mockRows       []map[string]any
		expectLimitCap bool
	}{
		{
			name:  "simple query with results",
			query: "MATCH (n:Node {team_id: $profileId}) RETURN n.name, n.value",
			mockRows: []map[string]any{
				{"n.name": "test1", "n.value": 1},
				{"n.name": "test2", "n.value": 2},
			},
			expectLimitCap: true,
		},
		{
			name:  "query with params",
			query: "MATCH (n:Node {team_id: $profileId}) WHERE n.value > $minValue RETURN n",
			params: map[string]any{
				"minValue": 10,
			},
			mockRows: []map[string]any{
				{"n": map[string]any{"name": "test"}},
			},
			expectLimitCap: true,
		},
		{
			name:           "query with no results",
			query:          "MATCH (n:Node {team_id: $profileId}) WHERE n.value = 'nonexistent' RETURN n",
			mockRows:       []map[string]any{},
			expectLimitCap: true,
		},
		{
			name:  "query with existing limit within bounds",
			query: "MATCH (n:Node {team_id: $profileId}) RETURN n LIMIT 100",
			mockRows: []map[string]any{
				{"n": map[string]any{"name": "test"}},
			},
			expectLimitCap: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			profileID := "test-profile-id"

			var capturedQuery string
			mockReader := &mockScopedReader{
				scopedReadFunc: func(ctx context.Context, pid string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
					assert.Equal(t, profileID, pid)
					capturedQuery = query
					// Note: ProfileScopeEnforcer would inject profileId into params, but this mock doesn't
					return nil, tt.mockRows, nil
				},
			}

			mockValidator := &mockValidator{
				validateFunc: func(query string) error {
					// Allow all queries for this test
					return nil
				},
			}

			svc := NewGraphQueryService(mockReader, mockValidator)
			result, err := svc.Execute(ctx, profileID, tt.query, tt.params)

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, len(tt.mockRows), result.RowCount)
			assert.Equal(t, tt.mockRows, result.Rows)
			assert.Equal(t, tt.expectLimitCap, result.RowCapApplied)

			// Verify LIMIT was added if expected
			if tt.expectLimitCap {
				assert.Contains(t, capturedQuery, "LIMIT 1000")
			}
		})
	}
}

// TestGraphQueryCapsOrRejectsLimit tests LIMIT enforcement.
func TestGraphQueryCapsOrRejectsLimit(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		expectError bool
		errorType   string
	}{
		{
			name:        "no LIMIT - adds default cap",
			query:       "MATCH (n:Node {team_id: $profileId}) RETURN n",
			expectError: false,
		},
		{
			name:        "LIMIT within bounds - allowed",
			query:       "MATCH (n:Node {team_id: $profileId}) RETURN n LIMIT 500",
			expectError: false,
		},
		{
			name:        "LIMIT exactly at max - allowed",
			query:       "MATCH (n:Node {team_id: $profileId}) RETURN n LIMIT 1000",
			expectError: false,
		},
		{
			name:        "LIMIT exceeds max - rejected",
			query:       "MATCH (n:Node {team_id: $profileId}) RETURN n LIMIT 1500",
			expectError: true,
			errorType:   "limit",
		},
		{
			name:        "LIMIT zero - rejected",
			query:       "MATCH (n:Node {team_id: $profileId}) RETURN n LIMIT 0",
			expectError: true,
			errorType:   "limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			profileID := "test-profile-id"

			mockReader := &mockScopedReader{
				scopedReadFunc: func(ctx context.Context, pid string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
					return nil, []map[string]any{}, nil
				},
			}

			mockValidator := &mockValidator{
				validateFunc: func(query string) error {
					return nil
				},
			}

			svc := NewGraphQueryService(mockReader, mockValidator)
			result, err := svc.Execute(ctx, profileID, tt.query, nil)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorType == "limit" {
					assert.True(t, IsLimitError(err), "expected LimitError, got: %v", err)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
			}
		})
	}
}

func TestGraphQueryServicePreservesLiteralLimit(t *testing.T) {
	ctx := context.Background()
	profileID := "test-profile-id"
	var capturedQuery string

	mockReader := &mockScopedReader{
		scopedReadFunc: func(ctx context.Context, pid string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
			assert.Equal(t, profileID, pid)
			capturedQuery = query
			assert.Empty(t, params)
			return nil, []map[string]any{{"edge_type": "SUPPORTED_BY"}}, nil
		},
	}

	svc := NewGraphQueryService(mockReader, &mockValidator{})
	result, err := svc.Execute(ctx, profileID, `
MATCH (a {team_id: $profileId})-[rel]->(b {team_id: $profileId})
WHERE rel.team_id = $profileId
RETURN type(rel) AS edge_type
LIMIT 4`, nil)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.RowCapApplied)
	assert.Contains(t, capturedQuery, "LIMIT 4")
	assert.NotContains(t, capturedQuery, "LIMIT 1000")
}

// TestGraphQuerySanitizesSyntaxError tests that Neo4j syntax errors are sanitized.
func TestGraphQuerySanitizesSyntaxError(t *testing.T) {
	tests := []struct {
		name            string
		driverError     error
		expectError     bool
		expectSanitized bool
	}{
		{
			name:            "syntax error - sanitized",
			driverError:     errors.New("SyntaxException: Invalid input 'MARCH' expected 'MATCH'"),
			expectError:     true,
			expectSanitized: true,
		},
		{
			name:            "invalid input error - sanitized",
			driverError:     errors.New("Invalid input 'RUTRUN' expected 'RETURN'"),
			expectError:     true,
			expectSanitized: true,
		},
		{
			name:            "unknown function error - sanitized",
			driverError:     errors.New("Unknown function 'invalidFunc'"),
			expectError:     true,
			expectSanitized: true,
		},
		{
			name:            "other error - passed through",
			driverError:     errors.New("connection refused"),
			expectError:     true,
			expectSanitized: false,
		},
		{
			name:        "no error - success",
			driverError: nil,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			profileID := "test-profile-id"
			query := "MATCH (n:Node {team_id: $profileId}) RETURN n"

			mockReader := &mockScopedReader{
				scopedReadFunc: func(ctx context.Context, pid string, q string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
					if tt.driverError != nil {
						return nil, nil, tt.driverError
					}
					return nil, []map[string]any{}, nil
				},
			}

			mockValidator := &mockValidator{
				validateFunc: func(query string) error {
					return nil
				},
			}

			svc := NewGraphQueryService(mockReader, mockValidator)
			result, err := svc.Execute(ctx, profileID, query, nil)

			if tt.expectError {
				require.Error(t, err)
				if tt.expectSanitized {
					// Sanitized errors should be SyntaxError
					assert.True(t, IsSyntaxError(err), "expected SyntaxError, got: %v", err)
					// Should not contain driver internals
					assert.NotContains(t, err.Error(), "SyntaxException:")
					assert.NotContains(t, err.Error(), "driver")
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
			}
		})
	}
}

// TestGraphQueryRejectsForbiddenParams tests that profileId/team_id params are rejected.
func TestGraphQueryRejectsForbiddenParams(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
	}{
		{
			name: "profileId param rejected",
			params: map[string]any{
				"profileId": "should-be-rejected",
			},
		},
		{
			name: "team_id param rejected",
			params: map[string]any{
				"team_id": "should-be-rejected",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			profileID := "test-profile-id"
			query := "MATCH (n:Node {team_id: $profileId}) RETURN n"

			mockReader := &mockScopedReader{}
			mockValidator := &mockValidator{}

			svc := NewGraphQueryService(mockReader, mockValidator)
			_, err := svc.Execute(ctx, profileID, query, tt.params)

			require.Error(t, err)
			assert.True(t, IsForbiddenParamError(err), "expected ForbiddenParamError, got: %v", err)
		})
	}
}

// TestProcessLimitClause tests the LIMIT clause processing logic.
func TestProcessLimitClause(t *testing.T) {
	tests := []struct {
		name          string
		query         string
		expectCap     bool
		expectError   bool
		expectedLimit string
	}{
		{
			name:          "no LIMIT - appends default",
			query:         "MATCH (n) RETURN n",
			expectCap:     true,
			expectedLimit: "MATCH (n) RETURN n LIMIT 1000",
		},
		{
			name:          "LIMIT 100 - allowed",
			query:         "MATCH (n) RETURN n LIMIT 100",
			expectCap:     false,
			expectedLimit: "MATCH (n) RETURN n LIMIT 100",
		},
		{
			name:          "LIMIT 1000 - allowed",
			query:         "MATCH (n) RETURN n LIMIT 1000",
			expectCap:     false,
			expectedLimit: "MATCH (n) RETURN n LIMIT 1000",
		},
		{
			name:        "LIMIT 1001 - rejected",
			query:       "MATCH (n) RETURN n LIMIT 1001",
			expectError: true,
		},
		{
			name:        "LIMIT 0 - rejected",
			query:       "MATCH (n) RETURN n LIMIT 0",
			expectError: true,
		},
		{
			name:          "case-insensitive LIMIT",
			query:         "MATCH (n) RETURN n limit 50",
			expectCap:     false,
			expectedLimit: "MATCH (n) RETURN n limit 50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, rowCapApplied, err := processLimitClause(tt.query)

			if tt.expectError {
				require.Error(t, err)
				assert.True(t, IsLimitError(err))
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectCap, rowCapApplied)
				assert.Equal(t, tt.expectedLimit, result)
			}
		})
	}
}
