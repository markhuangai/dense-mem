package neo4j

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
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

// ============================================================================
// Recording stub — captures Cypher strings without a real Neo4j connection.
// Used by unit tests for AC-3, AC-4, AC-5 (Unit 12).
// ============================================================================

// recordingClient records every Cypher string passed to ExecuteWrite/ExecuteRead.
// It satisfies Neo4jClientInterface without requiring a live driver.
type recordingClient struct {
	queries          []string
	writeErr         error // if non-nil, ExecuteWrite returns this error
	runErrFor        func(cypher string) error
	resultRecordsFor func(cypher string) []*neo4j.Record
	edition          string
}

func (c *recordingClient) Verify(_ context.Context) error { return nil }

func (c *recordingClient) ExecuteRead(ctx context.Context, fn neo4j.ManagedTransactionWork) (any, error) {
	tx := &recordingTx{
		queries:          &c.queries,
		runErrFor:        c.runErrFor,
		resultRecordsFor: c.resultRecordsFor,
		edition:          c.edition,
	}
	return fn(tx)
}

func (c *recordingClient) ExecuteWrite(ctx context.Context, fn neo4j.ManagedTransactionWork) (any, error) {
	if c.writeErr != nil {
		return nil, c.writeErr
	}
	tx := &recordingTx{
		queries:          &c.queries,
		runErrFor:        c.runErrFor,
		resultRecordsFor: c.resultRecordsFor,
		edition:          c.edition,
	}
	return fn(tx)
}

func (c *recordingClient) Close(_ context.Context) error { return nil }

// recordingTx implements neo4j.ManagedTransaction by embedding the interface
// (satisfies the unexported legacy() method) and overriding Run to capture queries.
type recordingTx struct {
	neo4j.ManagedTransaction // embedded nil satisfies legacy() at compile time
	queries                  *[]string
	runErrFor                func(cypher string) error
	resultRecordsFor         func(cypher string) []*neo4j.Record
	edition                  string
}

func (r *recordingTx) Run(_ context.Context, cypher string, _ map[string]any) (neo4j.ResultWithContext, error) {
	*r.queries = append(*r.queries, cypher)
	if r.runErrFor != nil {
		if err := r.runErrFor(cypher); err != nil {
			return nil, err
		}
	}
	return &stubResultWithContext{
		records: r.recordsFor(cypher),
		open:    true,
	}, nil
}

func (r *recordingTx) recordsFor(cypher string) []*neo4j.Record {
	if r.resultRecordsFor != nil {
		if records := r.resultRecordsFor(cypher); records != nil {
			return records
		}
	}
	if strings.Contains(strings.ToLower(cypher), "dbms.components()") {
		edition := r.edition
		if edition == "" {
			edition = string(EditionEnterprise)
		}
		return []*neo4j.Record{
			{
				Keys:   []string{"edition"},
				Values: []any{edition},
			},
		}
	}
	return nil
}

// stubResultWithContext satisfies neo4j.ResultWithContext without a live connection.
// It supports the small subset of iteration APIs used by schema bootstrap.
type stubResultWithContext struct {
	neo4j.ResultWithContext // embedded nil satisfies legacy() at compile time
	records                 []*neo4j.Record
	index                   int
	current                 *neo4j.Record
	err                     error
	open                    bool
}

func (s *stubResultWithContext) Keys() ([]string, error) {
	if len(s.records) == 0 {
		return nil, nil
	}
	return s.records[0].Keys, nil
}

func (s *stubResultWithContext) NextRecord(ctx context.Context, record **neo4j.Record) bool {
	ok := s.Next(ctx)
	if ok && record != nil {
		*record = s.current
	}
	return ok
}

func (s *stubResultWithContext) Next(_ context.Context) bool {
	if s.index >= len(s.records) {
		s.current = nil
		return false
	}
	s.current = s.records[s.index]
	s.index++
	return true
}

func (s *stubResultWithContext) PeekRecord(_ context.Context, record **neo4j.Record) bool {
	if s.index >= len(s.records) {
		return false
	}
	if record != nil {
		*record = s.records[s.index]
	}
	return true
}

