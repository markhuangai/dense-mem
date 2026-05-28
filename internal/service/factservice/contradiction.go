package factservice

import (
	"context"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	neo4jstorage "github.com/markhuangai/dense-mem/internal/storage/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// contradictionTxRunner executes multi-statement write transactions scoped to
// a profile. The caller-supplied fn receives the underlying
// neo4j.ManagedTransaction; every Cypher statement inside fn MUST be issued
// via neo4jstorage.RunScoped to maintain the $profileId guard.
//
// Profile isolation invariant: profileID must be non-empty; implementations
// are expected to propagate it into every downstream query.
type contradictionTxRunner interface {
	ScopedWriteTx(ctx context.Context, profileID string, fn func(tx neo4j.ManagedTransaction) error) error
}

// findActiveFactsCypher retrieves all active Facts for a (profile, subject,
// predicate) triple.
//
// Profile isolation: $profileId is injected automatically by ScopedRead.
// The status filter is applied inline to avoid a separate parameter.
const findActiveFactsCypher = `
MATCH (f:Fact {team_id: $profileId, subject: $subject, predicate: $predicate})
WHERE f.status = 'active'
RETURN
    f.fact_id                        AS fact_id,
    f.created_by_profile_id          AS created_by_profile_id,
    f.created_by_profile_name        AS created_by_profile_name,
    f.promoted_by_profile_id         AS promoted_by_profile_id,
    f.promoted_by_profile_name       AS promoted_by_profile_name,
    f.subject                        AS subject,
    f.predicate                      AS predicate,
    f.object                         AS object,
    f.status                         AS status,
    f.truth_score                    AS truth_score,
    f.valid_from                     AS valid_from,
    f.valid_to                       AS valid_to,
    f.recorded_at                    AS recorded_at,
    f.recorded_to                    AS recorded_to,
    f.retracted_at                   AS retracted_at,
    f.last_confirmed_at              AS last_confirmed_at,
    f.promoted_from_claim_id         AS promoted_from_claim_id,
    f.classification                 AS classification,
    f.classification_json            AS classification_json,
    f.classification_lattice_version AS classification_lattice_version,
    f.source_quality                 AS source_quality,
    f.labels                         AS labels,
    f.metadata                       AS metadata`

// findActiveFactsBySubjectPredicate returns all active Facts for the given
// (profileID, subject, predicate) triple. Returns an empty slice when none
// exist. The caller uses this to determine which contradiction path applies.
//
// Profile isolation: $profileId is injected by ScopedRead; facts belonging to
// other profiles are never returned.
func findActiveFactsBySubjectPredicate(
	ctx context.Context,
	reader factReader,
	profileID, subject, predicate string,
) ([]*domain.Fact, error) {
	_, rows, err := reader.ScopedRead(ctx, profileID, findActiveFactsCypher, map[string]any{
		"subject":   subject,
		"predicate": predicate,
	})
	if err != nil {
		return nil, fmt.Errorf("find active facts: %w", err)
	}

	facts := make([]*domain.Fact, 0, len(rows))
	for _, row := range rows {
		facts = append(facts, rowToFact(profileID, row))
	}
	return facts, nil
}

// newActivePath documents the promotion decision when no active Facts exist for
// the (subject, predicate) pair within the profile. No graph writes are needed;
// the caller proceeds directly to creating the new Fact node.
//
// This function is intentionally a no-op. It serves as an explicit named branch
// in the five-way contradiction decision tree so callers remain readable.
func newActivePath() {}

// sameObjectConfirmPath updates last_confirmed_at on each existing active Fact
// whose object matches the new claim's object. The new evidence confirms the
// Fact is still valid.
//
// Profile isolation: every RunScoped call injects $profileId into the Cypher.
func sameObjectConfirmPath(
	ctx context.Context,
	db contradictionTxRunner,
	profileID string,
	existingFacts []*domain.Fact,
) error {
	now := time.Now().UTC()
	return db.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		for _, f := range existingFacts {
			result, err := neo4jstorage.RunScoped(ctx, tx, profileID,
				`MATCH (f:Fact {team_id: $profileId, fact_id: $factId})
                 SET f.last_confirmed_at = $now`,
				map[string]any{
					"factId": f.FactID,
					"now":    now,
				},
			)
			if err != nil {
				return fmt.Errorf("confirm fact %s: %w", f.FactID, err)
			}
			if _, err := result.Consume(ctx); err != nil {
				return fmt.Errorf("confirm consume %s: %w", f.FactID, err)
			}
		}
		return nil
	})
}

