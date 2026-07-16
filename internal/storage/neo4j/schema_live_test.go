package neo4j

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	neo4jcontainer "github.com/testcontainers/testcontainers-go/modules/neo4j"
	"github.com/testcontainers/testcontainers-go/wait"
)

type schemaTestConfig struct {
	uri      string
	user     string
	password string
	database string
}

func (c *schemaTestConfig) GetNeo4jURI() string      { return c.uri }
func (c *schemaTestConfig) GetNeo4jUser() string     { return c.user }
func (c *schemaTestConfig) GetNeo4jPassword() string { return c.password }
func (c *schemaTestConfig) GetNeo4jDatabase() string { return c.database }

// getSchemaTestConfig returns the config to use for schema testing.
func getSchemaTestConfig(ctx context.Context) (*schemaTestConfig, func(), error) {
	// Check for existing Neo4j environment variables
	if uri := os.Getenv("NEO4J_URI"); uri != "" {
		cfg := &schemaTestConfig{
			uri:      uri,
			user:     os.Getenv("NEO4J_USER"),
			password: os.Getenv("NEO4J_PASSWORD"),
			database: os.Getenv("NEO4J_DATABASE"),
		}
		if cfg.user == "" {
			cfg.user = "neo4j"
		}
		if cfg.password == "" {
			cfg.password = "password"
		}
		if cfg.database == "" {
			cfg.database = "neo4j"
		}
		return cfg, func() {}, nil
	}

	if os.Getenv("DENSE_MEM_TEST_NEO4J") != "1" {
		return nil, nil, fmt.Errorf("set DENSE_MEM_TEST_NEO4J=1 to run legacy Neo4j testcontainer tests")
	}

	// Start a test container
	container, err := neo4jcontainer.Run(ctx,
		"neo4j:5-community",
		neo4jcontainer.WithAdminPassword("testpassword"),
		neo4jcontainer.WithLabsPlugin(neo4jcontainer.Apoc),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Started").
				WithOccurrence(1).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start neo4j container: %w", err)
	}

	uri, err := container.BoltUrl(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, nil, err
	}

	cleanup := func() {
		_ = container.Terminate(ctx)
	}

	cfg := &schemaTestConfig{
		uri:      uri,
		user:     "neo4j",
		password: "testpassword",
		database: "neo4j",
	}

	return cfg, cleanup, nil
}

// skipSchemaTestIfNoNeo4j skips the test if Neo4j is not available.
func skipSchemaTestIfNoNeo4j(t *testing.T, ctx context.Context) (*schemaTestConfig, func()) {
	cfg, cleanup, err := getSchemaTestConfig(ctx)
	if err != nil {
		t.Skipf("Neo4j not available: %v", err)
	}
	return cfg, cleanup
}

// TestEnsureSchema_CreatesConstraints tests that unique constraints are created.
func TestEnsureSchema_CreatesConstraints(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipSchemaTestIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Clean up any existing constraints first
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		_, _ = tx.Run(ctx, "DROP CONSTRAINT sourcefragment_fragment_id_unique IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP CONSTRAINT claim_claim_id_unique IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP CONSTRAINT fact_fact_id_unique IF EXISTS", nil)
		return nil, nil
	})

	// Create the bootstrapper
	logger := observability.New(slog.LevelDebug)
	bootstrapper := NewSchemaBootstrapper(client, 1536, logger)

	// Ensure schema
	err = bootstrapper.EnsureSchema(ctx)
	require.NoError(t, err, "EnsureSchema should succeed")

	// Verify constraints exist
	constraints := []string{
		"sourcefragment_fragment_id_unique",
		"claim_claim_id_unique",
		"fact_fact_id_unique",
	}

	for _, constraintName := range constraints {
		result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				"SHOW CONSTRAINTS WHERE name = $name",
				map[string]interface{}{"name": constraintName},
			)
			if err != nil {
				return nil, err
			}
			if res.Next(ctx) {
				return res.Record().Values[0], nil
			}
			return nil, nil
		})
		require.NoError(t, err, "Should be able to query constraints")
		assert.NotNil(t, result, "Constraint %s should exist", constraintName)
	}
}

// TestEnsureSchema_CreatesProfileIdIndexes tests that team_id indexes are created.
func TestEnsureSchema_CreatesProfileIdIndexes(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipSchemaTestIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Clean up any existing indexes first
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		_, _ = tx.Run(ctx, "DROP INDEX sourcefragment_team_id_idx IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP INDEX claim_team_id_idx IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP INDEX fact_team_id_idx IF EXISTS", nil)
		return nil, nil
	})

	// Create the bootstrapper
	logger := observability.New(slog.LevelDebug)
	bootstrapper := NewSchemaBootstrapper(client, 1536, logger)

	// Ensure schema
	err = bootstrapper.EnsureSchema(ctx)
	require.NoError(t, err, "EnsureSchema should succeed")

	// Verify indexes exist
	indexes := []string{
		"sourcefragment_team_id_idx",
		"claim_team_id_idx",
		"fact_team_id_idx",
	}

	for _, indexName := range indexes {
		result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				"SHOW INDEXES WHERE name = $name",
				map[string]interface{}{"name": indexName},
			)
			if err != nil {
				return nil, err
			}
			if res.Next(ctx) {
				return res.Record().Values[0], nil
			}
			return nil, nil
		})
		require.NoError(t, err, "Should be able to query indexes")
		assert.NotNil(t, result, "Index %s should exist", indexName)
	}
}

