package graphquery

import (
	"fmt"
	"regexp"
	"strings"
)

// CypherValidator is the companion interface for cypherValidator.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type CypherValidator interface {
	Validate(query string) error
}

// ValidationError represents a query validation failure with a specific reason.
type ValidationError struct {
	Reason string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("cypher validation failed: %s", e.Reason)
}

// cypherValidator validates Cypher queries for safe, scoped execution.
// It enforces read-only operations and ensures team_id predicates are present.
type cypherValidator struct{}

// Ensure cypherValidator implements CypherValidator
var _ CypherValidator = (*cypherValidator)(nil)

// NewCypherValidator creates a new Cypher validator.
func NewCypherValidator() CypherValidator {
	return &cypherValidator{}
}

// writeClausesPattern matches write operations and forbidden constructs in Cypher queries.
var writeClausesPattern = regexp.MustCompile(`(?i)\b(CREATE|MERGE|DELETE|SET|REMOVE|DROP|FOREACH|CALL|UNION|USE)\b`)

// loadCSVPattern matches LOAD CSV clause.
var loadCSVPattern = regexp.MustCompile(`(?i)\bLOAD\s+CSV\b`)

// semicolonPattern matches semicolons that would indicate multiple statements.
var semicolonPattern = regexp.MustCompile(`;`)

// optionalMatchPattern matches OPTIONAL MATCH clause.
var optionalMatchPattern = regexp.MustCompile(`(?i)\bOPTIONAL\s+MATCH\b`)

// Validate checks if a Cypher query is safe for scoped execution.
// It rejects queries containing:
// - Write clauses: CREATE, MERGE, DELETE, SET, REMOVE, DROP, FOREACH, LOAD CSV, CALL, UNION, USE
// - Multiple statements (semicolons)
// - Anonymous node patterns without aliases
// - Node patterns without team_id constraints
func (v *cypherValidator) Validate(query string) error {
	query = strings.TrimSpace(query)

	// Check for semicolons (multiple statements)
	if semicolonPattern.MatchString(query) {
		return &ValidationError{Reason: "multiple statements are not allowed (semicolon detected)"}
	}

	// Check for LOAD CSV first (before other write clauses)
	if loadCSVPattern.MatchString(query) {
		return &ValidationError{Reason: "query contains LOAD CSV which is not allowed"}
	}

	// Check for write clauses and forbidden constructs
	if match := writeClausesPattern.FindString(query); match != "" {
		return &ValidationError{Reason: fmt.Sprintf("query contains forbidden clause: %s", strings.ToUpper(match))}
	}

	// Check for anonymous node patterns
	if hasAnonymousNodePattern(query) {
		return &ValidationError{Reason: "all node patterns must have an alias"}
	}

	// Check if query has OPTIONAL MATCH
	hasOptionalMatch := optionalMatchPattern.MatchString(query)

	// Extract aliases from the main MATCH clause (before OPTIONAL MATCH if any)
	var mainQuery string
	if hasOptionalMatch {
		// Split at OPTIONAL MATCH and only check the main part
		parts := optionalMatchPattern.Split(query, 2)
		mainQuery = parts[0]
	} else {
		mainQuery = query
	}

	// Extract all aliases from the main query
	aliases := extractAliases(mainQuery)
	if len(aliases) == 0 {
		// No node patterns found (e.g., RETURN 1), allow it
		return nil
	}

	// Check if all aliases in the main query are constrained by team_id
	if !allAliasesHaveProfilePredicate(mainQuery, aliases) {
		return &ValidationError{Reason: "all node aliases must be constrained by team_id predicate"}
	}

	return nil
}

// extractAliases extracts all node aliases from a Cypher query.
// Returns the list of aliases found in node patterns.
func extractAliases(query string) []string {
	// Match node patterns: (alias:Label) or (alias) or (alias {...})
	// Pattern captures: (alias optional :Label optional {props})
	nodePatternWithAlias := regexp.MustCompile(`\(\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*(?::\s*[a-zA-Z_][a-zA-Z0-9_]*)?(?:\s*\{[^}]*\})?\s*\)`)

	matches := nodePatternWithAlias.FindAllStringSubmatch(query, -1)
	aliases := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) > 1 && match[1] != "" {
			aliases = append(aliases, match[1])
		}
	}
	return aliases
}

// hasAnonymousNodePattern checks if the query contains anonymous node patterns (nodes without aliases).
// Matches patterns like: () or (:Label) but NOT (n) or (n:Label)
func hasAnonymousNodePattern(query string) bool {
	// Match empty parentheses: () or with just a label: (:Label)
	anonymousPattern := regexp.MustCompile(`\(\s*:([^)]*)\)|\(\s*\)`)
	return anonymousPattern.MatchString(query)
}

// allAliasesHaveProfilePredicate checks if all aliases have team_id constraints.
// Valid constraints:
// - inline: {team_id: $profileId}
// - WHERE clause: alias.team_id = $profileId
func allAliasesHaveProfilePredicate(query string, aliases []string) bool {
	// Check for WHERE clause with team_id that applies to any alias
	whereHasProfileID := regexp.MustCompile(`(?i)\bWHERE\b.*team_id\s*=\s*\$profileId`).MatchString(query)

	for _, alias := range aliases {
		// Check inline: {team_id: $profileId} for this specific alias
		inlinePattern := regexp.MustCompile(fmt.Sprintf(`(?i)\(\s*%s\s*(?::\s*[a-zA-Z_][a-zA-Z0-9_]*)?\s*\{[^}]*team_id\s*:\s*\$profileId[^}]*\}`, regexp.QuoteMeta(alias)))
		if inlinePattern.MatchString(query) {
			continue // This alias has inline team_id
		}

		// Check WHERE clause for this specific alias
		wherePattern := regexp.MustCompile(fmt.Sprintf(`(?i)\bWHERE\b.*\b%s\.team_id\s*=\s*\$profileId`, regexp.QuoteMeta(alias)))
		if wherePattern.MatchString(query) {
			continue // This alias has WHERE team_id
		}

		// If WHERE clause has team_id but not specific to this alias, still accept
		// (This handles cases like: WHERE n.team_id = $profileId AND m.team_id = $profileId)
		if whereHasProfileID {
			// Check if this specific alias is mentioned with team_id in WHERE
			aliasProfileInWhere := regexp.MustCompile(fmt.Sprintf(`(?i)\b%s\.team_id\s*=\s*\$profileId`, regexp.QuoteMeta(alias))).MatchString(query)
			if aliasProfileInWhere {
				continue
			}
		}

		// This alias doesn't have team_id constraint
		return false
	}

	return true
}
