package neo4j

import (
	"fmt"
	"regexp"
	"strings"
)

// QueryGuardInterface is the companion interface for QueryGuard.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type QueryGuardInterface interface {
	Validate(query string) error
	InjectProfilePredicate(query, profileID string) (string, error)
}

// QueryGuard validates and sanitizes Cypher queries for safe execution.
// It enforces read-only operations and ensures team_id predicates are present.
type QueryGuard struct{}

// Ensure QueryGuard implements QueryGuardInterface
var _ QueryGuardInterface = (*QueryGuard)(nil)

// NewQueryGuard creates a new query guard.
func NewQueryGuard() *QueryGuard {
	return &QueryGuard{}
}

// writeClausePattern matches write operations in Cypher queries.
// These keywords indicate data modification operations.
var writeClausePattern = regexp.MustCompile(`(?i)\b(CREATE|MERGE|SET|DELETE|REMOVE|DETACH)\b`)

// apocProcedurePattern matches APOC procedure calls.
var apocProcedurePattern = regexp.MustCompile(`(?i)\bCALL\s+apoc\.`)

// networkProcedurePattern matches network/file system procedures.
var networkProcedurePattern = regexp.MustCompile(`(?i)\b(CALL\s+(apoc\.load|apoc\.export|apoc\.import)|dbms\.)`)

// callInTransactionsPattern matches "CALL { ... } IN TRANSACTIONS" pattern.
var callInTransactionsPattern = regexp.MustCompile(`(?i)\}\s*IN\s+TRANSACTIONS\b`)

// ValidationError represents a query validation failure.
type ValidationError struct {
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("query validation failed: %s", e.Reason)
}

// Validate checks if a Cypher query is safe for execution.
// It rejects queries containing:
// - Write clauses: CREATE, MERGE, SET, DELETE, REMOVE, DETACH
// - APOC procedures: any apoc. prefixed call
// - Network/file procedures: apoc.load, apoc.export, apoc.import, dbms.
// - Call in transactions
func (g *QueryGuard) Validate(query string) error {
	// Check for write clauses
	if writeClausePattern.MatchString(query) {
		return &ValidationError{Reason: "query contains write clauses (CREATE, MERGE, SET, DELETE, REMOVE, DETACH)"}
	}

	// Check for APOC procedures
	if apocProcedurePattern.MatchString(query) {
		return &ValidationError{Reason: "query contains APOC procedures which are not allowed"}
	}

	// Check for network/file procedures
	if networkProcedurePattern.MatchString(query) {
		return &ValidationError{Reason: "query contains network or file system procedures which are not allowed"}
	}

	// Check for CALL ... IN TRANSACTIONS
	if callInTransactionsPattern.MatchString(query) {
		return &ValidationError{Reason: "query contains CALL ... IN TRANSACTIONS which is not allowed"}
	}

	return nil
}

// profileIDPattern matches team_id filters in WHERE clauses.
var profileIDPattern = regexp.MustCompile(`(?i)\bteam_id\s*=\s*\$`)

// InjectProfilePredicate ensures a query has a team_id filter.
// If the query already contains a team_id filter, it returns the query unchanged.
// If missing, it injects a WHERE clause to enforce profile isolation.
// Returns an error if injection is ambiguous or unsafe.
func (g *QueryGuard) InjectProfilePredicate(query, profileID string) (string, error) {
	// Check if team_id filter already exists
	if profileIDPattern.MatchString(query) {
		// Query already has team_id parameter filter
		return query, nil
	}

	// Check for literal team_id comparison (without parameter)
	literalProfileIDPattern := regexp.MustCompile(`(?i)\bteam_id\s*=\s*['"]`)
	if literalProfileIDPattern.MatchString(query) {
		// Query has literal team_id, which is acceptable
		return query, nil
	}

	// Need to inject team_id predicate
	// Normalize query whitespace for analysis
	normalizedQuery := strings.TrimSpace(query)

	// Determine if query has a WHERE clause
	wherePattern := regexp.MustCompile(`(?i)\bWHERE\b`)
	hasWhere := wherePattern.MatchString(normalizedQuery)

	// Find the appropriate place to inject
	// Look for MATCH clause to determine the node variable to use
	// Pattern: MATCH (n:Label) or MATCH (n)
	nodeVarPattern := regexp.MustCompile(`(?i)MATCH\s*\(\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*(?::\w+)?\)`)
	matches := nodeVarPattern.FindStringSubmatch(normalizedQuery)

	if len(matches) < 2 {
		// Cannot determine node variable - query structure is too complex or ambiguous
		return "", &ValidationError{Reason: "cannot determine node variable for team_id injection - query structure is ambiguous or unsupported"}
	}

	nodeVar := matches[1]

	// Check if the query has IN predicate in team_id check (more complex)
	// e.g., team_id IN [...] - we consider this as already having a filter
	profileIDInPattern := regexp.MustCompile(`(?i)\bteam_id\s+IN\b`)
	if profileIDInPattern.MatchString(normalizedQuery) {
		return query, nil
	}

	// Build the profile predicate
	profilePredicate := fmt.Sprintf("%s.team_id = '%s'", nodeVar, profileID)

	if hasWhere {
		// Append to existing WHERE clause
		// Find WHERE and append with AND
		whereIndex := wherePattern.FindStringIndex(normalizedQuery)
		if whereIndex == nil {
			return "", &ValidationError{Reason: "failed to locate WHERE clause for injection"}
		}

		// Find the end of WHERE clause (before ORDER BY, LIMIT, RETURN, WITH)
		endPattern := regexp.MustCompile(`(?i)\s+(ORDER\s+BY|LIMIT|RETURN|WITH)\b`)
		endMatch := endPattern.FindStringIndex(normalizedQuery[whereIndex[0]:])

		if endMatch == nil {
			// No end clause found, append to end
			return normalizedQuery + " AND " + profilePredicate, nil
		}

		// Insert before the end clause
		insertPos := whereIndex[0] + endMatch[0]
		return normalizedQuery[:insertPos] + " AND " + profilePredicate + normalizedQuery[insertPos:], nil
	}

	// No WHERE clause - need to add one
	// Find the first MATCH clause end (look for RETURN, WITH, or end of string)
	returnPattern := regexp.MustCompile(`(?i)\s+(RETURN|WITH|ORDER)\b`)
	returnMatch := returnPattern.FindStringIndex(normalizedQuery)

	if returnMatch == nil {
		// No RETURN/WITH found, append WHERE at the end
		return normalizedQuery + " WHERE " + profilePredicate, nil
	}

	// Insert WHERE clause before RETURN/WITH/ORDER
	insertPos := returnMatch[0]
	return normalizedQuery[:insertPos] + " WHERE " + profilePredicate + normalizedQuery[insertPos:], nil
}
