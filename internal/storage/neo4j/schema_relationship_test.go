package neo4j

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRelationshipProfileConstraints verifies that EnsureSchema issues all four
// relationship team_id existence constraints (Unit 13, AC-X1).
//
// Each constraint must:
//   - Use the canonical constant name (prevents typo drift).
//   - Target the correct relationship type.
//   - Require r.team_id IS NOT NULL (enforces profile isolation at the edge level).
//
// A cross-profile isolation sub-test verifies that profile A data cannot leak
// to profile B through an unconstrained relationship.
func TestRelationshipProfileConstraints(t *testing.T) {
	t.Run("constraints_issued_by_EnsureSchema", func(t *testing.T) {
		ctx := context.Background()
		client := &recordingClient{}
		bs := NewSchemaBootstrapper(client, 1536, unitLogger())

		err := bs.EnsureSchema(ctx)
		require.NoError(t, err)

		wantConstraints := []struct {
			name      string
			relType   string
			constName string
		}{
			{"SUPPORTED_BY", "SUPPORTED_BY", ConstraintSupportedByProfileIDExists},
			{"PROMOTES_TO", "PROMOTES_TO", ConstraintPromotesToProfileIDExists},
			{"SUPERSEDED_BY", "SUPERSEDED_BY", ConstraintSupersededByProfileIDExists},
			{"CONTRADICTS", "CONTRADICTS", ConstraintContradictsProfileIDExists},
			{"OVERLAYS", "OVERLAYS", ConstraintOverlaysProfileIDExists},
			{"ALIGNS_WITH", "ALIGNS_WITH", ConstraintAlignsWithProfileIDExists},
			{"DREAMS_FROM", "DREAMS_FROM", ConstraintDreamsFromProfileIDExists},
			{"PROMOTED_TO", "PROMOTED_TO", ConstraintPromotedToProfileIDExists},
		}

		for _, w := range wantConstraints {
			t.Run(w.name, func(t *testing.T) {
				// The Cypher must reference the canonical constraint name.
				assert.True(t, hasQuery(client.queries, w.constName),
					"EnsureSchema must issue CREATE CONSTRAINT for %s", w.constName)
				// The Cypher must target the correct relationship type.
				assert.True(t, hasQuery(client.queries, w.relType),
					"Constraint for %s must reference rel type %s", w.constName, w.relType)
				// The Cypher must enforce IS NOT NULL on team_id.
				found := false
				for _, q := range client.queries {
					if strings.Contains(q, w.constName) {
						assert.True(t, strings.Contains(q, "team_id IS NOT NULL"),
							"Constraint %s must require team_id IS NOT NULL: %s", w.constName, q)
						found = true
						break
					}
				}
				assert.True(t, found, "no CREATE CONSTRAINT query found for %s", w.constName)
			})
		}
	})

}

func TestEnsureSchema_RelationshipConstraintsUnsupportedDoesNotFail(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{
		runErrFor: func(cypher string) error {
			if strings.Contains(cypher, "REQUIRE r.team_id IS NOT NULL") {
				return fmt.Errorf("Neo4jError: Neo.DatabaseError.Schema.ConstraintCreationFailed (Property existence constraint requires Neo4j Enterprise Edition.)")
			}
			return nil
		},
	}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)
	assert.True(t, hasQuery(client.queries, IndexCommunityProfileCommunityID),
		"EnsureSchema must continue creating later indexes when relationship constraints are unsupported")
}

func TestEnsureSchema_CommunityEditionSkipsRelationshipConstraints(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{edition: string(EditionCommunity)}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)

	assert.False(t, hasQuery(client.queries, ConstraintSupportedByProfileIDExists))
	assert.False(t, hasQuery(client.queries, ConstraintPromotesToProfileIDExists))
	assert.False(t, hasQuery(client.queries, ConstraintSupersededByProfileIDExists))
	assert.False(t, hasQuery(client.queries, ConstraintContradictsProfileIDExists))
	assert.False(t, hasQuery(client.queries, ConstraintOverlaysProfileIDExists))
	assert.False(t, hasQuery(client.queries, ConstraintAlignsWithProfileIDExists))
	assert.False(t, hasQuery(client.queries, ConstraintDreamsFromProfileIDExists))
	assert.False(t, hasQuery(client.queries, ConstraintPromotedToProfileIDExists))
	assert.True(t, hasQuery(client.queries, IndexCommunityProfileCommunityID),
		"EnsureSchema must continue creating supported indexes on community edition")
}

