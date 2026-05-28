//go:build integration

package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	neo4jclient "github.com/markhuangai/dense-mem/internal/storage/neo4j"
	postgresstorage "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// testNeo4jConfig implements neo4j.ConfigProvider for testing
type testNeo4jConfig struct {
	uri      string
	user     string
	password string
	database string
}

func (c *testNeo4jConfig) GetNeo4jURI() string      { return c.uri }
func (c *testNeo4jConfig) GetNeo4jUser() string     { return c.user }
func (c *testNeo4jConfig) GetNeo4jPassword() string { return c.password }
func (c *testNeo4jConfig) GetNeo4jDatabase() string { return c.database }

// testPostgresConfig implements postgresstorage.ConfigProvider for testing
type testPostgresConfig struct {
	dsn string
}

func (c *testPostgresConfig) GetPostgresDSN() string { return c.dsn }

// getNeo4jTestConfig returns Neo4j config for testing
func getNeo4jTestConfig(t *testing.T, ctx context.Context) (*testNeo4jConfig, func()) {
	uri := os.Getenv("NEO4J_URI")
	if uri == "" {
		t.Skip("NEO4J_URI not set, skipping integration test")
	}

	cfg := &testNeo4jConfig{
		uri:      uri,
		user:     getEnvOrDefault("NEO4J_USER", "neo4j"),
		password: getEnvOrDefault("NEO4J_PASSWORD", "password"),
		database: getEnvOrDefault("NEO4J_DATABASE", "neo4j"),
	}

	// Verify connectivity
	client, err := neo4jclient.NewClient(ctx, cfg)
	if err != nil {
		t.Skipf("Could not connect to Neo4j: %v", err)
	}
	_ = client.Close(ctx)

	return cfg, func() {}
}

// getPostgresTestConfig returns PostgreSQL config for testing
func getPostgresTestConfig(t *testing.T, ctx context.Context) (*testPostgresConfig, func()) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set, skipping integration test")
	}

	cfg := &testPostgresConfig{dsn: dsn}

	// Test connection
	db, err := postgresstorage.Open(ctx, cfg)
	if err != nil {
		t.Skipf("Could not connect to PostgreSQL: %v", err)
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()

	return cfg, func() {}
}

// getEnvOrDefault returns environment variable value or default
func getEnvOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// TestInvariantScanClean verifies that a clean graph returns violations=0, status="clean".
func TestInvariantScanClean(t *testing.T) {
	ctx := context.Background()

	// Get Neo4j config
	neo4jCfg, cleanupNeo4j := getNeo4jTestConfig(t, ctx)
	defer cleanupNeo4j()

	// Create Neo4j client
	client, err := neo4jclient.NewClient(ctx, neo4jCfg)
	require.NoError(t, err, "Neo4j client should be created")
	defer client.Close(ctx)

	// Clean test data before scan
	_, err = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		// Delete all test nodes and relationships
		_, err := tx.Run(ctx, "MATCH (n) WHERE n.team_id STARTS WITH 'test-' DETACH DELETE n", nil)
		return nil, err
	})
	require.NoError(t, err, "Test data cleanup should succeed")

	// Create audit service (PostgreSQL)
	pgCfg, cleanupPg := getPostgresTestConfig(t, ctx)
	defer cleanupPg()

	db, err := postgresstorage.Open(ctx, pgCfg)
	require.NoError(t, err, "PostgreSQL connection should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}()

	// Run migrations
	m, err := postgresstorage.NewMigrator(db)
	require.NoError(t, err, "Migrator should be created")
	err = m.RunUp(ctx)
	require.NoError(t, err, "Migrations should succeed")

	auditSvc := NewAuditService(db)

	// Create invariant scan service
	invariantSvc := NewInvariantScanService(client, auditSvc)

	// Execute scan (without audit for this test)
	result, err := invariantSvc.Scan(ctx)

	require.NoError(t, err, "Scan should succeed")
	assert.NotNil(t, result, "Result should not be nil")
	assert.Equal(t, 0, result.Violations, "Clean graph should have 0 violations")
	assert.Equal(t, "clean", result.Status, "Clean graph should have status 'clean'")
	assert.Nil(t, result.Findings, "Clean graph should have no findings")
}

