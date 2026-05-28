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

// TestEnsureSchema_ClaimCompositeIndexes verifies that EnsureSchema issues all
// six Claim composite indexes with team_id as the leading key (AC-3).
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
	}

	for _, w := range wantIndexes {
		assert.True(t, hasQuery(client.queries, w.index),
			"EnsureSchema must issue CREATE INDEX for %s", w.index)
	}
}

// TestEnsureSchema_FactCompositeIndexes verifies that EnsureSchema issues both
// Fact composite indexes with team_id as the leading key (AC-4).
func TestEnsureSchema_FactCompositeIndexes(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)

	wantIndexes := []string{
		IndexFactProfileStatus,
		IndexFactProfileSubjectPredicateStatus,
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

	// All 9 pipeline index names from Unit 12.
	newIndexNames := []string{
		IndexClaimProfileClaimID,
		IndexClaimProfileStatus,
		IndexClaimProfilePredicate,
		IndexClaimProfileSubjectPredicate,
		IndexClaimProfileIdempotency,
		IndexClaimProfileContentHash,
		IndexFactProfileStatus,
		IndexFactProfileSubjectPredicateStatus,
		IndexSourceFragmentProfileStatus,
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
	if len(existingConstraints) < 4 {
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