func TestEnsureSchema_RelationshipConstraintUnexpectedFailureReturnsError(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{
		runErrFor: func(cypher string) error {
			if strings.Contains(cypher, ConstraintSupportedByProfileIDExists) {
				return fmt.Errorf("boom")
			}
			return nil
		},
	}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), ConstraintSupportedByProfileIDExists)
}

// TestRelationshipProfileConstraints_LiveEnforcement verifies that the four
// relationship team_id existence constraints actually prevent creating
// relationships without team_id (AC-X1 enforcement).
//
// This is a live integration test against Neo4j that complements the unit-level
// Cypher-recording test above.  It uses the _TestNode label so all data can be
// cleaned up deterministically.
func TestRelationshipProfileConstraints_LiveEnforcement(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipSchemaTestIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	if err != nil {
		t.Skipf("Neo4j not reachable (NewClient failed): %v", err)
	}
	defer client.Close(ctx)

	// Drop relationship constraints before the test so we start from a known
	// state (idempotent; ignores "not found" via IF EXISTS).
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		for _, name := range []string{
			ConstraintSupportedByProfileIDExists,
			ConstraintPromotesToProfileIDExists,
			ConstraintSupersededByProfileIDExists,
			ConstraintContradictsProfileIDExists,
			ConstraintOverlaysProfileIDExists,
			ConstraintAlignsWithProfileIDExists,
		} {
			if res, err := tx.Run(ctx, "DROP CONSTRAINT "+name+" IF EXISTS", nil); err == nil {
				res.Consume(ctx)
			}
		}
		return nil, nil
	})

	// Remove any stale test nodes from a previous run.
	cleanupNodes := func() {
		_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			if res, err := tx.Run(ctx, "MATCH (n:_TestNode) DETACH DELETE n", nil); err == nil {
				res.Consume(ctx)
			}
			return nil, nil
		})
	}
	cleanupNodes()
	defer cleanupNodes()

	// Run EnsureSchema.  If relationship existence constraints are not supported
	// by this Neo4j edition/version, the call will fail and we skip enforcement
	// tests rather than reporting a false failure.
	logger := observability.New(slog.LevelDebug)
	bootstrapper := NewSchemaBootstrapper(client, 1536, logger)
	if err := bootstrapper.EnsureSchema(ctx); err != nil {
		t.Skipf("EnsureSchema failed (relationship existence constraints may be unsupported): %v", err)
	}

	existingConstraintsRaw, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		res, err := tx.Run(ctx,
			"SHOW CONSTRAINTS YIELD name WHERE name IN $names RETURN name",
			map[string]any{"names": []string{
				ConstraintSupportedByProfileIDExists,
				ConstraintPromotesToProfileIDExists,
				ConstraintSupersededByProfileIDExists,
				ConstraintContradictsProfileIDExists,
				ConstraintOverlaysProfileIDExists,
				ConstraintAlignsWithProfileIDExists,
			}},
		)
		if err != nil {
			return nil, err
		}
		var names []string
		for res.Next(ctx) {
			name, _ := res.Record().Get("name")
			if s, ok := name.(string); ok {
				names = append(names, s)
			}
		}
		return names, res.Err()
	})
	require.NoError(t, err)

	existingConstraints, ok := existingConstraintsRaw.([]string)
	require.True(t, ok, "existing constraints result must be []string")
	if len(existingConstraints) < 6 {
		t.Skip("relationship team_id constraints are unsupported by the connected Neo4j edition")
	}

	t.Run("rejects_relationship_without_team_id", func(t *testing.T) {
		// The SUPPORTED_BY existence constraint must reject a relationship that
		// omits team_id entirely (or sets it to null).
		_, err := client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				"CREATE (:_TestNode {id: 'rej-a'})-[r:SUPPORTED_BY]->(:_TestNode {id: 'rej-b'})",
				nil,
			)
			if err != nil {
				return nil, err
			}
			res.Consume(ctx)
			return nil, nil
		})
		require.Error(t, err,
			"creating SUPPORTED_BY without team_id must be rejected by the constraint")
	})

	t.Run("cross_profile_isolation", func(t *testing.T) {
		// Create SUPPORTED_BY relationships for two distinct profiles and verify
		// that a query scoped to profile A does not return profile B's data.
		profileA := "test-enforce-profile-a"
		profileB := "test-enforce-profile-b"

		// Profile A relationship.
		_, err := client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				`CREATE (:_TestNode {id: $sfID,    team_id: $pid})
				 -[r:SUPPORTED_BY {team_id: $pid}]->
				 (:_TestNode {id: $claimID, team_id: $pid})`,
				map[string]any{
					"sfID":    "sf-enforce-a",
					"claimID": "claim-enforce-a",
					"pid":     profileA,
				},
			)
			if err != nil {
				return nil, err
			}
			res.Consume(ctx)
			return nil, nil
		})
		require.NoError(t, err, "creating profile A SUPPORTED_BY relationship must succeed")

		// Profile B relationship.
		_, err = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				`CREATE (:_TestNode {id: $sfID,    team_id: $pid})
				 -[r:SUPPORTED_BY {team_id: $pid}]->
				 (:_TestNode {id: $claimID, team_id: $pid})`,
				map[string]any{
					"sfID":    "sf-enforce-b",
					"claimID": "claim-enforce-b",
					"pid":     profileB,
				},
			)
			if err != nil {
				return nil, err
			}
			res.Consume(ctx)
			return nil, nil
		})
		require.NoError(t, err, "creating profile B SUPPORTED_BY relationship must succeed")

		// A query scoped to profile A must return only profile A's relationship
		// and must not contain any profile B data.
		result, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			res, err := tx.Run(ctx,
				`MATCH (:_TestNode)-[r:SUPPORTED_BY]->(:_TestNode)
				 WHERE r.team_id = $profileId
				 RETURN r.team_id AS team_id`,
				map[string]any{"profileId": profileA},
			)
			if err != nil {
				return nil, err
			}
			var profiles []string
			for res.Next(ctx) {
				pid, _ := res.Record().Get("team_id")
				if s, ok := pid.(string); ok {
					profiles = append(profiles, s)
				}
			}
			return profiles, res.Err()
		})
		require.NoError(t, err, "profile-scoped relationship query must succeed")

		profiles, ok := result.([]string)
		require.True(t, ok, "result must be []string")
		require.NotEmpty(t, profiles, "profile A query must return at least one relationship")

		for _, p := range profiles {
			assert.Equal(t, profileA, p,
				"every returned relationship must belong to profile A, got %q", p)
		}
		require.NotContains(t, profiles, profileB,
			"data from profile B must not appear in profile A results")
	})
}

