package claimservice

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
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
	strVal := func(key string) string {
		v, _ := row[key].(string)
		return v
	}

	intVal := func(key string) int {
		switch v := row[key].(type) {
		case int64:
			return int(v)
		case int:
			return v
		}
		return 0
	}

	float64Val := func(key string) float64 {
		v, _ := row[key].(float64)
		return v
	}

	// timePtr returns nil when the property is absent or not a time.Time.
	timePtr := func(key string) *time.Time {
		v, ok := row[key].(time.Time)
		if !ok {
			return nil
		}
		return &v
	}

	// timeVal returns a zero time.Time when the property is absent.
	timeVal := func(key string) time.Time {
		v, _ := row[key].(time.Time)
		return v
	}

	// supported_by is the result of collect(sf.fragment_id); the driver returns
	// it as []any. Filter out empty strings that Neo4j may emit when the OPTIONAL
	// MATCH found no matching SourceFragment (collect on a null produces []).
	var supportedBy []string
	if raw, ok := row["supported_by"].([]any); ok {
		supportedBy = make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				supportedBy = append(supportedBy, s)
			}
		}
	}

	var classification map[string]any
	if decoded := fragmentcodec.DecodeOptionalMap(row["classification"]); decoded != nil {
		classification = decoded
	} else if decoded := fragmentcodec.DecodeOptionalMap(row["classification_json"]); decoded != nil {
		classification = decoded
	}

	var evidence []domain.Evidence
	if raw, ok := row["evidence"].([]any); ok {
		evidence = make([]domain.Evidence, 0, len(raw))
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			fragmentID, _ := m["fragment_id"].(string)
			if fragmentID == "" {
				continue
			}
			evidence = append(evidence, domain.Evidence{
				FragmentID:        fragmentID,
				Speaker:           strFromMap(m, "speaker"),
				SpanStart:         intFromMap(m, "span_start"),
				SpanEnd:           intFromMap(m, "span_end"),
				ExtractConf:       float64FromMap(m, "extract_conf"),
				ExtractionModel:   strFromMap(m, "extraction_model"),
				ExtractionVersion: strFromMap(m, "extraction_version"),
				PipelineRunID:     strFromMap(m, "pipeline_run_id"),
				Authority:         domain.Authority(strFromMap(m, "authority")),
			})
		}
	}

	return &domain.Claim{
		ClaimID:              strVal("claim_id"),
		ProfileID:            profileID,
		CreatedByProfileID:   strVal("created_by_profile_id"),
		CreatedByProfileName: strVal("created_by_profile_name"),

		Subject:   strVal("subject"),
		Predicate: strVal("predicate"),
		Object:    strVal("object"),

		Modality:  domain.ClaimModality(strVal("modality")),
		Polarity:  domain.ClaimPolarity(strVal("polarity")),
		Speaker:   strVal("speaker"),
		SpanStart: intVal("span_start"),
		SpanEnd:   intVal("span_end"),

		ValidFrom:  timePtr("valid_from"),
		ValidTo:    timePtr("valid_to"),
		RecordedAt: timeVal("recorded_at"),
		RecordedTo: timePtr("recorded_to"),

		ExtractConf:    float64Val("extract_conf"),
		ResolutionConf: float64Val("resolution_conf"),
		SourceQuality:  float64Val("source_quality"),

		EntailmentVerdict:    domain.EntailmentVerdict(strVal("entailment_verdict")),
		Status:               domain.ClaimStatus(strVal("status")),
		LastVerifierResponse: strVal("last_verifier_response"),
		VerifiedAt:           timePtr("verified_at"),

		ExtractionModel:   strVal("extraction_model"),
		ExtractionVersion: strVal("extraction_version"),
		VerifierModel:     strVal("verifier_model"),
		PipelineRunID:     strVal("pipeline_run_id"),

		ContentHash:    strVal("content_hash"),
		IdempotencyKey: strVal("idempotency_key"),

		Classification:               classification,
		ClassificationLatticeVersion: strVal("classification_lattice_version"),

		SupportedBy: supportedBy,
		Evidence:    evidence,
	}
}

func strFromMap(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func intFromMap(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func float64FromMap(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	}
	return 0
}