// TestEnsureSchema_CreatesFullTextIndexes tests that full-text indexes are created with canonical names.
func TestEnsureSchema_CreatesFullTextIndexes(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipSchemaTestIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Clean up any existing full-text indexes (both legacy and canonical)
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		_, _ = tx.Run(ctx, "DROP INDEX sourcefragment_content IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP INDEX fragment_content_idx IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP INDEX fact_predicate IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP INDEX fact_predicate_idx IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP INDEX fact_recall_idx IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP INDEX claim_recall_idx IF EXISTS", nil)
		return nil, nil
	})

	// Create the bootstrapper
	logger := observability.New(slog.LevelDebug)
	bootstrapper := NewSchemaBootstrapper(client, 1536, logger)

	// Ensure schema
	err = bootstrapper.EnsureSchema(ctx)
	require.NoError(t, err, "EnsureSchema should succeed")

	// Verify full-text indexes exist with canonical names
	ftIndexes := []string{
		"fragment_content_idx",
		"fact_predicate_idx",
		"fact_recall_idx",
		"claim_recall_idx",
	}

	for _, indexName := range ftIndexes {
		result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				"SHOW INDEXES WHERE name = $name AND type = 'FULLTEXT'",
				map[string]interface{}{"name": indexName},
			)
			if err != nil {
				return nil, err
			}
			if res.Next(ctx) {
				return res.Record().Values[0], nil
			}
			return nil, nil
		})
		require.NoError(t, err, "Should be able to query full-text indexes")
		assert.NotNil(t, result, "Full-text index %s should exist", indexName)
	}
}

// TestEnsureSchema_CreatesVectorIndex tests that the vector index is created with canonical name.
func TestEnsureSchema_CreatesVectorIndex(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipSchemaTestIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Clean up any existing vector index (both legacy and canonical)
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		_, _ = tx.Run(ctx, "DROP INDEX sourcefragment_embedding IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP INDEX fragment_embedding_idx IF EXISTS", nil)
		return nil, nil
	})

	// Create the bootstrapper with 1536 dimensions
	logger := observability.New(slog.LevelDebug)
	bootstrapper := NewSchemaBootstrapper(client, 1536, logger)

	// Ensure schema
	err = bootstrapper.EnsureSchema(ctx)
	require.NoError(t, err, "EnsureSchema should succeed")

	// Verify vector index exists with canonical name
	result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		res, err := tx.Run(ctx,
			"SHOW INDEXES WHERE name = 'fragment_embedding_idx' AND type = 'VECTOR'",
			nil,
		)
		if err != nil {
			return nil, err
		}
		if res.Next(ctx) {
			return res.Record().Values[0], nil
		}
		return nil, nil
	})
	require.NoError(t, err, "Should be able to query vector index")
	assert.NotNil(t, result, "Vector index fragment_embedding_idx should exist")
}

// TestEnsureSchema_Idempotent tests that EnsureSchema can be run multiple times without error.
func TestEnsureSchema_Idempotent(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipSchemaTestIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Create the bootstrapper
	logger := observability.New(slog.LevelDebug)
	bootstrapper := NewSchemaBootstrapper(client, 1536, logger)

	// Run EnsureSchema multiple times
	for i := 0; i < 3; i++ {
		err = bootstrapper.EnsureSchema(ctx)
		require.NoError(t, err, "EnsureSchema should be idempotent - run %d should succeed", i+1)
	}

	// Verify constraints still exist after multiple runs
	constraints := []string{
		"sourcefragment_fragment_id_unique",
		"claim_claim_id_unique",
		"fact_fact_id_unique",
	}

	for _, constraintName := range constraints {
		result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				"SHOW CONSTRAINTS WHERE name = $name",
				map[string]interface{}{"name": constraintName},
			)
			if err != nil {
				return nil, err
			}
			if res.Next(ctx) {
				return res.Record().Values[0], nil
			}
			return nil, nil
		})
		require.NoError(t, err, "Should be able to query constraints")
		assert.NotNil(t, result, "Constraint %s should still exist after multiple runs", constraintName)
	}
}

// TestSchemaBootstrapper_Interface ensures SchemaBootstrapper implements SchemaBootstrapperInterface.
func TestSchemaBootstrapper_Interface(t *testing.T) {
	var _ SchemaBootstrapperInterface = (*SchemaBootstrapper)(nil)
}

