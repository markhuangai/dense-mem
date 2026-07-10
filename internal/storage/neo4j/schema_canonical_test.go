package neo4j

import (
	"context"
	"log/slog"
	"testing"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