// TestInvariantScanDetectsCrossProfile verifies that injected cross-profile edge is detected.
func TestInvariantScanDetectsCrossProfile(t *testing.T) {
	ctx := context.Background()

	// Get Neo4j config
	neo4jCfg, cleanupNeo4j := getNeo4jTestConfig(t, ctx)
	defer cleanupNeo4j()

	// Create Neo4j client
	client, err := neo4jclient.NewClient(ctx, neo4jCfg)
	require.NoError(t, err, "Neo4j client should be created")
	defer client.Close(ctx)

	// Clean test data
	_, err = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, "MATCH (n) WHERE n.team_id STARTS WITH 'test-' DETACH DELETE n", nil)
		return nil, err
	})
	require.NoError(t, err, "Test data cleanup should succeed")

	// Create nodes in different profiles
	profile1 := fmt.Sprintf("test-profile-%s", uuid.New().String())
	profile2 := fmt.Sprintf("test-profile-%s", uuid.New().String())
	node1ID := fmt.Sprintf("test-node-%s", uuid.New().String())
	node2ID := fmt.Sprintf("test-node-%s", uuid.New().String())

	// Create node in profile 1
	_, err = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx,
			"CREATE (n:Memory {id: $nodeId, team_id: $profileId, content: 'test content'})",
			map[string]any{"nodeId": node1ID, "profileId": profile1},
		)
		return nil, err
	})
	require.NoError(t, err, "Node 1 should be created")

	// Create node in profile 2
	_, err = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx,
			"CREATE (n:Memory {id: $nodeId, team_id: $profileId, content: 'test content'})",
			map[string]any{"nodeId": node2ID, "profileId": profile2},
		)
		return nil, err
	})
	require.NoError(t, err, "Node 2 should be created")

	// Create cross-profile relationship (this should NOT happen in normal operation)
	// We inject it directly to test the invariant scan
	_, err = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx,
			"MATCH (a:Memory {id: $fromId}) MATCH (b:Memory {id: $toId}) CREATE (a)-[r:RELATED]->(b)",
			map[string]any{"fromId": node1ID, "toId": node2ID},
		)
		return nil, err
	})
	require.NoError(t, err, "Cross-profile relationship should be created for test")

	// Create audit service (PostgreSQL)
	pgCfg, cleanupPg := getPostgresTestConfig(t, ctx)
	defer cleanupPg()

	db, err := postgresstorage.Open(ctx, pgCfg)
	require.NoError(t, err, "PostgreSQL connection should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}()

	// Run migrations
	m, err := postgresstorage.NewMigrator(db)
	require.NoError(t, err, "Migrator should be created")
	err = m.RunUp(ctx)
	require.NoError(t, err, "Migrations should succeed")

	auditSvc := NewAuditService(db)

	// Create invariant scan service
	invariantSvc := NewInvariantScanService(client, auditSvc)

	// Execute scan
	result, err := invariantSvc.Scan(ctx)

	require.NoError(t, err, "Scan should succeed")
	assert.NotNil(t, result, "Result should not be nil")
	assert.Greater(t, result.Violations, 0, "Should detect at least one violation")
	assert.Equal(t, "violations_found", result.Status, "Status should be 'violations_found'")
	assert.NotEmpty(t, result.Findings, "Should have findings")

	// Check that the finding matches our injected violation
	foundViolation := false
	for _, f := range result.Findings {
		if f.FromProfileID == profile1 && f.ToProfileID == profile2 && f.RelType == "RELATED" {
			foundViolation = true
			break
		}
	}
	assert.True(t, foundViolation, "Should find the injected cross-profile violation")

	// Clean up test data
	_, err = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, "MATCH (n) WHERE n.team_id STARTS WITH 'test-' DETACH DELETE n", nil)
		return nil, err
	})
	require.NoError(t, err, "Test data cleanup should succeed")
}

