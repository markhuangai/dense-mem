package claimservice

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	"github.com/markhuangai/dense-mem/internal/service/graphrow"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// claimReader is the minimal Neo4j interface required by getClaimServiceImpl.
//
// Profile isolation invariant: ScopedRead injects $profileId into every query;
// implementations MUST scope results to that profile. A missed filter here is
// a tenant-escape vulnerability.
type claimReader interface {
	ScopedRead(
		ctx context.Context,
		profileID string,
		query string,
		params map[string]any,
	) (neo4j.ResultSummary, []map[string]any, error)
}

// getClaimServiceImpl implements GetClaimService.
type getClaimServiceImpl struct {
	reader claimReader
	logger *slog.Logger
}

// Compile-time check that getClaimServiceImpl satisfies GetClaimService.
var _ GetClaimService = (*getClaimServiceImpl)(nil)
var _ interface {
	GetByIDs(ctx context.Context, profileID string, claimIDs []string) (map[string]*domain.Claim, error)
} = (*getClaimServiceImpl)(nil)

// NewGetClaimService constructs a ready-to-use GetClaimService.
// logger may be nil; an absent logger emits no structured log lines.
func NewGetClaimService(reader claimReader, logger *slog.Logger) GetClaimService {
	return &getClaimServiceImpl{reader: reader, logger: logger}
}

// getClaimCypher fetches a Claim node and collects the IDs of all
// SourceFragment nodes reachable via outgoing SUPPORTED_BY edges from the
// Claim — i.e. (c)-[:SUPPORTED_BY]->(sf:SourceFragment) — in a single read.
//
// Edge direction: AC-14 binds SUPPORTED_BY as an outgoing edge from Claim to
// SourceFragment. The OPTIONAL MATCH therefore starts from (c) and traverses
// to (sf), not the reverse.
//
// Profile isolation: $profileId is injected automatically by ScopedRead and
// appears on both the Claim node pattern and the SUPPORTED_BY relationship
// pattern. This prevents cross-profile leakage through relationship traversal —
// a SUPPORTED_BY edge whose team_id differs from the Claim's team_id is
// silently excluded from the collect().
//
// OPTIONAL MATCH on SourceFragment ensures that a Claim with no supporting
// fragments still produces one result row (supported_by = []). When the Claim
// node itself is absent, Neo4j produces zero rows; Get() maps that to
// ErrClaimNotFound without leaking that the claim exists under a different
// profile.
const getClaimCypher = `
	MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
OPTIONAL MATCH (c)-[r:SUPPORTED_BY {team_id: $profileId}]->(sf:SourceFragment {team_id: $profileId})
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
    collect(sf.fragment_id)           AS supported_by,
    collect(CASE
        WHEN sf.fragment_id IS NULL THEN NULL
        ELSE {
            fragment_id: sf.fragment_id,
            speaker: r.speaker,
            span_start: r.span_start,
            span_end: r.span_end,
            extract_conf: r.extract_conf,
            extraction_model: r.extraction_model,
            extraction_version: r.extraction_version,
            pipeline_run_id: r.pipeline_run_id,
            authority: coalesce(r.authority, sf.authority, 'unknown')
        }
	    END) AS evidence`

const getClaimsByIDCypher = `
	MATCH (c:Claim {team_id: $profileId})
	WHERE c.claim_id IN $claimIds
	OPTIONAL MATCH (c)-[r:SUPPORTED_BY {team_id: $profileId}]->(sf:SourceFragment {team_id: $profileId})
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
	    collect(sf.fragment_id)           AS supported_by,
	    collect(CASE
	        WHEN sf.fragment_id IS NULL THEN NULL
	        ELSE {
	            fragment_id: sf.fragment_id,
	            speaker: r.speaker,
	            span_start: r.span_start,
	            span_end: r.span_end,
	            extract_conf: r.extract_conf,
	            extraction_model: r.extraction_model,
	            extraction_version: r.extraction_version,
	            pipeline_run_id: r.pipeline_run_id,
	            authority: coalesce(r.authority, sf.authority, 'unknown')
	        }
	    END) AS evidence`

