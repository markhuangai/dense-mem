package neo4j

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// FragmentStatusMigrationRunner provides batch-safe migration for backfilling
// status='active' on SourceFragment nodes that have a null status property.
//
// OPERATOR OPERATION — GLOBAL SCOPE (AC-43):
// This migration is intentionally an operator-level operation that processes ALL
// SourceFragment nodes across ALL profiles in a single sweep. This design mirrors
// the existing FragmentMigrationRunner (fragment_migration.go) and is appropriate
// because:
//   - Schema migrations are cluster-wide, not per-profile operations.
//   - The migration only sets sf.status='active' on null nodes; it never reads,
//     exposes, or cross-contaminates profile data.
//   - After migration, every node retains its original sf.team_id so that
//     subsequent scoped queries (WHERE sf.team_id = $profileId) remain correct.
//   - Caller (e.g. an operator CLI or startup hook) is responsible for ensuring no
//     normal profile traffic can observe intermediate migration state.
//
// ADDITIVE MIGRATION CONTRACT (AC-43):
// - This migration is ADDITIVE ONLY.
// - Only sets sf.status when it is null (idempotent, safe to rerun).
// - Does not remove or rewrite any other node property, including team_id.
// - Legacy fragments remain readable during and after migration.
// - Migration is safe to rerun (idempotent).
//
// Per-profile isolation is NOT required here because migrations are not scoped
// to individual profiles; they are run globally by operators with elevated
// access. The profile-isolation rule (.claude/rules/profile-isolation.md)
// governs data-plane queries; operator-run migrations are intentionally exempt.
type FragmentStatusMigrationRunner interface {
	// BackfillFragmentStatus sets status='active' on SourceFragment nodes where
	// status is null. It processes in batches of batchSize to avoid a single
	// monolithic transaction (AC-43). Returns the total number of nodes updated.
	// The migration is idempotent — it only processes nodes with null status.
	BackfillFragmentStatus(ctx context.Context, batchSize int) (processed int, err error)
}

// fragmentStatusMigrationRunner implements FragmentStatusMigrationRunner.
type fragmentStatusMigrationRunner struct {
	client Neo4jClientInterface
	logger observability.LogProvider
}

// Ensure fragmentStatusMigrationRunner implements FragmentStatusMigrationRunner.
var _ FragmentStatusMigrationRunner = (*fragmentStatusMigrationRunner)(nil)

// NewFragmentStatusMigrationRunner creates a new migration runner for backfilling
// status='active' on SourceFragment nodes.
func NewFragmentStatusMigrationRunner(client Neo4jClientInterface, logger observability.LogProvider) FragmentStatusMigrationRunner {
	return &fragmentStatusMigrationRunner{
		client: client,
		logger: logger,
	}
}

// BackfillFragmentStatus sets status='active' on SourceFragment nodes where status is null.
// It processes in batches of batchSize to avoid a single monolithic transaction (AC-43).
//
// The algorithm:
// 1. Fetch a batch of fragment_ids with null status (using LIMIT for batch safety)
// 2. Write status='active' back using UNWIND for efficient batch update
// 3. Repeat until no more null rows or context cancelled
//
// This approach ensures:
// - No single giant transaction (AC-43)
// - Progress logging for observability
// - Context cancellation support
// - Idempotency (only processes null status nodes)
func (r *fragmentStatusMigrationRunner) BackfillFragmentStatus(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 100 // Default batch size
	}

	totalProcessed := 0

	for {
		// Check for context cancellation before each batch
		select {
		case <-ctx.Done():
			r.logger.Info("status backfill interrupted by context", observability.Int("processed", totalProcessed))
			return totalProcessed, ctx.Err()
		default:
		}

		// Fetch a batch of nodes with null status
		// Using LIMIT ensures batch-safe processing (AC-43)
		batch, err := r.fetchNullStatusBatch(ctx, batchSize)
		if err != nil {
			return totalProcessed, fmt.Errorf("failed to fetch status batch: %w", err)
		}

		if len(batch) == 0 {
			r.logger.Info("status backfill complete, no more null status nodes")
			break
		}

		// Write status='active' back to the database
		processed, err := r.writeActiveStatus(ctx, batch)
		if err != nil {
			return totalProcessed, fmt.Errorf("failed to write active status: %w", err)
		}

		totalProcessed += processed
		r.logger.Info("status backfill batch processed",
			observability.Int("batch_size", len(batch)),
			observability.Int("processed", processed),
			observability.Int("total", totalProcessed),
		)

		// If we got fewer than batchSize, we're done
		if len(batch) < batchSize {
			break
		}
	}

	return totalProcessed, nil
}

// fetchNullStatusBatch fetches a batch of SourceFragment node IDs where status is null.
// It uses LIMIT to ensure batch-safe processing (AC-43).
// Fragment IDs are returned via the ExecuteRead return value so that test stubs can
// inject pre-configured batches without needing to implement neo4j.ManagedTransaction.
func (r *fragmentStatusMigrationRunner) fetchNullStatusBatch(ctx context.Context, limit int) ([]string, error) {
	query := `
		MATCH (sf:SourceFragment)
		WHERE sf.status IS NULL
		WITH sf LIMIT $limit
		RETURN sf.fragment_id AS fragment_id
	`
	params := map[string]interface{}{"limit": limit}

	result, err := r.client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		res, err := tx.Run(ctx, query, params)
		if err != nil {
			return nil, err
		}

		// Collect all records
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}

		fragmentIDs := make([]string, 0, len(records))
		for _, record := range records {
			fragmentID, _ := record.Get("fragment_id")
			fragmentIDStr, _ := fragmentID.(string)
			fragmentIDs = append(fragmentIDs, fragmentIDStr)
		}

		return fragmentIDs, nil
	})

	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	fragmentIDs, ok := result.([]string)
	if !ok {
		return nil, nil
	}
	return fragmentIDs, nil
}

// writeActiveStatus sets status='active' on the given fragments using UNWIND for batch efficiency.
// The SET only applies when the node matches by fragment_id; since the fetch query already
// filtered for null status, this write is effectively idempotent.
// The processed count is returned via the ExecuteWrite return value so that test stubs can
// inject a synthetic count without needing to implement neo4j.ManagedTransaction.
func (r *fragmentStatusMigrationRunner) writeActiveStatus(ctx context.Context, fragmentIDs []string) (int, error) {
	if len(fragmentIDs) == 0 {
		return 0, nil
	}

	// Prepare rows for UNWIND. Each row carries only fragment_id — team_id is never
	// included here, ensuring the write cannot inadvertently modify profile ownership.
	rows := make([]map[string]interface{}, len(fragmentIDs))
	for i, id := range fragmentIDs {
		rows[i] = map[string]interface{}{"fragment_id": id}
	}

	query := `
		UNWIND $rows AS row
		MATCH (sf:SourceFragment {fragment_id: row.fragment_id})
		SET sf.status = 'active'
		RETURN count(sf) AS processed
	`

	result, err := r.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		res, err := tx.Run(ctx, query, map[string]interface{}{"rows": rows})
		if err != nil {
			return nil, err
		}

		// Collect result to get the count
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}

		if len(records) > 0 {
			record := records[0]
			val, ok := record.Get("processed")
			if ok {
				if count, ok := val.(int64); ok {
					return int(count), nil
				}
			}
		}

		return 0, nil
	})

	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, nil
	}
	count, ok := result.(int)
	if !ok {
		return 0, nil
	}
	return count, nil
}