// TestEnsureSchema_DropsLegacyIndexes tests that legacy index names are migrated to canonical names.
func TestEnsureSchema_DropsLegacyIndexes(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := skipSchemaTestIfNoNeo4j(t, ctx)
	defer cleanup()

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client, "NewClient should return non-nil client")
	defer client.Close(ctx)

	// Clean up all indexes first (both legacy and canonical)
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
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

	// Create legacy indexes manually (simulating a pre-existing database)
	_, _ = client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		// Create legacy fulltext index for content
		if res, err := tx.Run(ctx, "CREATE FULLTEXT INDEX sourcefragment_content FOR (sf:SourceFragment) ON EACH [sf.content]", nil); err == nil {
			res.Consume(ctx)
		}
		// Create legacy fact_predicate index
		if res, err := tx.Run(ctx, "CREATE FULLTEXT INDEX fact_predicate FOR (f:Fact) ON EACH [f.predicate]", nil); err == nil {
			res.Consume(ctx)
		}
		return nil, nil
	})

	// Verify legacy indexes exist before migration
	legacyIndexes := []string{"sourcefragment_content", "fact_predicate"}
	for _, indexName := range legacyIndexes {
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
		require.NoError(t, err, "Should be able to query legacy index %s", indexName)
		require.NotNil(t, result, "Legacy index %s should exist before migration", indexName)
	}

	// Create the bootstrapper and run EnsureSchema
	logger := observability.New(slog.LevelDebug)
	bootstrapper := NewSchemaBootstrapper(client, 1536, logger)

	err = bootstrapper.EnsureSchema(ctx)
	require.NoError(t, err, "EnsureSchema should succeed")

	// Verify legacy indexes are gone
	for _, indexName := range legacyIndexes {
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
		assert.Nil(t, result, "Legacy index %s should be dropped", indexName)
	}

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
		require.NoError(t, err, "Should be able to query canonical index %s", indexName)
		assert.NotNil(t, result, "Canonical index %s should exist after migration", indexName)
	}
}
