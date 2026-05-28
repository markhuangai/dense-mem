package factservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
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
	strVal := func(key string) string {
		v, _ := row[key].(string)
		return v
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

	var labels []string
	if raw, ok := row["labels"].([]any); ok {
		labels = make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				labels = append(labels, s)
			}
		}
	}

	var classification map[string]any
	if decoded := fragmentcodec.DecodeOptionalMap(row["classification"]); decoded != nil {
		classification = decoded
	} else if decoded := fragmentcodec.DecodeOptionalMap(row["classification_json"]); decoded != nil {
		classification = decoded
	}

	metadata := fragmentcodec.DecodeOptionalMap(row["metadata"])

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
				Speaker:           factStrFromMap(m, "speaker"),
				SpanStart:         factIntFromMap(m, "span_start"),
				SpanEnd:           factIntFromMap(m, "span_end"),
				ExtractConf:       factFloat64FromMap(m, "extract_conf"),
				ExtractionModel:   factStrFromMap(m, "extraction_model"),
				ExtractionVersion: factStrFromMap(m, "extraction_version"),
				PipelineRunID:     factStrFromMap(m, "pipeline_run_id"),
				Authority:         domain.Authority(factStrFromMap(m, "authority")),
			})
		}
	}

	return &domain.Fact{
		FactID:                strVal("fact_id"),
		ProfileID:             profileID,
		CreatedByProfileID:    strVal("created_by_profile_id"),
		CreatedByProfileName:  strVal("created_by_profile_name"),
		PromotedByProfileID:   strVal("promoted_by_profile_id"),
		PromotedByProfileName: strVal("promoted_by_profile_name"),

		Subject:   strVal("subject"),
		Predicate: strVal("predicate"),
		Object:    strVal("object"),

		Status:     domain.FactStatus(strVal("status")),
		TruthScore: float64Val("truth_score"),

		ValidFrom:       timePtr("valid_from"),
		ValidTo:         timePtr("valid_to"),
		RecordedAt:      timeVal("recorded_at"),
		RecordedTo:      timePtr("recorded_to"),
		RetractedAt:     timePtr("retracted_at"),
		LastConfirmedAt: timePtr("last_confirmed_at"),

		PromotedFromClaimID: strVal("promoted_from_claim_id"),

		Classification:               classification,
		ClassificationLatticeVersion: strVal("classification_lattice_version"),

		SourceQuality: float64Val("source_quality"),
		Labels:        labels,
		Metadata:      metadata,
		Evidence:      evidence,
	}
}

func factStrFromMap(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func factIntFromMap(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func factFloat64FromMap(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	}
	return 0
}