// TestEnsureSchema_FragmentDedupeIndexes tests that composite indexes for fragment deduplication are created.
// AC-44: Idempotency-key uniqueness and indexing — dedupe scoped to (team_id, idempotency_key).
// AC-45: Content-hash lookup indexing strategy — profile-scoped lookup by content hash is efficient.
// AC-29: Created-at ordering index for list ordering.
func TestEnsureSchema_FragmentDedupeIndexes(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipSchemaTestIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Clean up any existing composite indexes first
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		_, _ = tx.Run(ctx, "DROP INDEX fragment_team_idempotency_idx IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP INDEX fragment_profile_content_hash_idx IF EXISTS", nil)
		_, _ = tx.Run(ctx, "DROP INDEX fragment_profile_created_at_idx IF EXISTS", nil)
		return nil, nil
	})

	// Create the bootstrapper
	logger := observability.New(slog.LevelDebug)
	bootstrapper := NewSchemaBootstrapper(client, 1536, logger)

	// Ensure schema
	err = bootstrapper.EnsureSchema(ctx)
	require.NoError(t, err, "EnsureSchema should succeed")

	// Verify composite indexes exist
	compositeIndexes := []string{
		"fragment_team_idempotency_idx",
		"fragment_profile_content_hash_idx",
		"fragment_profile_created_at_idx",
	}

	for _, indexName := range compositeIndexes {
		result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				"SHOW INDEXES WHERE name = $name",
				map[string]interface{}{"name": indexName},
			)
			if err != nil {
				return nil, err
			}
			if res.Next(ctx) {
				return res.Record().Values[0], nil
			}
			return nil, nil
		})
		require.NoError(t, err, "Should be able to query indexes")
		assert.NotNil(t, result, "Composite index %s should exist", indexName)
	}
}

// TestEnsureSchema_FragmentDedupeIndexes_Idempotent tests that composite index creation is idempotent.
// AC-48: Migration idempotency and data preservation — migration safe to rerun.
func TestEnsureSchema_FragmentDedupeIndexes_Idempotent(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipSchemaTestIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Create the bootstrapper
	logger := observability.New(slog.LevelDebug)
	bootstrapper := NewSchemaBootstrapper(client, 1536, logger)

	// Run EnsureSchema multiple times
	for i := 0; i < 3; i++ {
		err = bootstrapper.EnsureSchema(ctx)
		require.NoError(t, err, "EnsureSchema should be idempotent - run %d should succeed", i+1)
	}

	// Verify composite indexes still exist after multiple runs
	compositeIndexes := []string{
		"fragment_team_idempotency_idx",
		"fragment_profile_content_hash_idx",
		"fragment_profile_created_at_idx",
	}

	for _, indexName := range compositeIndexes {
		result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				"SHOW INDEXES WHERE name = $name",
				map[string]interface{}{"name": indexName},
			)
			if err != nil {
				return nil, err
			}
			if res.Next(ctx) {
				return res.Record().Values[0], nil
			}
			return nil, nil
		})
		require.NoError(t, err, "Should be able to query indexes")
		assert.NotNil(t, result, "Composite index %s should still exist after multiple runs", indexName)
	}
}

// TestEnsureSchema_CreatesCanonicalIndexNames tests that canonical index names are created.
func TestEnsureSchema_CreatesCanonicalIndexNames(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipSchemaTestIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Clean up all indexes (legacy and canonical) to test from scratch
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		// Must consume results for DDL to actually execute
		if res, err := tx.Run(ctx, "DROP INDEX sourcefragment_content IF EXISTS", nil); err == nil {
			res.Consume(ctx)
		}
		if res, err := tx.Run(ctx, "DROP INDEX fragment_content_idx IF EXISTS", nil); err == nil {
			res.Consume(ctx)
		}
		if res, err := tx.Run(ctx, "DROP INDEX sourcefragment_embedding IF EXISTS", nil); err == nil {
			res.Consume(ctx)
		}
		if res, err := tx.Run(ctx, "DROP INDEX fragment_embedding_idx IF EXISTS", nil); err == nil {
			res.Consume(ctx)
		}
		if res, err := tx.Run(ctx, "DROP INDEX fact_predicate IF EXISTS", nil); err == nil {
			res.Consume(ctx)
		}
		if res, err := tx.Run(ctx, "DROP INDEX fact_predicate_idx IF EXISTS", nil); err == nil {
			res.Consume(ctx)
		}
		return nil, nil
	})

	// Create the bootstrapper
	logger := observability.New(slog.LevelDebug)
	bootstrapper := NewSchemaBootstrapper(client, 1536, logger)

	// Ensure schema
	err = bootstrapper.EnsureSchema(ctx)
	require.NoError(t, err, "EnsureSchema should succeed")

	// Verify canonical indexes exist
	canonicalIndexes := []string{
		"fragment_content_idx",
		"fragment_embedding_idx",
		"fact_predicate_idx",
	}

	for _, indexName := range canonicalIndexes {
		result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				"SHOW INDEXES WHERE name = $name",
				map[string]interface{}{"name": indexName},
			)
			if err != nil {
				return nil, err
			}
			if res.Next(ctx) {
				return res.Record().Values[0], nil
			}
			return nil, nil
		})
		require.NoError(t, err, "Should be able to query index %s", indexName)
		assert.NotNil(t, result, "Canonical index %s should exist", indexName)
	}
}