func (s *stubResultWithContext) Peek(_ context.Context) bool {
	return s.index < len(s.records)
}

func (s *stubResultWithContext) Err() error {
	return s.err
}

func (s *stubResultWithContext) Record() *neo4j.Record {
	return s.current
}

func (s *stubResultWithContext) Collect(_ context.Context) ([]*neo4j.Record, error) {
	if s.index >= len(s.records) {
		return nil, s.err
	}
	remaining := s.records[s.index:]
	s.index = len(s.records)
	return remaining, s.err
}

func (s *stubResultWithContext) Records(ctx context.Context) func(yield func(*neo4j.Record, error) bool) {
	return func(yield func(*neo4j.Record, error) bool) {
		for s.Next(ctx) {
			if !yield(s.Record(), nil) {
				return
			}
		}
		if s.err != nil {
			yield(nil, s.err)
		}
	}
}

func (s *stubResultWithContext) Single(ctx context.Context) (*neo4j.Record, error) {
	records, err := s.Collect(ctx)
	if err != nil {
		return nil, err
	}
	if len(records) != 1 {
		return nil, fmt.Errorf("expected exactly one record, got %d", len(records))
	}
	s.current = records[0]
	return records[0], nil
}

func (s *stubResultWithContext) Consume(_ context.Context) (neo4j.ResultSummary, error) {
	s.index = len(s.records)
	s.open = false
	return nil, s.err
}

func (s *stubResultWithContext) IsOpen() bool {
	return s.open
}

// hasQuery returns true when any recorded query contains the substr.
func hasQuery(queries []string, substr string) bool {
	for _, q := range queries {
		if strings.Contains(q, substr) {
			return true
		}
	}
	return false
}

func queryIndex(queries []string, substr string) int {
	for i, q := range queries {
		if strings.Contains(q, substr) {
			return i
		}
	}
	return -1
}

// unitLogger returns a minimal logger suitable for unit tests.
func unitLogger() observability.LogProvider {
	return observability.New(slog.LevelDebug)
}

// ============================================================================
// Unit tests — no build tag, no Neo4j required (AC-3, AC-4, AC-5)
// ============================================================================

// TestEnsureSchema_ClaimCompositeIndexes verifies that EnsureSchema issues
// Claim composite indexes with team_id as the leading key (AC-3).
func TestEnsureSchema_ClaimCompositeIndexes(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)

	wantIndexes := []struct {
		name  string
		index string
	}{
		{"claim_profile_claim_id_idx", IndexClaimProfileClaimID},
		{"claim_profile_status_idx", IndexClaimProfileStatus},
		{"claim_profile_predicate_idx", IndexClaimProfilePredicate},
		{"claim_profile_subject_predicate_idx", IndexClaimProfileSubjectPredicate},
		{"claim_team_idempotency_idx", IndexClaimProfileIdempotency},
		{"claim_profile_content_hash_idx", IndexClaimProfileContentHash},
		{"claim_profile_recorded_at_idx", IndexClaimProfileRecordedAt},
		{"claim_owner_idempotency_idx", IndexClaimOwnerIdempotency},
		{"claim_owner_content_hash_idx", IndexClaimOwnerContentHash},
	}

	for _, w := range wantIndexes {
		assert.True(t, hasQuery(client.queries, w.index),
			"EnsureSchema must issue CREATE INDEX for %s", w.index)
	}
}

// TestEnsureSchema_FactCompositeIndexes verifies that EnsureSchema issues Fact
// composite indexes with team_id as the leading key (AC-4).
func TestEnsureSchema_FactCompositeIndexes(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)

	wantIndexes := []string{
		IndexFactProfileStatus,
		IndexFactProfileSubjectPredicateStatus,
		IndexFactProfileRecordedAt,
	}

	for _, idx := range wantIndexes {
		assert.True(t, hasQuery(client.queries, idx),
			"EnsureSchema must issue CREATE INDEX for %s", idx)
	}
}

