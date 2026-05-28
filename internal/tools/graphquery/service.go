package graphquery

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	// MaxRowLimit is the maximum allowed LIMIT value for queries.
	MaxRowLimit = 1000
	// DefaultRowLimit is the default LIMIT applied when none is specified.
	DefaultRowLimit = 1000
)

// GraphQueryResult represents the result of a graph query execution.
type GraphQueryResult struct {
	Columns       []string         `json:"columns"`
	Rows          []map[string]any `json:"rows"`
	RowCount      int              `json:"row_count"`
	RowCapApplied bool             `json:"row_cap_applied"`
}

// GraphQueryService is the interface for executing graph queries.
type GraphQueryService interface {
	Execute(ctx context.Context, profileID string, query string, params map[string]any) (*GraphQueryResult, error)
}

// ScopedReaderInterface is the interface for scoped read operations.
// This is a subset of ProfileScopeEnforcer for dependency injection.
type ScopedReaderInterface interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error)
}

// graphQueryService implements GraphQueryService.
type graphQueryService struct {
	reader    ScopedReaderInterface
	validator CypherValidator
}

// Ensure graphQueryService implements GraphQueryService.
var _ GraphQueryService = (*graphQueryService)(nil)

// NewGraphQueryService creates a new GraphQueryService.
func NewGraphQueryService(reader ScopedReaderInterface, validator CypherValidator) GraphQueryService {
	return &graphQueryService{
		reader:    reader,
		validator: validator,
	}
}

// Execute validates, prepares, and executes a Cypher query with profile scoping.
func (s *graphQueryService) Execute(ctx context.Context, profileID string, query string, params map[string]any) (*GraphQueryResult, error) {
	// Validate query with CypherValidator
	if err := s.validator.Validate(query); err != nil {
		return nil, err
	}

	// Check for forbidden params (profileId, team_id)
	if err := validateParams(params); err != nil {
		return nil, err
	}

	// Process LIMIT clause
	processedQuery, rowCapApplied, err := processLimitClause(query)
	if err != nil {
		return nil, err
	}

	// Execute via ScopedRead
	_, rows, err := s.reader.ScopedRead(ctx, profileID, processedQuery, params)
	if err != nil {
		return nil, sanitizeNeo4jError(err)
	}

	// Extract columns from rows
	columns := extractColumns(rows)

	return &GraphQueryResult{
		Columns:       columns,
		Rows:          rows,
		RowCount:      len(rows),
		RowCapApplied: rowCapApplied,
	}, nil
}

// limitPattern matches LIMIT clauses in Cypher queries.
// Matches: LIMIT <number> with optional whitespace
var limitPattern = regexp.MustCompile(`(?i)\bLIMIT\s+(\d+)\b`)

// processLimitClause handles LIMIT clause processing.
// - If no LIMIT, appends LIMIT 1000 and returns rowCapApplied=true.
// - If LIMIT > 1000 or LIMIT 0, returns error.
// - Otherwise returns the query as-is with rowCapApplied=false.
func processLimitClause(query string) (string, bool, error) {
	// Find all LIMIT clauses
	matches := limitPattern.FindAllStringSubmatch(query, -1)

	if len(matches) == 0 {
		// No LIMIT found - append default
		return strings.TrimSpace(query) + " LIMIT " + strconv.Itoa(DefaultRowLimit), true, nil
	}

	// Check each LIMIT value
	for _, match := range matches {
		if len(match) > 1 {
			limitStr := match[1]
			limit, err := strconv.Atoi(limitStr)
			if err != nil {
				continue // Skip unparseable limits
			}

			// LIMIT 0 or LIMIT > 1000 is rejected
			if limit == 0 {
				return "", false, NewLimitError("LIMIT 0 is not allowed")
			}
			if limit > MaxRowLimit {
				return "", false, NewLimitError(fmt.Sprintf("LIMIT %d exceeds maximum allowed value of %d", limit, MaxRowLimit))
			}
		}
	}

	// LIMIT is valid, return as-is
	return query, false, nil
}

// validateParams checks that params don't contain forbidden keys.
func validateParams(params map[string]any) error {
	if params == nil {
		return nil
	}

	// Check for profileId and team_id
	forbiddenKeys := []string{"profileId", "team_id"}
	for _, key := range forbiddenKeys {
		if _, exists := params[key]; exists {
			return NewForbiddenParamError(key)
		}
	}

	return nil
}

// extractColumns extracts column names from the first row of results.
func extractColumns(rows []map[string]any) []string {
	if len(rows) == 0 {
		return []string{}
	}

	// Get keys from the first row
	firstRow := rows[0]
	columns := make([]string, 0, len(firstRow))
	for col := range firstRow {
		columns = append(columns, col)
	}

	return columns
}

// sanitizeNeo4jError converts Neo4j driver errors to user-safe errors.
func sanitizeNeo4jError(err error) error {
	if err == nil {
		return nil
	}

	// Check for Neo4j syntax errors
	errStr := err.Error()
	if strings.Contains(errStr, "SyntaxException") ||
		strings.Contains(errStr, "syntax error") ||
		strings.Contains(errStr, "Invalid input") ||
		strings.Contains(errStr, "Unknown function") {
		return NewSyntaxError("query syntax error")
	}

	// Return generic error for other cases
	return err
}

// LimitError represents an error with LIMIT clause validation.
type LimitError struct {
	Message string
}

// Error implements the error interface.
func (e *LimitError) Error() string {
	return e.Message
}

// NewLimitError creates a new LimitError.
func NewLimitError(message string) *LimitError {
	return &LimitError{Message: message}
}

// IsLimitError checks if an error is a LimitError.
func IsLimitError(err error) bool {
	var limitErr *LimitError
	return errors.As(err, &limitErr)
}

// ForbiddenParamError represents an error when forbidden params are provided.
type ForbiddenParamError struct {
	Param string
}

// Error implements the error interface.
func (e *ForbiddenParamError) Error() string {
	return fmt.Sprintf("parameter '%s' is not allowed", e.Param)
}

// NewForbiddenParamError creates a new ForbiddenParamError.
func NewForbiddenParamError(param string) *ForbiddenParamError {
	return &ForbiddenParamError{Param: param}
}

// IsForbiddenParamError checks if an error is a ForbiddenParamError.
func IsForbiddenParamError(err error) bool {
	var forbiddenErr *ForbiddenParamError
	return errors.As(err, &forbiddenErr)
}

// SyntaxError represents a sanitized syntax error.
type SyntaxError struct {
	Message string
}

// Error implements the error interface.
func (e *SyntaxError) Error() string {
	return e.Message
}

// NewSyntaxError creates a new SyntaxError.
func NewSyntaxError(message string) *SyntaxError {
	return &SyntaxError{Message: message}
}

// IsSyntaxError checks if an error is a SyntaxError.
func IsSyntaxError(err error) bool {
	var syntaxErr *SyntaxError
	return errors.As(err, &syntaxErr)
}
