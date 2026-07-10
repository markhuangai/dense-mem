// Package fragmentservice — retract path.
//
// RETRACT SEMANTICS (AC-45):
// Retract is a soft tombstone: the SourceFragment node is stamped with
// status='retracted' and retracted_at=now. It remains in the graph for lineage
// but is excluded from all active-fragment reads via FragmentActiveFilter (AC-44).
//
// FACT REVALIDATION (AC-47, AC-48):
// After tombstoning, the service traverses
//
//	(c:Claim)-[:SUPPORTED_BY {team_id}]->(sf:SourceFragment)
//	(c)-[:PROMOTES_TO {team_id}]->(f:Fact)
//
// The SUPPORTED_BY direction is (Claim)->(SourceFragment): a Claim node carries
// the outgoing SUPPORTED_BY edge pointing to the SourceFragment that supports it.
// For each reachable Fact the remaining active support is recounted. When
// support_count < MinSourceCount AND max_source_quality < MinMaxSourceQuality
// (support gate fails on both arms — AC-35 OR semantics), the Fact is marked
// status='needs_revalidation'.
//
// ATOMICITY (AC-48):
// The entire tombstone + recompute + revalidation-flag sequence runs inside a
// single ScopedWriteTx. Either all writes commit or none do. No partial state
// is possible: if the transaction is interrupted, the SourceFragment remains
// active and no Fact is incorrectly flagged.
package fragmentservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	neo4jstorage "github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

// retractDB is the minimal Neo4j interface required by retractFragmentService.
//
// The entire tombstone + recompute + revalidation algorithm executes inside one
// managed write transaction (ScopedWriteTx). Using a single transaction ensures
// the operation is atomic: either the tombstone and all revalidation flags commit
// together or none do (AC-48).
//
// Profile isolation invariant: profileID must be non-empty; every Cypher
// statement inside fn MUST be issued via neo4jstorage.RunScoped to carry the
// $profileId guard.
type retractDB interface {
	ScopedWriteTx(ctx context.Context, profileID string, fn func(tx neo4j.ManagedTransaction) error) error
}

// retractFragmentService implements RetractFragmentService.
type retractFragmentService struct {
	db      retractDB
	audit   AuditEmitter
	logger  *slog.Logger
	metrics observability.DiscoverabilityMetrics
	// gates is DefaultPromotionGates by default; injectable for testing.
	gates map[string]factservice.PromotionGate
}

var _ RetractFragmentService = (*retractFragmentService)(nil)

// NewRetractFragmentService constructs a RetractFragmentService.
// metrics may be nil; a noop recorder is substituted so call sites need no nil checks.
func NewRetractFragmentService(
	db retractDB,
	audit AuditEmitter,
	logger *slog.Logger,
	metrics observability.DiscoverabilityMetrics,
) RetractFragmentService {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &retractFragmentService{
		db:      db,
		audit:   audit,
		logger:  logger,
		metrics: metrics,
		gates:   factservice.DefaultPromotionGates,
	}
}

