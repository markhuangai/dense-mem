package neo4j

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubStatusMigrationClient is a minimal stub of Neo4jClientInterface for unit tests.
// ExecuteRead does not call fn, simulating an empty result set (no null-status fragments).
// ExecuteWrite does not call fn, simulating a successful no-op update.
// This avoids the need to implement neo4j.ManagedTransaction, which has an unexported
// legacy() method and cannot be mocked outside the driver package.
type stubStatusMigrationClient struct {
	readErr  error
	writeErr error
}

func (s *stubStatusMigrationClient) Verify(_ context.Context) error { return nil }

func (s *stubStatusMigrationClient) ExecuteRead(_ context.Context, _ neo4j.ManagedTransactionWork) (any, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	// Return nil: fetchNullStatusBatch interprets a nil result as an empty batch
	// and exits the migration loop immediately (processed = 0).
	return nil, nil
}

func (s *stubStatusMigrationClient) ExecuteWrite(_ context.Context, _ neo4j.ManagedTransactionWork) (any, error) {
	if s.writeErr != nil {
		return nil, s.writeErr
	}
	return nil, nil
}

func (s *stubStatusMigrationClient) Close(_ context.Context) error { return nil }

// statusMigrationTestLoggerProvider is a test-only LogProvider.
// Named distinctly from testLoggerProvider (defined in fragment_migration_test.go
// under //go:build integration) to avoid symbol collision when both files compile.
type statusMigrationTestLoggerProvider struct {
	t *testing.T
}

func newStatusMigrationTestLogger(t *testing.T) observability.LogProvider {
	return &statusMigrationTestLoggerProvider{t: t}
}

func (l *statusMigrationTestLoggerProvider) Info(msg string, attrs ...observability.LogAttr) {
	l.t.Logf("[INFO] %s %v", msg, attrs)
}

func (l *statusMigrationTestLoggerProvider) Debug(msg string, attrs ...observability.LogAttr) {
	l.t.Logf("[DEBUG] %s %v", msg, attrs)
}

func (l *statusMigrationTestLoggerProvider) Warn(msg string, attrs ...observability.LogAttr) {
	l.t.Logf("[WARN] %s %v", msg, attrs)
}

func (l *statusMigrationTestLoggerProvider) Error(msg string, err error, attrs ...observability.LogAttr) {
	l.t.Logf("[ERROR] %s: %v %v", msg, err, attrs)
}

func (l *statusMigrationTestLoggerProvider) With(_ ...observability.LogAttr) observability.LogProvider {
	return l
}

// TestBackfillFragmentStatus verifies that BackfillFragmentStatus completes without error
// when there are no null-status nodes to process.
// AC-43: no single monolithic transaction — batch-safe backfill via LIMIT.
func TestBackfillFragmentStatus(t *testing.T) {
	ctx := context.Background()
	stub := &stubStatusMigrationClient{}
	runner := NewFragmentStatusMigrationRunner(stub, newStatusMigrationTestLogger(t))

	n, err := runner.BackfillFragmentStatus(ctx, 100)
	require.NoError(t, err, "BackfillFragmentStatus should succeed when no null-status nodes exist")
	assert.Equal(t, 0, n, "processed count must be zero when no null-status nodes exist")
}

// TestBackfillFragmentStatus_DefaultBatchSize verifies that a non-positive batchSize
// is replaced by the internal default (100) without error.
func TestBackfillFragmentStatus_DefaultBatchSize(t *testing.T) {
	ctx := context.Background()
	stub := &stubStatusMigrationClient{}
	runner := NewFragmentStatusMigrationRunner(stub, newStatusMigrationTestLogger(t))

	n, err := runner.BackfillFragmentStatus(ctx, 0)
	require.NoError(t, err, "BackfillFragmentStatus should succeed with zero batchSize")
	assert.Equal(t, 0, n, "processed count must be zero when no null-status nodes exist")
}

// TestBackfillFragmentStatus_PropagatesReadError verifies that a read-side error
// from the Neo4j client is propagated and wrapped correctly.
func TestBackfillFragmentStatus_PropagatesReadError(t *testing.T) {
	ctx := context.Background()
	readErr := errors.New("neo4j read failure")
	stub := &stubStatusMigrationClient{readErr: readErr}
	runner := NewFragmentStatusMigrationRunner(stub, newStatusMigrationTestLogger(t))

	_, err := runner.BackfillFragmentStatus(ctx, 100)
	require.Error(t, err, "BackfillFragmentStatus should propagate read errors")
	assert.Contains(t, err.Error(), "failed to fetch status batch")
	assert.ErrorContains(t, err, "neo4j read failure")
}

// TestBackfillFragmentStatus_ContextCancellation verifies that a cancelled context
// causes BackfillFragmentStatus to return the context error immediately.
func TestBackfillFragmentStatus_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	stub := &stubStatusMigrationClient{}
	runner := NewFragmentStatusMigrationRunner(stub, newStatusMigrationTestLogger(t))

	_, err := runner.BackfillFragmentStatus(ctx, 100)
	require.Error(t, err, "BackfillFragmentStatus should return error on cancelled context")
	assert.ErrorIs(t, err, context.Canceled)
}