// Get retrieves the claim identified by claimID within profileID.
//
// Returns ErrClaimNotFound when the claim does not exist or belongs to a
// different profile. Existence under other profiles is never leaked; the caller
// always receives the same error regardless of the cause of the miss.
func (s *getClaimServiceImpl) Get(ctx context.Context, profileID string, claimID string) (*domain.Claim, error) {
	_, rows, err := s.reader.ScopedRead(ctx, profileID, getClaimCypher, map[string]any{
		"claimId": claimID,
	})
	if err != nil {
		return nil, fmt.Errorf("claim get: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrClaimNotFound
	}
	return rowToClaim(profileID, rows[0]), nil
}

// GetByIDs retrieves claims by ID with one profile-scoped read. Missing IDs are
// omitted from the returned map.
func (s *getClaimServiceImpl) GetByIDs(ctx context.Context, profileID string, claimIDs []string) (map[string]*domain.Claim, error) {
	out := make(map[string]*domain.Claim, len(claimIDs))
	if len(claimIDs) == 0 {
		return out, nil
	}
	_, rows, err := s.reader.ScopedRead(ctx, profileID, getClaimsByIDCypher, map[string]any{
		"claimIds": claimIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("claim batch get: %w", err)
	}
	for _, row := range rows {
		claim := rowToClaim(profileID, row)
		if claim.ClaimID != "" {
			out[claim.ClaimID] = claim
		}
	}
	return out, nil
}

// rowToClaim maps a single Neo4j result row (keyed by RETURN aliases) to a
// domain.Claim. profileID is propagated from the service call rather than read
// from the row — ScopedRead has already enforced profile isolation at the
// query level, so the row is guaranteed to belong to that profile.
//
// Type coercions follow the neo4j-go-driver/v5 conventions:
//   - integers arrive as int64
//   - floats as float64
//   - temporal values as time.Time
//   - lists from collect() as []any
//   - maps as map[string]any
func rowToClaim(profileID string, row map[string]any) *domain.Claim {
	// supported_by is the result of collect(sf.fragment_id); the driver returns
	// it as []any. Filter out empty strings that Neo4j may emit when the OPTIONAL
	// MATCH found no matching SourceFragment (collect on a null produces []).
	supportedBy := graphrow.StringSlice(row, "supported_by")

	var classification map[string]any
	if decoded := fragmentcodec.DecodeOptionalMap(row["classification"]); decoded != nil {
		classification = decoded
	} else if decoded := fragmentcodec.DecodeOptionalMap(row["classification_json"]); decoded != nil {
		classification = decoded
	}

	return &domain.Claim{
		ClaimID:              graphrow.String(row, "claim_id"),
		ProfileID:            profileID,
		OwnerProfileID:       graphrow.FirstNonEmpty(graphrow.String(row, "owner_profile_id"), graphrow.String(row, "created_by_profile_id")),
		OwnerProfileName:     graphrow.FirstNonEmpty(graphrow.String(row, "owner_profile_name"), graphrow.String(row, "created_by_profile_name")),
		CreatedByProfileID:   graphrow.String(row, "created_by_profile_id"),
		CreatedByProfileName: graphrow.String(row, "created_by_profile_name"),

		Subject:   graphrow.String(row, "subject"),
		Predicate: graphrow.String(row, "predicate"),
		Object:    graphrow.String(row, "object"),

		Modality:  domain.ClaimModality(graphrow.String(row, "modality")),
		Polarity:  domain.ClaimPolarity(graphrow.String(row, "polarity")),
		Speaker:   graphrow.String(row, "speaker"),
		SpanStart: graphrow.Int(row, "span_start"),
		SpanEnd:   graphrow.Int(row, "span_end"),

		ValidFrom:  graphrow.TimePtr(row, "valid_from"),
		ValidTo:    graphrow.TimePtr(row, "valid_to"),
		RecordedAt: graphrow.Time(row, "recorded_at"),
		RecordedTo: graphrow.TimePtr(row, "recorded_to"),

		ExtractConf:    graphrow.Float64(row, "extract_conf"),
		ResolutionConf: graphrow.Float64(row, "resolution_conf"),
		SourceQuality:  graphrow.Float64(row, "source_quality"),

		EntailmentVerdict:    domain.EntailmentVerdict(graphrow.String(row, "entailment_verdict")),
		Status:               domain.ClaimStatus(graphrow.String(row, "status")),
		LastVerifierResponse: graphrow.String(row, "last_verifier_response"),
		VerifiedAt:           graphrow.TimePtr(row, "verified_at"),

		ExtractionModel:   graphrow.String(row, "extraction_model"),
		ExtractionVersion: graphrow.String(row, "extraction_version"),
		VerifierModel:     graphrow.String(row, "verifier_model"),
		PipelineRunID:     graphrow.String(row, "pipeline_run_id"),

		ContentHash:    graphrow.String(row, "content_hash"),
		IdempotencyKey: graphrow.String(row, "idempotency_key"),

		Classification:               classification,
		ClassificationLatticeVersion: graphrow.String(row, "classification_lattice_version"),

		SupportedBy: supportedBy,
		Evidence:    graphrow.Evidence(row, "evidence"),
	}
}