// TestInvariantScanAuditLog verifies that an audit event is recorded for every scan execution.
func TestInvariantScanAuditLog(t *testing.T) {
	ctx := context.Background()

	// Get Neo4j config
	neo4jCfg, cleanupNeo4j := getNeo4jTestConfig(t, ctx)
	defer cleanupNeo4j()

	// Create Neo4j client
	client, err := neo4jclient.NewClient(ctx, neo4jCfg)
	require.NoError(t, err, "Neo4j client should be created")
	defer client.Close(ctx)

	// Clean test data
	_, err = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, "MATCH (n) WHERE n.team_id STARTS WITH 'test-' DETACH DELETE n", nil)
		return nil, err
	})
	require.NoError(t, err, "Test data cleanup should succeed")

	// Create audit service (PostgreSQL)
	pgCfg, cleanupPg := getPostgresTestConfig(t, ctx)
	defer cleanupPg()

	db, err := postgresstorage.Open(ctx, pgCfg)
	require.NoError(t, err, "PostgreSQL connection should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}()

	// Run migrations
	m, err := postgresstorage.NewMigrator(db)
	require.NoError(t, err, "Migrator should be created")
	err = m.RunUp(ctx)
	require.NoError(t, err, "Migrations should succeed")

	// Clean audit log for test
	sqlDB, _ := db.DB()
	_, _ = sqlDB.Exec("DELETE FROM audit_log WHERE operation = 'SYSTEM_QUERY' AND metadata::text LIKE '%invariant_scan%'")

	auditSvc := NewAuditService(db)

	// Create invariant scan service
	invariantSvc := NewInvariantScanService(client, auditSvc)

	// Execute scan with audit
	actorKeyID := fmt.Sprintf("test-key-%s", uuid.New().String())
	correlationID := fmt.Sprintf("test-correlation-%s", uuid.New().String())
	result, err := invariantSvc.ScanWithAudit(ctx, &actorKeyID, "system", "127.0.0.1", correlationID)

	require.NoError(t, err, "Scan should succeed")
	assert.NotNil(t, result, "Result should not be nil")

	// Verify audit log entry was created
	// Wait a moment for async audit logging (if implemented as fire-and-forget)
	time.Sleep(100 * time.Millisecond)

	var auditCount int
	err = sqlDB.QueryRow(`
		SELECT COUNT(*) FROM audit_log 
		WHERE operation = 'SYSTEM_QUERY' 
		AND entity_id = 'invariant_scan'
		AND actor_key_id = $1
		AND correlation_id = $2
	`, actorKeyID, correlationID).Scan(&auditCount)

	require.NoError(t, err, "Audit log query should succeed")
	assert.GreaterOrEqual(t, auditCount, 1, "Audit event should be recorded for scan execution")

	// Verify audit metadata contains expected fields
	var metadataJSON []byte
	err = sqlDB.QueryRow(`
		SELECT metadata FROM audit_log 
		WHERE operation = 'SYSTEM_QUERY' 
		AND entity_id = 'invariant_scan'
		AND actor_key_id = $1
		AND correlation_id = $2
		LIMIT 1
	`, actorKeyID, correlationID).Scan(&metadataJSON)

	require.NoError(t, err, "Audit metadata query should succeed")
	assert.NotEmpty(t, metadataJSON, "Audit metadata should not be empty")

	// Check that metadata contains status and violations count
	assert.Contains(t, string(metadataJSON), "violations", "Metadata should contain violations count")
	assert.Contains(t, string(metadataJSON), "status", "Metadata should contain status")

	// Clean up test data
	_, err = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, "MATCH (n) WHERE n.team_id STARTS WITH 'test-' DETACH DELETE n", nil)
		return nil, err
	})
	require.NoError(t, err, "Test data cleanup should succeed")

	_, _ = sqlDB.Exec("DELETE FROM audit_log WHERE actor_key_id = $1", actorKeyID)
}