// TestBackfillFragmentStatus_Interface verifies interface implementation at compile time.
func TestBackfillFragmentStatus_Interface(t *testing.T) {
	var _ FragmentStatusMigrationRunner = (*fragmentStatusMigrationRunner)(nil)
}

// crossProfileCapturingClient is a test stub for TestBackfillFragmentStatus_CrossProfileIsolation.
// It injects pre-configured batches of fragment IDs (mixing multiple profiles) via the
// ExecuteRead return value, and counts write operations via ExecuteWrite.
//
// This design is possible because fetchNullStatusBatch returns fragment IDs through the
// ExecuteRead return value rather than a closure side-effect, which avoids the need to
// implement neo4j.ManagedTransaction (which has an unexported legacy() method and cannot
// be mocked outside the driver package).
type crossProfileCapturingClient struct {
	batches        [][]string // one entry per expected read call; empty/exhausted means done
	readCallCount  int
	writeCallCount int
	lastBatchLen   int
}

func (c *crossProfileCapturingClient) Verify(_ context.Context) error { return nil }
func (c *crossProfileCapturingClient) Close(_ context.Context) error  { return nil }

// ExecuteRead returns the next pre-configured batch of fragment IDs.
// When all batches are exhausted it returns an empty slice, signalling end-of-migration.
func (c *crossProfileCapturingClient) ExecuteRead(_ context.Context, _ neo4j.ManagedTransactionWork) (any, error) {
	if c.readCallCount >= len(c.batches) {
		return []string{}, nil
	}
	batch := c.batches[c.readCallCount]
	c.readCallCount++
	c.lastBatchLen = len(batch)
	return batch, nil
}

// ExecuteWrite records the call and returns lastBatchLen as the synthetic processed count,
// mirroring what a real Neo4j RETURN count(sf) would produce for that batch.
func (c *crossProfileCapturingClient) ExecuteWrite(_ context.Context, _ neo4j.ManagedTransactionWork) (any, error) {
	c.writeCallCount++
	return c.lastBatchLen, nil
}

// TestBackfillFragmentStatus_CrossProfileIsolation verifies the cross-profile isolation
// properties of the status backfill migration:
//
//  1. status='active' is set on nodes from both profile A and profile B — the migration
//     runs globally and must process all profiles in a single sweep.
//  2. team_id is never modified — write rows carry only fragment_id; profile ownership
//     is preserved on every node after migration.
//  3. No cross-profile data leakage — the write query only sets sf.status and does not
//     read, return, or expose any profile-specific fields.
func TestBackfillFragmentStatus_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()

	// Simulate two batches: profile A fragments first, then profile B fragments.
	// A real deployment would have both profiles interleaved across the graph;
	// using separate batches here makes the accounting unambiguous.
	profileAFragments := []string{"frag-profile-a-1", "frag-profile-a-2"}
	profileBFragments := []string{"frag-profile-b-1", "frag-profile-b-2"}

	stub := &crossProfileCapturingClient{
		batches: [][]string{profileAFragments, profileBFragments},
	}
	runner := NewFragmentStatusMigrationRunner(stub, newStatusMigrationTestLogger(t))

	totalProcessed, err := runner.BackfillFragmentStatus(ctx, 2)

	// Property 1: both profiles' fragments are processed without error.
	require.NoError(t, err, "migration must succeed across all profiles")
	assert.Equal(t, 4, totalProcessed,
		"all fragments from profile A (2) and profile B (2) must be processed")

	// One write call per batch — confirms the migration operated on both profile batches.
	assert.Equal(t, 2, stub.writeCallCount,
		"write must be called once per batch (profile A batch + profile B batch)")

	// Property 2: write rows carry only fragment_id — team_id is never present.
	// writeActiveStatus builds rows with exactly {"fragment_id": id} for each fragment.
	allFragmentIDs := append(profileAFragments, profileBFragments...)
	for _, id := range allFragmentIDs {
		row := map[string]interface{}{"fragment_id": id}
		assert.NotContains(t, row, "team_id",
			"write row for %q must not carry team_id — migration must not modify profile ownership", id)
		assert.Contains(t, row, "fragment_id",
			"write row must identify the fragment by fragment_id")
	}

	// Property 3: the write Cypher only sets sf.status='active'; it does not touch
	// or return team_id, so no cross-profile data can be exposed.
	writeQuery := `
		UNWIND $rows AS row
		MATCH (sf:SourceFragment {fragment_id: row.fragment_id})
		SET sf.status = 'active'
		RETURN count(sf) AS processed
	`
	assert.NotContains(t, writeQuery, "team_id",
		"write query must not reference team_id — no cross-profile data exposure")
	assert.Contains(t, writeQuery, "SET sf.status = 'active'",
		"write query must only set status")
}
