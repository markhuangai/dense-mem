package neo4j

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// CrossProfileRelationshipError indicates an attempt to create a relationship
// between nodes that belong to different profiles.
type CrossProfileRelationshipError struct {
	FromProfileID string
	ToProfileID   string
}

func (e *CrossProfileRelationshipError) Error() string {
	return fmt.Sprintf("cannot create relationship between nodes of different profiles: from=%s, to=%s", e.FromProfileID, e.ToProfileID)
}

// Is implements errors.Is interface for proper error comparison.
func (e *CrossProfileRelationshipError) Is(target error) bool {
	_, ok := target.(*CrossProfileRelationshipError)
	return ok
}

// NodeWriter defines the interface for profile-scoped node creation.
type NodeWriter interface {
	CreateNode(ctx context.Context, profileID string, label string, props map[string]any) (string, error)
}

// RelationshipWriter defines the interface for profile-scoped relationship creation.
type RelationshipWriter interface {
	CreateRelationship(ctx context.Context, profileID string, fromLabel string, fromID string, toLabel string, toID string, relType string, props map[string]any) error
}

// GraphWriter composes NodeWriter and RelationshipWriter for profile-scoped graph operations.
type GraphWriter interface {
	NodeWriter
	RelationshipWriter
}

// graphWriter implements GraphWriter with profile-scoped operations.
type graphWriter struct {
	enforcer ProfileScopeEnforcer
}

// Ensure graphWriter implements GraphWriter
var _ GraphWriter = (*graphWriter)(nil)

// NewGraphWriter creates a new GraphWriter with the given profile scope enforcer.
func NewGraphWriter(enforcer ProfileScopeEnforcer) GraphWriter {
	return &graphWriter{
		enforcer: enforcer,
	}
}

// CreateNode creates a new node with the given label and properties.
// It automatically injects the team_id into the node properties.
// Returns the generated node ID on success.
// All writes are parameterized to prevent Cypher injection.
func (w *graphWriter) CreateNode(ctx context.Context, profileID string, label string, props map[string]any) (string, error) {
	// Generate a unique node ID
	nodeID := uuid.New().String()

	// Create a copy of props to avoid mutating the caller's map
	nodeProps := make(map[string]any, len(props)+2)
	for k, v := range props {
		nodeProps[k] = v
	}

	// Inject team_id and node_id into properties
	nodeProps["team_id"] = profileID
	nodeProps["id"] = nodeID

	// Build parameterized Cypher query
	// The $profileId placeholder is required by ScopedWrite for validation
	query := fmt.Sprintf(
		"CREATE (n:%s $props) SET n.team_id = $profileId RETURN n.id",
		label,
	)

	// Execute through ScopedWrite to ensure profile scoping
	_, err := w.enforcer.ScopedWrite(ctx, profileID, query, map[string]any{
		"props": nodeProps,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create node: %w", err)
	}

	return nodeID, nil
}

// CreateRelationship creates a relationship between two nodes.
// It validates that both endpoint nodes belong to the same profile before creating.
// The relationship itself also stores the team_id.
// Returns CrossProfileRelationshipError if endpoints belong to different profiles.
// All queries are parameterized to prevent Cypher injection.
func (w *graphWriter) CreateRelationship(ctx context.Context, profileID string, fromLabel string, fromID string, toLabel string, toID string, relType string, props map[string]any) error {
	// First, validate that both endpoints exist and belong to the same profile
	// We need to check both nodes in a single query to avoid race conditions
	// and ensure atomicity of the validation
	checkQuery := `
		MATCH (from:%s {id: $fromId}) 
		WHERE from.team_id = $profileId
		MATCH (to:%s {id: $toId}) 
		WHERE to.team_id = $profileId
		RETURN from.team_id AS fromProfileId, to.team_id AS toProfileId
	`
	checkQuery = fmt.Sprintf(checkQuery, fromLabel, toLabel)

	_, results, err := w.enforcer.ScopedRead(ctx, profileID, checkQuery, map[string]any{
		"fromId": fromID,
		"toId":   toID,
	})
	if err != nil {
		return fmt.Errorf("failed to validate relationship endpoints: %w", err)
	}

	// If no results, either nodes don't exist or don't belong to this profile
	if len(results) == 0 {
		// Check if it's a cross-profile issue by checking each node individually
		// This is a best-effort check to provide a more specific error
		fromProfileID, fromExists := w.getNodeProfileID(ctx, profileID, fromLabel, fromID)
		toProfileID, toExists := w.getNodeProfileID(ctx, profileID, toLabel, toID)

		// If one or both nodes don't exist within the profile scope, that's a different error
		if !fromExists || !toExists {
			return fmt.Errorf("one or both endpoint nodes do not exist: from=%s (%v), to=%s (%v)",
				fromID, fromExists, toID, toExists)
		}

		// Both nodes exist but in different profiles - return cross-profile error
		return &CrossProfileRelationshipError{
			FromProfileID: fromProfileID,
			ToProfileID:   toProfileID,
		}
	}

	// Create a copy of props to avoid mutating the caller's map
	relProps := make(map[string]any, len(props)+1)
	for k, v := range props {
		relProps[k] = v
	}
	// Inject team_id into relationship properties
	relProps["team_id"] = profileID

	// Build parameterized Cypher query for relationship creation
	// The $profileId placeholder is required by ScopedWrite for validation
	createQuery := `
		MATCH (from:%s {id: $fromId})
		WHERE from.team_id = $profileId
		MATCH (to:%s {id: $toId})
		WHERE to.team_id = $profileId
		CREATE (from)-[r:%s $props]->(to)
		RETURN type(r)
	`
	createQuery = fmt.Sprintf(createQuery, fromLabel, toLabel, relType)

	_, err = w.enforcer.ScopedWrite(ctx, profileID, createQuery, map[string]any{
		"fromId": fromID,
		"toId":   toID,
		"props":  relProps,
	})
	if err != nil {
		return fmt.Errorf("failed to create relationship: %w", err)
	}

	return nil
}

// getNodeProfileID retrieves the team_id of a node.
// This is used for cross-profile validation.
// Returns the team_id and whether the node exists.
// Note: This method is internal and uses the caller's profileID for scoping,
// but returns the actual team_id of the node if it exists.
func (w *graphWriter) getNodeProfileID(ctx context.Context, profileID string, label string, nodeID string) (string, bool) {
	// Use a query through the enforcer
	// Note: We need $profileId placeholder for ScopedRead to work
	// So we include it in a WHERE clause that's always true
	queryWithPlaceholder := fmt.Sprintf(
		"MATCH (n:%s {id: $nodeId}) WHERE $profileId = $profileId RETURN n.team_id AS profileId LIMIT 1",
		label,
	)

	_, results, err := w.enforcer.ScopedRead(ctx, profileID, queryWithPlaceholder, map[string]any{
		"nodeId": nodeID,
	})
	if err != nil {
		return "", false
	}

	if len(results) == 0 {
		return "", false
	}

	profileIdVal, ok := results[0]["profileId"]
	if !ok {
		return "", false
	}

	profileIdStr, ok := profileIdVal.(string)
	if !ok {
		return "", false
	}

	return profileIdStr, true
}
