//go:build integration

package neo4j

import (
	"context"
	"errors"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateNodeInjectsProfileId tests that CreateNode automatically injects
// the team_id into node properties.
func TestCreateNodeInjectsProfileId(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Clean up any existing test data
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, _ = tx.Run(ctx, "MATCH (n:TestNode) DELETE n", nil)
		return nil, nil
	})

	enforcer := NewProfileScopeEnforcer(client)
	writer := NewGraphWriter(enforcer)

	profileID := "test-profile-123"
	label := "TestNode"
	props := map[string]any{
		"name":  "TestName",
		"value": 42,
	}

	// Create the node
	nodeID, err := writer.CreateNode(ctx, profileID, label, props)
	require.NoError(t, err, "CreateNode should succeed")
	require.NotEmpty(t, nodeID, "CreateNode should return a non-empty node ID")

	// Verify the node was created with team_id injected
	result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx,
			"MATCH (n:TestNode {id: $nodeId}) RETURN n.id, n.team_id, n.name, n.value",
			map[string]any{"nodeId": nodeID},
		)
		if err != nil {
			return nil, err
		}
		if res.Next(ctx) {
			return map[string]any{
				"id":      res.Record().Values[0],
				"team_id": res.Record().Values[1],
				"name":    res.Record().Values[2],
				"value":   res.Record().Values[3],
			}, nil
		}
		return nil, errors.New("node not found")
	})
	require.NoError(t, err, "should find the created node")

	nodeData := result.(map[string]any)
	assert.Equal(t, nodeID, nodeData["id"], "node ID should match")
	assert.Equal(t, profileID, nodeData["team_id"], "team_id should be injected")
	assert.Equal(t, "TestName", nodeData["name"], "name property should be preserved")
	assert.Equal(t, int64(42), nodeData["value"], "value property should be preserved")

	// Cleanup
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, _ = tx.Run(ctx, "MATCH (n:TestNode) DELETE n", nil)
		return nil, nil
	})
}

// TestCreateRelationshipValidatesSameProfile tests that CreateRelationship
// creates relationships between nodes of the same profile.
func TestCreateRelationshipValidatesSameProfile(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Clean up any existing test data
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, _ = tx.Run(ctx, "MATCH (n:Person) DETACH DELETE n", nil)
		return nil, nil
	})

	enforcer := NewProfileScopeEnforcer(client)
	writer := NewGraphWriter(enforcer)

	profileID := "test-profile-456"

	// Create two nodes in the same profile
	fromID, err := writer.CreateNode(ctx, profileID, "Person", map[string]any{"name": "Alice"})
	require.NoError(t, err, "CreateNode should succeed for from node")

	toID, err := writer.CreateNode(ctx, profileID, "Person", map[string]any{"name": "Bob"})
	require.NoError(t, err, "CreateNode should succeed for to node")

	// Create relationship between nodes of same profile
	relProps := map[string]any{
		"since": 2020,
	}
	err = writer.CreateRelationship(ctx, profileID, "Person", fromID, "Person", toID, "KNOWS", relProps)
	require.NoError(t, err, "CreateRelationship should succeed for same-profile nodes")

	// Verify the relationship was created with team_id
	result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx,
			"MATCH (from:Person {id: $fromId})-[r:KNOWS]->(to:Person {id: $toId}) RETURN r.team_id, r.since",
			map[string]any{"fromId": fromID, "toId": toID},
		)
		if err != nil {
			return nil, err
		}
		if res.Next(ctx) {
			return map[string]any{
				"team_id": res.Record().Values[0],
				"since":   res.Record().Values[1],
			}, nil
		}
		return nil, errors.New("relationship not found")
	})
	require.NoError(t, err, "should find the created relationship")

	relData := result.(map[string]any)
	assert.Equal(t, profileID, relData["team_id"], "relationship should have team_id")
	assert.Equal(t, int64(2020), relData["since"], "relationship property should be preserved")

	// Cleanup
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, _ = tx.Run(ctx, "MATCH (n:Person) DETACH DELETE n", nil)
		return nil, nil
	})
}

// TestRejectCrossProfileRelationship tests that CreateRelationship rejects
// attempts to create relationships between nodes of different profiles.
func TestRejectCrossProfileRelationship(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Clean up any existing test data
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, _ = tx.Run(ctx, "MATCH (n:User) DETACH DELETE n", nil)
		return nil, nil
	})

	enforcer := NewProfileScopeEnforcer(client)
	writer := NewGraphWriter(enforcer)

	// Create nodes in different profiles
	profile1 := "profile-alpha"
	profile2 := "profile-beta"

	node1ID, err := writer.CreateNode(ctx, profile1, "User", map[string]any{"name": "User1"})
	require.NoError(t, err, "CreateNode should succeed for node1")

	node2ID, err := writer.CreateNode(ctx, profile2, "User", map[string]any{"name": "User2"})
	require.NoError(t, err, "CreateNode should succeed for node2")

	// Attempt to create a cross-profile relationship
	// Use profile1 as the actor, trying to connect to a node in profile2
	err = writer.CreateRelationship(ctx, profile1, "User", node1ID, "User", node2ID, "FRIENDS_WITH", nil)

	// Should return CrossProfileRelationshipError
	require.Error(t, err, "CreateRelationship should reject cross-profile relationship")

	var crossProfileErr *CrossProfileRelationshipError
	require.True(t, errors.As(err, &crossProfileErr),
		"error should be CrossProfileRelationshipError, got: %T - %v", err, err)

	assert.Equal(t, profile1, crossProfileErr.FromProfileID,
		"FromProfileID should match profile1")
	assert.Equal(t, profile2, crossProfileErr.ToProfileID,
		"ToProfileID should match profile2")

	// Also verify with errors.Is
	require.True(t, errors.Is(err, &CrossProfileRelationshipError{}),
		"error should match CrossProfileRelationshipError with errors.Is")

	// Verify no relationship was created
	result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx,
			"MATCH (from:User {id: $fromId})-[r]->(to:User {id: $toId}) RETURN count(r)",
			map[string]any{"fromId": node1ID, "toId": node2ID},
		)
		if err != nil {
			return nil, err
		}
		if res.Next(ctx) {
			return res.Record().Values[0], nil
		}
		return int64(0), nil
	})
	require.NoError(t, err, "should be able to query for relationships")
	assert.Equal(t, int64(0), result, "no relationship should exist between cross-profile nodes")

	// Cleanup
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, _ = tx.Run(ctx, "MATCH (n:User) DETACH DELETE n", nil)
		return nil, nil
	})
}