// Retract tombstones the fragment and marks affected facts for revalidation.
//
// Algorithm (all steps inside one ScopedWriteTx for atomicity — AC-48):
//  1. tx-local existence check: accurate 404, no existence leak across profiles.
//  2. Tombstone: SET status='retracted', retracted_at=now.
//  3. Traverse affected facts; collect remaining active support stats (run after
//     tombstone so the retracted node is excluded by the active filter).
//  4. Evaluate each fact against DefaultPromotionGates (OR semantics, AC-35).
//  5. Single set-based UNWIND SET status='needs_revalidation' for failing facts.
//  6. Post-tx: emit fragment_retract_total, fact_needs_revalidation_total, and audit event.
func (s *retractFragmentService) Retract(ctx context.Context, profileID, fragmentID string) error {
	now := time.Now().UTC()

	// needsRevalidationCount is captured from inside the tx fn so that the
	// metric can be emitted after the transaction commits.
	var needsRevalidationCount int

	err := s.db.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		// Step 1: tx-local existence check — same profile-scoped pattern as Delete (AC-31).
		// Running inside the transaction prevents a TOCTOU race between the check
		// and the tombstone SET.
		existsResult, err := neo4jstorage.RunScoped(ctx, tx, profileID,
			`MATCH (sf:SourceFragment {team_id: $profileId, fragment_id: $fragmentId})
			 RETURN sf.fragment_id AS fragment_id,
			        coalesce(sf.owner_profile_id, sf.created_by_profile_id, '') AS owner_profile_id
			 LIMIT 1`,
			map[string]any{"fragmentId": fragmentID},
		)
		if err != nil {
			return fmt.Errorf("existence check: %w", err)
		}
		existsRecords, err := existsResult.Collect(ctx)
		if err != nil {
			return fmt.Errorf("existence collect: %w", err)
		}
		if len(existsRecords) == 0 {
			return ErrFragmentNotFound
		}
		ownerID, _ := existsRecords[0].AsMap()["owner_profile_id"].(string)
		if err := ownership.RequireOwner(ctx, ownerID); err != nil {
			return err
		}

		// Step 2: tombstone the fragment.
		tombstoneResult, err := neo4jstorage.RunScoped(ctx, tx, profileID,
			`MATCH (sf:SourceFragment {team_id: $profileId, fragment_id: $fragmentId})
			 SET sf.status = 'retracted',
			     sf.retracted_at = coalesce(sf.retracted_at, $now)`,
			map[string]any{"fragmentId": fragmentID, "now": now},
		)
		if err != nil {
			return fmt.Errorf("tombstone: %w", err)
		}
		if _, err := tombstoneResult.Consume(ctx); err != nil {
			return fmt.Errorf("tombstone consume: %w", err)
		}

		// Step 3: collect affected facts and their remaining active support stats.
		// The tombstoned fragment is now excluded from the active-source count because
		// neo4jstorage.FragmentActiveFilter (coalesce(sf.status,'active') = 'active')
		// filters it out (AC-44).
		//
		// SUPPORTED_BY direction: (Claim)-[:SUPPORTED_BY]->(SourceFragment).
		// Line 1: Claim → SUPPORTED_BY → retractedSF  (the retracted fragment).
		// Line 2: Claim → PROMOTES_TO → Fact          (facts the claim promotes).
		// Line 3: c2 → SUPPORTED_BY → sf              (remaining active fragments for each fact).
		affectedQuery := fmt.Sprintf(`
			MATCH (retractedSF:SourceFragment {team_id: $profileId, fragment_id: $fragmentId})
			MATCH (c:Claim {team_id: $profileId})-[:SUPPORTED_BY {team_id: $profileId}]->(retractedSF)
			MATCH (c)-[:PROMOTES_TO {team_id: $profileId}]->(f:Fact {team_id: $profileId})
			OPTIONAL MATCH (f)<-[:PROMOTES_TO {team_id: $profileId}]-(c2:Claim {team_id: $profileId})-[:SUPPORTED_BY {team_id: $profileId}]->(sf:SourceFragment {team_id: $profileId})
			WHERE %s
			WITH f, count(sf) AS activeSourceCount, max(coalesce(sf.source_quality, 0.0)) AS maxSourceQuality
			RETURN f.fact_id AS fact_id, f.predicate AS predicate,
			       activeSourceCount AS active_source_count, maxSourceQuality AS max_source_quality
		`, neo4jstorage.FragmentActiveFilter)

		affectedResult, err := neo4jstorage.RunScoped(ctx, tx, profileID, affectedQuery,
			map[string]any{"fragmentId": fragmentID},
		)
		if err != nil {
			return fmt.Errorf("affected facts: %w", err)
		}
		affectedRecords, err := affectedResult.Collect(ctx)
		if err != nil {
			return fmt.Errorf("affected facts collect: %w", err)
		}

		// Step 4: evaluate gate and collect failing fact IDs.
		var failingFactIDs []string
		for _, record := range affectedRecords {
			row := record.AsMap()
			factID, _ := row["fact_id"].(string)
			predicate, _ := row["predicate"].(string)
			activeSourceCount := toInt(row["active_source_count"])
			maxSourceQuality := toFloat64(row["max_source_quality"])

			if !s.passesGate(predicate, activeSourceCount, maxSourceQuality) {
				failingFactIDs = append(failingFactIDs, factID)
			}
		}
		needsRevalidationCount = len(failingFactIDs)

		// Step 5: single set-based revalidation write — avoids N round-trips.
		// Only issued when there are facts to mark; skipped entirely when none fail.
		if len(failingFactIDs) > 0 {
			revalResult, err := neo4jstorage.RunScoped(ctx, tx, profileID,
				`UNWIND $factIds AS factId
				 MATCH (f:Fact {team_id: $profileId, fact_id: factId})
				 SET f.status = 'needs_revalidation'`,
				map[string]any{"factIds": failingFactIDs},
			)
			if err != nil {
				return fmt.Errorf("mark revalidation: %w", err)
			}
			if _, err := revalResult.Consume(ctx); err != nil {
				return fmt.Errorf("mark revalidation consume: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		// ErrFragmentNotFound is expected; skip the warning log for it.
		if s.logger != nil && !errors.Is(err, ErrFragmentNotFound) {
			s.logger.Warn("retract transaction failed",
				slog.String("team_id", profileID),
				slog.String("fragment_id", fragmentID),
				slog.String("error", err.Error()),
			)
		}
		return err
	}

	// Step 6: post-tx side effects — emitted only after the transaction commits
	// so no counters are bumped on failure.
	observability.RecordFragmentRetract(ctx, s.metrics)
	for i := 0; i < needsRevalidationCount; i++ {
		observability.RecordFactNeedsRevalidation(ctx, s.metrics)
	}

	if s.audit != nil {
		entry := AuditLogEntry{
			ProfileID:     &profileID,
			Timestamp:     time.Now().UTC(),
			Operation:     "fragment.retract",
			EntityType:    "fragment",
			EntityID:      fragmentID,
			CorrelationID: correlation.FromContext(ctx),
			AfterPayload: map[string]interface{}{
				"fragment_id": fragmentID,
				"team_id":     profileID,
				// content and embedding intentionally excluded (AC-26)
			},
		}
		if err := s.audit.Append(ctx, entry); err != nil {
			if s.logger != nil {
				s.logger.Warn("failed to emit audit event for fragment retraction",
					slog.String("team_id", profileID),
					slog.String("fragment_id", fragmentID),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	return nil
}

// passesGate returns true when the remaining active support satisfies the gate.
// Support gate uses OR semantics (AC-35): support_count >= MinSourceCount OR
// max_source_quality >= MinMaxSourceQuality.
// For unknown predicates the gate is denied by default (AC-34).
func (s *retractFragmentService) passesGate(predicate string, activeSourceCount int, maxSourceQuality float64) bool {
	gate, ok := s.gates[predicate]
	if !ok {
		// Deny by default: unknown predicates are not policed.
		return false
	}
	// OR semantics — passing either arm is sufficient.
	return activeSourceCount >= gate.MinSourceCount || maxSourceQuality >= gate.MinMaxSourceQuality
}

// toInt coerces a Neo4j integer value (int64 or int) to int.
func toInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

// toFloat64 coerces a Neo4j float value to float64.
func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	default:
		return 0.0
	}
}
