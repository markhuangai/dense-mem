package factservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	"github.com/markhuangai/dense-mem/internal/service/graphrow"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// ErrFactNotFound is returned when a fact does not exist or belongs to a
// different profile. Both cases return the same error to prevent existence
// leakage across profiles (profile isolation invariant).
var ErrFactNotFound = errors.New("fact not found")

// factReader is the minimal Neo4j interface required by getFactServiceImpl.
//
// Profile isolation invariant: ScopedRead injects $profileId into every query;
// implementations MUST scope results to that profile. A missed filter is a
// tenant-escape vulnerability.
type factReader interface {
	ScopedRead(
		ctx context.Context,
		profileID string,
		query string,
		params map[string]any,
	) (neo4j.ResultSummary, []map[string]any, error)
}

// getFactServiceImpl implements GetFactService.
type getFactServiceImpl struct {
	reader factReader
}

// Compile-time check that getFactServiceImpl satisfies GetFactService.
var _ GetFactService = (*getFactServiceImpl)(nil)
var _ interface {
	GetByIDs(ctx context.Context, profileID string, factIDs []string) (map[string]*domain.Fact, error)
} = (*getFactServiceImpl)(nil)

// NewGetFactService constructs a ready-to-use GetFactService.
func NewGetFactService(reader factReader) GetFactService {
	return &getFactServiceImpl{reader: reader}
}

// getFactCypher retrieves a Fact node scoped to the given profile.
//
// Profile isolation: $profileId is injected automatically by ScopedRead and
// appears in the Fact node pattern. A fact belonging to a different profile
// produces zero rows — the caller receives ErrFactNotFound without any
// indication of whether the fact exists under another profile.
const getFactCypher = `
	MATCH (f:Fact {team_id: $profileId, fact_id: $factId})
OPTIONAL MATCH (f)<-[:PROMOTES_TO {team_id: $profileId}]-(c:Claim {team_id: $profileId})
OPTIONAL MATCH (c)-[r:SUPPORTED_BY {team_id: $profileId}]->(sf:SourceFragment {team_id: $profileId})
WITH f, collect(CASE
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
END) AS evidence
OPTIONAL MATCH (incomingOverlay:Fact {team_id: $profileId, status: 'active'})-[:OVERLAYS {team_id: $profileId}]->(f)
WITH f, evidence, count(DISTINCT incomingOverlay) AS incoming_overlay_count
OPTIONAL MATCH (f)-[:OVERLAYS {team_id: $profileId}]->(outgoingOverlay:Fact {team_id: $profileId, status: 'active'})
WITH f, evidence, incoming_overlay_count, count(DISTINCT outgoingOverlay) AS outgoing_overlay_count
RETURN
    f.fact_id                        AS fact_id,
    f.owner_profile_id               AS owner_profile_id,
    f.owner_profile_name             AS owner_profile_name,
    f.created_by_profile_id          AS created_by_profile_id,
    f.created_by_profile_name        AS created_by_profile_name,
    f.promoted_by_profile_id         AS promoted_by_profile_id,
    f.promoted_by_profile_name       AS promoted_by_profile_name,
    f.subject                        AS subject,
    f.predicate                      AS predicate,
    f.object                         AS object,
    f.status                         AS status,
    CASE
        WHEN outgoing_overlay_count > 0 THEN 'overlay'
        WHEN incoming_overlay_count > 0 THEN 'conflicted'
        ELSE 'authoritative'
    END                              AS authority_state,
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
	    f.metadata                       AS metadata,
	    evidence                         AS evidence`

const getFactsByIDCypher = `
	MATCH (f:Fact {team_id: $profileId})
	WHERE f.fact_id IN $factIds
	OPTIONAL MATCH (f)<-[:PROMOTES_TO {team_id: $profileId}]-(c:Claim {team_id: $profileId})
	OPTIONAL MATCH (c)-[r:SUPPORTED_BY {team_id: $profileId}]->(sf:SourceFragment {team_id: $profileId})
	WITH f, collect(CASE
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
	END) AS evidence
	OPTIONAL MATCH (incomingOverlay:Fact {team_id: $profileId, status: 'active'})-[:OVERLAYS {team_id: $profileId}]->(f)
	WITH f, evidence, count(DISTINCT incomingOverlay) AS incoming_overlay_count
	OPTIONAL MATCH (f)-[:OVERLAYS {team_id: $profileId}]->(outgoingOverlay:Fact {team_id: $profileId, status: 'active'})
	WITH f, evidence, incoming_overlay_count, count(DISTINCT outgoingOverlay) AS outgoing_overlay_count
	RETURN
	    f.fact_id                        AS fact_id,
	    f.owner_profile_id               AS owner_profile_id,
	    f.owner_profile_name             AS owner_profile_name,
	    f.created_by_profile_id          AS created_by_profile_id,
	    f.created_by_profile_name        AS created_by_profile_name,
	    f.promoted_by_profile_id         AS promoted_by_profile_id,
	    f.promoted_by_profile_name       AS promoted_by_profile_name,
	    f.subject                        AS subject,
	    f.predicate                      AS predicate,
	    f.object                         AS object,
	    f.status                         AS status,
	    CASE
	        WHEN outgoing_overlay_count > 0 THEN 'overlay'
	        WHEN incoming_overlay_count > 0 THEN 'conflicted'
	        ELSE 'authoritative'
	    END                              AS authority_state,
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
	    f.metadata                       AS metadata,
	    evidence                         AS evidence`

