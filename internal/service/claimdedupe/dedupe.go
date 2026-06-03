// Package claimdedupe provides deduplication lookup for claims.
package claimdedupe

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
)

// ScopedReader defines the profile-scoped read surface required by this package.
// It mirrors the shape used by other dedupe helpers while avoiding an import
// cycle on the concrete Neo4j package.
type ScopedReader interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (any, []map[string]any, error)
}

// DedupeLookup defines the claim dedupe lookup surface used by claim creation.
type DedupeLookup interface {
	ByIdempotencyKey(ctx context.Context, profileID, key string) (*domain.Claim, error)
	ByContentHash(ctx context.Context, profileID, hash string) (*domain.Claim, error)
}

type neo4jDedupeLookup struct {
	reader ScopedReader
}

// Ensure neo4jDedupeLookup implements DedupeLookup.
var _ DedupeLookup = (*neo4jDedupeLookup)(nil)

// NewNeo4jDedupeLookup creates a claim dedupe lookup backed by profile-scoped reads.
func NewNeo4jDedupeLookup(reader ScopedReader) DedupeLookup {
	return &neo4jDedupeLookup{reader: reader}
}

const claimLookupReturn = `
OPTIONAL MATCH (c)-[:SUPPORTED_BY {team_id: $profileId}]->(sf:SourceFragment {team_id: $profileId})
WITH c, collect(sf.fragment_id) AS supported_by
RETURN
    c.claim_id                        AS claim_id,
    c.created_by_profile_id           AS created_by_profile_id,
    c.created_by_profile_name         AS created_by_profile_name,
    c.subject                         AS subject,
    c.predicate                       AS predicate,
    c.object                          AS object,
    c.modality                        AS modality,
    c.polarity                        AS polarity,
    c.speaker                         AS speaker,
    c.span_start                      AS span_start,
    c.span_end                        AS span_end,
    c.valid_from                      AS valid_from,
    c.valid_to                        AS valid_to,
    c.recorded_at                     AS recorded_at,
    c.recorded_to                     AS recorded_to,
    c.extract_conf                    AS extract_conf,
    c.resolution_conf                 AS resolution_conf,
    c.source_quality                  AS source_quality,
    c.entailment_verdict              AS entailment_verdict,
    c.status                          AS status,
    c.last_verifier_response          AS last_verifier_response,
    c.verified_at                     AS verified_at,
    c.extraction_model                AS extraction_model,
    c.extraction_version              AS extraction_version,
    c.verifier_model                  AS verifier_model,
    c.pipeline_run_id                 AS pipeline_run_id,
    c.content_hash                    AS content_hash,
    c.idempotency_key                 AS idempotency_key,
    c.classification                  AS classification,
    c.classification_json             AS classification_json,
    c.classification_lattice_version  AS classification_lattice_version,
    c.owner_profile_id                AS owner_profile_id,
    c.owner_profile_name              AS owner_profile_name,
    supported_by                      AS supported_by
LIMIT 1`

const byIdempotencyKeyQuery = `
MATCH (c:Claim {team_id: $profileId, idempotency_key: $key})
WHERE ($ownerProfileId = '' OR coalesce(c.owner_profile_id, c.created_by_profile_id, '') = $ownerProfileId)
` + claimLookupReturn

const byContentHashQuery = `
MATCH (c:Claim {team_id: $profileId, content_hash: $hash})
WHERE ($ownerProfileId = '' OR coalesce(c.owner_profile_id, c.created_by_profile_id, '') = $ownerProfileId)
` + claimLookupReturn

// ByIdempotencyKey finds an existing claim by idempotency key within a profile.
func (l *neo4jDedupeLookup) ByIdempotencyKey(ctx context.Context, profileID, key string) (*domain.Claim, error) {
	_, rows, err := l.reader.ScopedRead(ctx, profileID, byIdempotencyKeyQuery, map[string]any{
		"key":            key,
		"ownerProfileId": ownerProfileIDFromContext(ctx),
	})
	if err != nil {
		return nil, fmt.Errorf("claim dedupe by idempotency key: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rowToClaim(profileID, rows[0]), nil
}

// ByContentHash finds an existing claim by content hash within a profile.
func (l *neo4jDedupeLookup) ByContentHash(ctx context.Context, profileID, hash string) (*domain.Claim, error) {
	_, rows, err := l.reader.ScopedRead(ctx, profileID, byContentHashQuery, map[string]any{
		"hash":           hash,
		"ownerProfileId": ownerProfileIDFromContext(ctx),
	})
	if err != nil {
		return nil, fmt.Errorf("claim dedupe by content hash: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rowToClaim(profileID, rows[0]), nil
}

// rowToClaim reuses the claimservice row mapping so dedupe hits return the same
// shape as direct reads.
func rowToClaim(profileID string, row map[string]any) *domain.Claim {
	return claimservice.RowToClaimForExternalUse(profileID, row)
}

func ownerProfileIDFromContext(ctx context.Context) string {
	ownerID, _, _ := requestctx.ActorOwner(ctx)
	return ownerID
}