// supersedePath marks each oldFact as superseded, closes its recorded_to and
// valid_to timestamps, and creates a SUPERSEDED_BY relationship from the old
// Fact to the new Claim. The relationship carries team_id for isolation.
//
// valid_to is set to newClaimValidFrom when non-nil, otherwise to now. This
// preserves the temporal validity chain across supersessions.
//
// Profile isolation: every RunScoped call injects $profileId.
func supersedePath(
	ctx context.Context,
	db contradictionTxRunner,
	profileID string,
	oldFacts []*domain.Fact,
	newClaimID string,
	newClaimValidFrom *time.Time,
) error {
	now := time.Now().UTC()
	validTo := now
	if newClaimValidFrom != nil {
		validTo = *newClaimValidFrom
	}

	return db.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		for _, old := range oldFacts {
			// 1. Mark fact superseded and close temporal range.
			result, err := neo4jstorage.RunScoped(ctx, tx, profileID,
				`MATCH (f:Fact {team_id: $profileId, fact_id: $factId})
                 SET f.status     = $status,
                     f.recorded_to = $recordedTo,
                     f.valid_to   = $validTo`,
				map[string]any{
					"factId":     old.FactID,
					"status":     string(domain.FactStatusSuperseded),
					"recordedTo": now,
					"validTo":    validTo,
				},
			)
			if err != nil {
				return fmt.Errorf("supersede fact %s: %w", old.FactID, err)
			}
			if _, err := result.Consume(ctx); err != nil {
				return fmt.Errorf("supersede consume %s: %w", old.FactID, err)
			}

			// 2. Create SUPERSEDED_BY relationship carrying team_id.
			result, err = neo4jstorage.RunScoped(ctx, tx, profileID,
				`MATCH (f:Fact {team_id: $profileId, fact_id: $factId}),
                       (c:Claim {team_id: $profileId, claim_id: $claimId})
                 CREATE (f)-[:SUPERSEDED_BY {team_id: $profileId}]->(c)`,
				map[string]any{
					"factId":  old.FactID,
					"claimId": newClaimID,
				},
			)
			if err != nil {
				return fmt.Errorf("create SUPERSEDED_BY for fact %s: %w", old.FactID, err)
			}
			if _, err := result.Consume(ctx); err != nil {
				return fmt.Errorf("SUPERSEDED_BY consume %s: %w", old.FactID, err)
			}
		}
		return nil
	})
}

// comparablePath marks the claim as disputed and creates a CONTRADICTS
// relationship from the Claim to each conflicting active Fact. The relationship
// carries team_id for isolation.
//
// "disputed" is the domain term for a claim with strength comparable to an
// existing Fact. No typed constant exists in domain.ClaimStatus; the string
// literal is used intentionally (same pattern as claimservice/verify.go).
//
// Profile isolation: every RunScoped call injects $profileId.
func comparablePath(
	ctx context.Context,
	db contradictionTxRunner,
	profileID string,
	claimID string,
	conflictingFacts []*domain.Fact,
) error {
	return db.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		// 1. Mark claim disputed.
		result, err := neo4jstorage.RunScoped(ctx, tx, profileID,
			`MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
             SET c.status = $status`,
			map[string]any{
				"claimId": claimID,
				"status":  string(domain.ClaimStatus("disputed")),
			},
		)
		if err != nil {
			return fmt.Errorf("mark claim disputed: %w", err)
		}
		if _, err := result.Consume(ctx); err != nil {
			return fmt.Errorf("consume disputed: %w", err)
		}

		// 2. Create CONTRADICTS relationship to each conflicting Fact.
		for _, f := range conflictingFacts {
			result, err = neo4jstorage.RunScoped(ctx, tx, profileID,
				`MATCH (c:Claim {team_id: $profileId, claim_id: $claimId}),
                       (f:Fact {team_id: $profileId, fact_id: $factId})
                 CREATE (c)-[:CONTRADICTS {team_id: $profileId}]->(f)`,
				map[string]any{
					"claimId": claimID,
					"factId":  f.FactID,
				},
			)
			if err != nil {
				return fmt.Errorf("create CONTRADICTS for fact %s: %w", f.FactID, err)
			}
			if _, err := result.Consume(ctx); err != nil {
				return fmt.Errorf("CONTRADICTS consume %s: %w", f.FactID, err)
			}
		}
		return nil
	})
}

// weakerPath marks the claim as rejected because an existing active Fact has
// higher strength. No relationship is created.
//
// Profile isolation: RunScoped injects $profileId.
func weakerPath(
	ctx context.Context,
	db contradictionTxRunner,
	profileID string,
	claimID string,
) error {
	return db.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		result, err := neo4jstorage.RunScoped(ctx, tx, profileID,
			`MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
             SET c.status = $status`,
			map[string]any{
				"claimId": claimID,
				"status":  string(domain.StatusRejected),
			},
		)
		if err != nil {
			return fmt.Errorf("mark claim rejected: %w", err)
		}
		_, err = result.Consume(ctx)
		return err
	})
}