// TestEnsureSchema_SourceFragmentStatusIndex verifies that EnsureSchema issues
// the SourceFragment status composite index with team_id as the leading key (AC-5).
func TestEnsureSchema_SourceFragmentStatusIndex(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)

	assert.True(t, hasQuery(client.queries, IndexSourceFragmentProfileStatus),
		"EnsureSchema must issue CREATE INDEX for %s", IndexSourceFragmentProfileStatus)
}

// TestEnsureSchema_CrossProfileIsolation verifies that every new composite index
// has team_id as its leading column, enforcing per-profile isolation at the
// schema level (profile-isolation.md).
func TestEnsureSchema_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)

	// Pipeline and owner index names must all keep team_id as the leading key.
	newIndexNames := []string{
		IndexClaimProfileClaimID,
		IndexClaimProfileStatus,
		IndexClaimProfilePredicate,
		IndexClaimProfileSubjectPredicate,
		IndexClaimProfileIdempotency,
		IndexClaimProfileContentHash,
		IndexClaimOwnerIdempotency,
		IndexClaimOwnerContentHash,
		IndexFactProfileStatus,
		IndexFactProfileSubjectPredicateStatus,
		IndexSourceFragmentProfileStatus,
		IndexFragmentOwnerIdempotency,
		IndexFragmentOwnerContentHash,
	}

	for _, idxName := range newIndexNames {
		// Find the CREATE INDEX cypher for this index name.
		found := false
		for _, q := range client.queries {
			if !strings.Contains(q, idxName) {
				continue
			}
			found = true
			// team_id must appear before any other field name in ON (...).
			onClause := q[strings.Index(q, " ON ("):]
			require.True(t, strings.Contains(onClause, "team_id"),
				"index %s must include team_id in ON clause: %s", idxName, q)
			// team_id must be the first property listed inside ON (...).
			firstParen := strings.Index(onClause, "(")
			require.True(t, firstParen >= 0, "expected ON clause in: %s", q)
			insideParen := onClause[firstParen+1:]
			assert.True(t, strings.HasPrefix(strings.TrimSpace(insideParen), strings.Split(insideParen, ".")[0]+".team_id") ||
				strings.Contains(insideParen[:strings.Index(insideParen, ",")+1], "team_id"),
				"team_id must be the leading key in index %s: %s", idxName, q)
		}
		assert.True(t, found, "no CREATE INDEX query found for %s", idxName)
	}
}

// TestEnsureSchema_LegacyDropIncludesFactPredicateIdx verifies that
// fact_predicate_idx is included in the legacy drop list so it can be
// recreated if a prior deployment created it with wrong configuration.
func TestEnsureSchema_LegacyDropIncludesFactPredicateIdx(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)

	assert.True(t, hasQuery(client.queries, "DROP INDEX fact_predicate_idx IF EXISTS"),
		"EnsureSchema must include DROP INDEX fact_predicate_idx IF EXISTS in legacy drops")
}

func TestEnsureSchema_BackfillsLegacyProfileIDBeforeTeamConstraints(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{edition: string(EditionEnterprise)}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)

	nodeBackfill := queryIndex(client.queries, "n.team_id IS NULL AND n.profile_id IS NOT NULL")
	relationshipBackfill := queryIndex(client.queries, "r.team_id IS NULL AND r.profile_id IS NOT NULL")
	endpointBackfill := queryIndex(client.queries, "a.team_id = b.team_id")
	constraint := queryIndex(client.queries, "CREATE CONSTRAINT supported_by_team_id_exists")

	require.NotEqual(t, -1, nodeBackfill, "node profile_id backfill query must be issued")
	require.NotEqual(t, -1, relationshipBackfill, "relationship profile_id backfill query must be issued")
	require.NotEqual(t, -1, endpointBackfill, "relationship endpoint team_id backfill query must be issued")
	require.NotEqual(t, -1, constraint, "enterprise relationship constraint query must be issued")

	assert.Less(t, nodeBackfill, constraint, "node backfill must run before relationship constraints")
	assert.Less(t, relationshipBackfill, constraint, "relationship backfill must run before relationship constraints")
	assert.Less(t, endpointBackfill, constraint, "endpoint relationship backfill must run before relationship constraints")
}

// schemaTestConfig implements ConfigProvider for schema testing
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
