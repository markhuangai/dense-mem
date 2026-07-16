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

func TestEnsureSchema_DropsPlainIndexBeforeSameNamedUniqueConstraint(t *testing.T) {
	ctx := context.Background()
	dreamIndexDropped := false
	client := &recordingClient{
		resultRecordsFor: func(cypher string) []*neo4j.Record {
			if strings.Contains(cypher, "SHOW INDEXES") {
				return []*neo4j.Record{{
					Keys:   []string{"owningConstraint"},
					Values: []any{nil},
				}}
			}
			return nil
		},
		runErrFor: func(cypher string) error {
			if strings.Contains(cypher, "DROP INDEX dream_dream_id_unique") {
				dreamIndexDropped = true
			}
			if strings.Contains(cypher, "CREATE CONSTRAINT dream_dream_id_unique") && !dreamIndexDropped {
				return fmt.Errorf("Neo4jError: Neo.ClientError.Schema.IndexWithNameAlreadyExists (There already exists an index called 'dream_dream_id_unique'.)")
			}
			return nil
		},
	}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)

	dropIndex := queryIndex(client.queries, "DROP INDEX dream_dream_id_unique")
	createIndex := queryIndex(client.queries, "CREATE CONSTRAINT dream_dream_id_unique")
	require.NotEqual(t, -1, dropIndex, "EnsureSchema must drop blocking plain dream index")
	require.NotEqual(t, -1, createIndex, "EnsureSchema must create dream unique constraint")
	require.Less(t, dropIndex, createIndex, "blocking plain index must be dropped before creating the same-named constraint")
}

func TestEnsureSchema_IgnoresConstraintOwnedUniqueIndexDrop(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{
		resultRecordsFor: func(cypher string) []*neo4j.Record {
			if strings.Contains(cypher, "SHOW INDEXES") {
				return []*neo4j.Record{{
					Keys:   []string{"owningConstraint"},
					Values: []any{"dream_dream_id_unique"},
				}}
			}
			return nil
		},
	}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)
	assert.False(t, hasQuery(client.queries, "DROP INDEX dream_dream_id_unique"),
		"EnsureSchema must not attempt to drop constraint-owned index names")
}

func TestEnsureSchema_FallsBackWhenLegacyUniqueIndexInspectionFails(t *testing.T) {
	ctx := context.Background()
	client := &recordingClient{
		runErrFor: func(cypher string) error {
			if strings.Contains(cypher, "SHOW INDEXES") {
				return fmt.Errorf("Neo4jError: unsupported SHOW INDEXES column")
			}
			if strings.Contains(cypher, "DROP INDEX dream_dream_id_unique") {
				return fmt.Errorf("Neo4jError: Neo.ClientError.Schema.IndexBelongsToConstraint (Index belongs to constraint 'dream_dream_id_unique'.)")
			}
			return nil
		},
	}
	bs := NewSchemaBootstrapper(client, 1536, unitLogger())

	err := bs.EnsureSchema(ctx)
	require.NoError(t, err)
	assert.True(t, hasQuery(client.queries, "DROP INDEX dream_dream_id_unique"),
		"EnsureSchema must keep legacy drop fallback when index metadata inspection is unavailable")
}

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
		IndexDreamProfileDreamID,
		IndexDreamProfileStatus,
		IndexDreamProfileContentHash,
		IndexDreamProfileUpdatedAt,
		IndexDreamRunProfileDate,
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
	claimSupportCleanup := queryIndex(client.queries, "REMOVE c.supported_by")
	fragmentRetractedAtBackfill := queryIndex(client.queries, "SET sf.retracted_at = sf.recorded_to")
	factAuthorityCleanup := queryIndex(client.queries, "REMOVE f.authority_state")
	constraint := queryIndex(client.queries, "CREATE CONSTRAINT supported_by_team_id_exists")

	require.NotEqual(t, -1, nodeBackfill, "node profile_id backfill query must be issued")
	require.NotEqual(t, -1, relationshipBackfill, "relationship profile_id backfill query must be issued")
	require.NotEqual(t, -1, endpointBackfill, "relationship endpoint team_id backfill query must be issued")
	require.NotEqual(t, -1, claimSupportCleanup, "claim supported_by property cleanup query must be issued")
	require.NotEqual(t, -1, fragmentRetractedAtBackfill, "fragment retracted_at backfill query must be issued")
	require.NotEqual(t, -1, factAuthorityCleanup, "fact authority_state cleanup query must be issued")
	require.NotEqual(t, -1, constraint, "enterprise relationship constraint query must be issued")

	assert.Less(t, nodeBackfill, constraint, "node backfill must run before relationship constraints")
	assert.Less(t, relationshipBackfill, constraint, "relationship backfill must run before relationship constraints")
	assert.Less(t, endpointBackfill, constraint, "endpoint relationship backfill must run before relationship constraints")
}

// schemaTestConfig implements ConfigProvider for schema testing