// Get retrieves the fact identified by factID within profileID.
//
// Returns ErrFactNotFound when the fact does not exist or belongs to a
// different profile. Existence under other profiles is never leaked.
func (s *getFactServiceImpl) Get(ctx context.Context, profileID string, factID string) (*domain.Fact, error) {
	_, rows, err := s.reader.ScopedRead(ctx, profileID, getFactCypher, map[string]any{
		"factId": factID,
	})
	if err != nil {
		return nil, fmt.Errorf("fact get: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrFactNotFound
	}
	return rowToFact(profileID, rows[0]), nil
}

// GetByIDs retrieves facts by ID with one profile-scoped read. Missing IDs are
// omitted from the returned map.
func (s *getFactServiceImpl) GetByIDs(ctx context.Context, profileID string, factIDs []string) (map[string]*domain.Fact, error) {
	out := make(map[string]*domain.Fact, len(factIDs))
	if len(factIDs) == 0 {
		return out, nil
	}
	_, rows, err := s.reader.ScopedRead(ctx, profileID, getFactsByIDCypher, map[string]any{
		"factIds": factIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("fact batch get: %w", err)
	}
	for _, row := range rows {
		fact := rowToFact(profileID, row)
		if fact.FactID != "" {
			out[fact.FactID] = fact
		}
	}
	return out, nil
}

// rowToFact maps a single Neo4j result row (keyed by RETURN aliases) to a
// domain.Fact. profileID is propagated from the service call rather than read
// from the row — ScopedRead has already enforced profile isolation at the
// query level, so the row is guaranteed to belong to that profile.
func rowToFact(profileID string, row map[string]any) *domain.Fact {
	var classification map[string]any
	if decoded := fragmentcodec.DecodeOptionalMap(row["classification"]); decoded != nil {
		classification = decoded
	} else if decoded := fragmentcodec.DecodeOptionalMap(row["classification_json"]); decoded != nil {
		classification = decoded
	}

	metadata := fragmentcodec.DecodeOptionalMap(row["metadata"])

	return &domain.Fact{
		FactID:                graphrow.String(row, "fact_id"),
		ProfileID:             profileID,
		OwnerProfileID:        firstNonEmpty(graphrow.String(row, "owner_profile_id"), graphrow.String(row, "created_by_profile_id"), graphrow.String(row, "promoted_by_profile_id")),
		OwnerProfileName:      firstNonEmpty(graphrow.String(row, "owner_profile_name"), graphrow.String(row, "created_by_profile_name"), graphrow.String(row, "promoted_by_profile_name")),
		CreatedByProfileID:    graphrow.String(row, "created_by_profile_id"),
		CreatedByProfileName:  graphrow.String(row, "created_by_profile_name"),
		PromotedByProfileID:   graphrow.String(row, "promoted_by_profile_id"),
		PromotedByProfileName: graphrow.String(row, "promoted_by_profile_name"),

		Subject:   graphrow.String(row, "subject"),
		Predicate: graphrow.String(row, "predicate"),
		Object:    graphrow.String(row, "object"),

		Status:         domain.FactStatus(graphrow.String(row, "status")),
		AuthorityState: graphrow.String(row, "authority_state"),
		TruthScore:     graphrow.Float64(row, "truth_score"),

		ValidFrom:       graphrow.TimePtr(row, "valid_from"),
		ValidTo:         graphrow.TimePtr(row, "valid_to"),
		RecordedAt:      graphrow.Time(row, "recorded_at"),
		RecordedTo:      graphrow.TimePtr(row, "recorded_to"),
		RetractedAt:     graphrow.TimePtr(row, "retracted_at"),
		LastConfirmedAt: graphrow.TimePtr(row, "last_confirmed_at"),

		PromotedFromClaimID: graphrow.String(row, "promoted_from_claim_id"),

		Classification:               classification,
		ClassificationLatticeVersion: graphrow.String(row, "classification_lattice_version"),

		SourceQuality: graphrow.Float64(row, "source_quality"),
		Labels:        graphrow.StringSlice(row, "labels"),
		Metadata:      metadata,
		Evidence:      graphrow.Evidence(row, "evidence"),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
