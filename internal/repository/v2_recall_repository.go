package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	defaultV2RecallLimit      = 10
	maxV2RecallLimit          = 50
	v2RecallOverfetchMultiple = 5
	v2RecallOverfetchFloor    = 50
	v2RecallRRFConstant       = 60
)

var _ V2RecallRepository = (*V2SearchRepositoryImpl)(nil)

func (r *V2SearchRepositoryImpl) RecallEvidence(ctx context.Context, input V2RecallEvidenceInput) (*V2RecallEvidenceResult, error) {
	input = normalizeV2RecallEvidenceInput(input)
	if err := validateV2RecallEvidenceInput(input); err != nil {
		return nil, err
	}
	profile, err := r.GetActiveSearchProfile(ctx, input.ProfileKey)
	if err != nil {
		return nil, err
	}
	overfetch := v2RecallOverfetchLimit(input.Limit)
	acc := map[string]*v2RecallCandidate{}
	var textHits, vectorHits, expansionHits []V2SearchHit
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		if input.Query != "" {
			textHits, err = searchV2RecallFullText(ctx, tx, input, profile, overfetch)
			if err != nil {
				return err
			}
		}
		if len(input.QueryEmbedding) > 0 {
			vectorHits, err = searchV2RecallExactVector(ctx, tx, input, profile, overfetch)
			if err != nil {
				return err
			}
		}
		if len(input.ExpandFromEntityIDs) > 0 {
			expansionHits, err = searchV2RecallEntityExpansion(ctx, tx, input, profile, overfetch)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 recall: search evidence: %w", err)
	}
	knownEvidence := v2RecallStringSet(input.KnownEvidenceIDs)
	addV2RecallBranch(acc, textHits, knownEvidence, 1)
	addV2RecallBranch(acc, vectorHits, knownEvidence, 1)
	addV2RecallBranch(acc, expansionHits, knownEvidence, 0.5)
	candidates := sortedV2RecallCandidates(acc)
	if len(candidates) == 0 {
		return &V2RecallEvidenceResult{
			TeamID:      input.TeamID,
			SearchState: string(domain.V2SearchProjectionCurrent),
			Results:     []V2RecallEvidenceHit{},
		}, nil
	}
	candidateIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidateIDs = append(candidateIDs, candidate.EvidenceID)
	}
	hydrated := map[string]V2RecallEvidenceHit{}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		hydrated, err = hydrateV2RecallEvidence(ctx, tx, input, profile, candidateIDs)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("v2 recall: hydrate evidence: %w", err)
	}
	results := make([]V2RecallEvidenceHit, 0, input.Limit)
	searchState := string(domain.V2SearchProjectionCurrent)
	for _, candidate := range candidates {
		hit, ok := hydrated[candidate.EvidenceID]
		if !ok {
			continue
		}
		hit.Score = candidate.Score
		hit.SearchState = v2RecallCombinedSearchState(candidate.SearchState, hit.SearchState)
		if hit.SearchState == string(domain.V2SearchProjectionPending) {
			searchState = string(domain.V2SearchProjectionPending)
		}
		hit.Rank = len(results) + 1
		results = append(results, hit)
		if len(results) == input.Limit {
			break
		}
	}
	return &V2RecallEvidenceResult{
		TeamID:      input.TeamID,
		SearchState: searchState,
		Results:     results,
	}, nil
}

type v2RecallCandidate struct {
	EvidenceID  string
	Score       float64
	SearchState string
}

func searchV2RecallFullText(
	ctx context.Context,
	tx *gorm.DB,
	input V2RecallEvidenceInput,
	profile *V2SearchProfile,
	limit int,
) ([]V2SearchHit, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, search_document_id::text, source_kind, source_id::text,
		       source_version, document_version, embedding_contract_id::text,
		       search_state,
		       0::double precision AS distance,
		       ts_rank_cd(search_tsv, plainto_tsquery('simple', ?))::double precision AS text_rank
		FROM search_documents
		WHERE team_id = ?::uuid
		  AND source_kind = 'evidence'
		  AND embedding_contract_id = ?::uuid
		  AND search_state IN ('pending', 'current')
		  AND search_tsv @@ plainto_tsquery('simple', ?)
		ORDER BY text_rank DESC, updated_at DESC, search_document_id ASC
		LIMIT ?
	`, input.Query, input.TeamID, profile.EmbeddingContractID, input.Query, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanV2SearchHits(rows)
}

func searchV2RecallExactVector(
	ctx context.Context,
	tx *gorm.DB,
	input V2RecallEvidenceInput,
	profile *V2SearchProfile,
	limit int,
) ([]V2SearchHit, error) {
	if len(input.QueryEmbedding) != profile.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: profile dimensions %d, query dimensions %d", ErrV2SearchProfileMismatch, profile.EmbeddingDimensions, len(input.QueryEmbedding))
	}
	vectorLiteral, err := v2VectorLiteral(input.QueryEmbedding)
	if err != nil {
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, search_document_id::text, source_kind, source_id::text,
		       source_version, document_version, embedding_contract_id::text,
		       search_state,
		       (embedding <=> ?::vector)::double precision AS distance,
		       0::double precision AS text_rank
		FROM search_documents
		WHERE team_id = ?::uuid
		  AND source_kind = 'evidence'
		  AND embedding_contract_id = ?::uuid
		  AND embedding_dimensions = ?
		  AND search_state = 'current'
		  AND embedding IS NOT NULL
		ORDER BY embedding <=> ?::vector ASC, search_document_id ASC
		LIMIT ?
	`, vectorLiteral, input.TeamID, profile.EmbeddingContractID,
		profile.EmbeddingDimensions, vectorLiteral, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanV2SearchHits(rows)
}

func searchV2RecallEntityExpansion(
	ctx context.Context,
	tx *gorm.DB,
	input V2RecallEvidenceInput,
	profile *V2SearchProfile,
	limit int,
) ([]V2SearchHit, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH latest_support_decision AS (
			SELECT DISTINCT ON (team_id, support_id)
			       team_id, support_id, decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
		)
		SELECT document.team_id::text,
		       document.search_document_id::text,
		       document.source_kind,
		       document.source_id::text,
		       document.source_version,
		       document.document_version,
		       document.embedding_contract_id::text,
		       document.search_state,
		       0::double precision AS distance,
		       0::double precision AS text_rank
		FROM relationship_records AS relationship
		JOIN relationship_evidence_supports AS support
		  ON support.team_id = relationship.team_id
		 AND support.relationship_id = relationship.relationship_id
		JOIN latest_support_decision AS latest
		  ON latest.team_id = support.team_id
		 AND latest.support_id = support.support_id
		 AND latest.decision IN ('grant', 'reinstate')
		JOIN search_documents AS document
		  ON document.team_id = support.team_id
		 AND document.source_kind = 'evidence'
		 AND document.source_id = support.fragment_id
		 AND document.embedding_contract_id = ?::uuid
		 AND document.search_state IN ('pending', 'current')
		LEFT JOIN evidence_quarantines AS quarantine
		  ON quarantine.team_id = support.team_id
		 AND quarantine.fragment_id = support.fragment_id
		 AND quarantine.status = 'active'
		WHERE relationship.team_id = ?::uuid
		  AND relationship.status = 'active'
		  AND relationship.tier IN ('validated_claim', 'fact')
		  AND quarantine.quarantine_id IS NULL
		  AND (
		      relationship.subject_entity_id = ANY(?::uuid[])
		      OR relationship.object_entity_id = ANY(?::uuid[])
		  )
		  AND (
		      ?::timestamptz IS NULL
		      OR ((relationship.valid_from IS NULL OR relationship.valid_from <= ?::timestamptz)
		          AND (relationship.valid_to IS NULL OR relationship.valid_to > ?::timestamptz))
		  )
		  AND (
		      ?::timestamptz IS NULL
		      OR (relationship.created_at <= ?::timestamptz
		          AND support.created_at <= ?::timestamptz
		          AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz))
		  )
		  AND (
		      cardinality(?::uuid[]) = 0
		      OR relationship.relationship_id <> ALL(?::uuid[])
		  )
		GROUP BY document.team_id, document.search_document_id, document.source_kind,
		         document.source_id, document.source_version, document.document_version,
		         document.embedding_contract_id, document.search_state
		ORDER BY max(relationship.updated_at) DESC, document.search_document_id ASC
		LIMIT ?
	`, input.TeamID, profile.EmbeddingContractID, input.TeamID,
		pq.Array(input.ExpandFromEntityIDs), pq.Array(input.ExpandFromEntityIDs),
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt,
		pq.Array(input.KnownRelationshipIDs), pq.Array(input.KnownRelationshipIDs),
		limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanV2SearchHits(rows)
}

func hydrateV2RecallEvidence(
	ctx context.Context,
	tx *gorm.DB,
	input V2RecallEvidenceInput,
	profile *V2SearchProfile,
	evidenceIDs []string,
) (map[string]V2RecallEvidenceHit, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH requested AS (
			SELECT unnest(?::uuid[]) AS fragment_id
		),
		latest_support_decision AS (
			SELECT DISTINCT ON (team_id, support_id)
			       team_id, support_id, decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
		),
		eligible AS (
			SELECT fragment.fragment_id::text AS evidence_id,
			       fragment.content AS context,
			       document.search_state,
			       relationship.relationship_id::text AS relationship_id
			FROM requested
			JOIN evidence_fragments AS fragment
			  ON fragment.team_id = ?::uuid
			 AND fragment.fragment_id = requested.fragment_id
			JOIN search_documents AS document
			  ON document.team_id = fragment.team_id
			 AND document.source_kind = 'evidence'
			 AND document.source_id = fragment.fragment_id
			 AND document.embedding_contract_id = ?::uuid
			 AND document.search_state IN ('pending', 'current')
			JOIN relationship_evidence_supports AS support
			  ON support.team_id = fragment.team_id
			 AND support.fragment_id = fragment.fragment_id
			JOIN latest_support_decision AS latest
			  ON latest.team_id = support.team_id
			 AND latest.support_id = support.support_id
			 AND latest.decision IN ('grant', 'reinstate')
			JOIN relationship_records AS relationship
			  ON relationship.team_id = support.team_id
			 AND relationship.relationship_id = support.relationship_id
			 AND relationship.status = 'active'
			 AND relationship.tier IN ('validated_claim', 'fact')
			LEFT JOIN evidence_quarantines AS quarantine
			  ON quarantine.team_id = fragment.team_id
			 AND quarantine.fragment_id = fragment.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_sources AS source
			  ON source.team_id = support.team_id
			 AND source.source_id = support.source_id
			WHERE quarantine.quarantine_id IS NULL
			  AND (
			      support.source_id IS NULL
			      OR source.current_revision_id = support.source_revision_id
			  )
			  AND (
			      ?::timestamptz IS NULL
			      OR ((relationship.valid_from IS NULL OR relationship.valid_from <= ?::timestamptz)
			          AND (relationship.valid_to IS NULL OR relationship.valid_to > ?::timestamptz))
			  )
			  AND (
			      ?::timestamptz IS NULL
			      OR (fragment.created_at <= ?::timestamptz
			          AND relationship.created_at <= ?::timestamptz
			          AND support.created_at <= ?::timestamptz
			          AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz))
			  )
			  AND (
			      cardinality(?::uuid[]) = 0
			      OR relationship.relationship_id <> ALL(?::uuid[])
			  )
		)
		SELECT evidence_id,
		       max(context) AS context,
		       array_agg(DISTINCT relationship_id ORDER BY relationship_id) AS relationship_ids,
		       CASE
		           WHEN bool_or(search_state = 'pending') THEN 'pending'
		           ELSE 'current'
		       END AS search_state
		FROM eligible
		GROUP BY evidence_id
	`, pq.Array(evidenceIDs), input.TeamID, input.TeamID, profile.EmbeddingContractID,
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt,
		pq.Array(input.KnownRelationshipIDs), pq.Array(input.KnownRelationshipIDs)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]V2RecallEvidenceHit)
	for rows.Next() {
		var evidenceID, context, searchState string
		var relationshipIDs pq.StringArray
		if err := rows.Scan(&evidenceID, &context, &relationshipIDs, &searchState); err != nil {
			return nil, err
		}
		out[evidenceID] = V2RecallEvidenceHit{
			TeamID:          input.TeamID,
			EvidenceID:      evidenceID,
			RelationshipIDs: []string(relationshipIDs),
			Context:         truncateV2RecallContext(context),
			SearchState:     searchState,
		}
	}
	return out, rows.Err()
}

func scanV2SearchHits(rows *sql.Rows) ([]V2SearchHit, error) {
	hits := []V2SearchHit{}
	for rows.Next() {
		hit, err := scanV2SearchHit(rows)
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func normalizeV2RecallEvidenceInput(input V2RecallEvidenceInput) V2RecallEvidenceInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ProfileKey = normalizeV2SearchProfileKey(input.ProfileKey)
	input.Query = strings.TrimSpace(input.Query)
	input.KnownEvidenceIDs = normalizeV2RecallUUIDList(input.KnownEvidenceIDs)
	input.KnownRelationshipIDs = normalizeV2RecallUUIDList(input.KnownRelationshipIDs)
	input.ExpandFromEntityIDs = normalizeV2RecallUUIDList(input.ExpandFromEntityIDs)
	if input.Limit <= 0 {
		input.Limit = defaultV2RecallLimit
	}
	if input.Limit > maxV2RecallLimit {
		input.Limit = maxV2RecallLimit
	}
	return input
}

func validateV2RecallEvidenceInput(input V2RecallEvidenceInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.Query == "" && len(input.ExpandFromEntityIDs) == 0 {
		return errors.New("query or expand_from_entity_ids is required")
	}
	for label, values := range map[string][]string{
		"known_evidence_ids":     input.KnownEvidenceIDs,
		"known_relationship_ids": input.KnownRelationshipIDs,
		"expand_from_entity_ids": input.ExpandFromEntityIDs,
	} {
		for _, value := range values {
			if _, err := uuid.Parse(value); err != nil {
				return fmt.Errorf("%s contains invalid UUID %q: %w", label, value, err)
			}
		}
	}
	return nil
}

func normalizeV2RecallUUIDList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func v2RecallOverfetchLimit(limit int) int {
	overfetch := limit * v2RecallOverfetchMultiple
	if overfetch < v2RecallOverfetchFloor {
		overfetch = v2RecallOverfetchFloor
	}
	if overfetch > 250 {
		return 250
	}
	return overfetch
}

func addV2RecallBranch(acc map[string]*v2RecallCandidate, hits []V2SearchHit, knownEvidence map[string]struct{}, weight float64) {
	for i, hit := range hits {
		if hit.SourceKind != "evidence" || hit.SourceID == "" {
			continue
		}
		if _, known := knownEvidence[hit.SourceID]; known {
			continue
		}
		candidate := acc[hit.SourceID]
		if candidate == nil {
			candidate = &v2RecallCandidate{
				EvidenceID:  hit.SourceID,
				SearchState: hit.SearchState,
			}
			acc[hit.SourceID] = candidate
		}
		candidate.Score += weight / (v2RecallRRFConstant + float64(i+1))
		candidate.SearchState = v2RecallCombinedSearchState(candidate.SearchState, hit.SearchState)
	}
}

func sortedV2RecallCandidates(acc map[string]*v2RecallCandidate) []v2RecallCandidate {
	out := make([]v2RecallCandidate, 0, len(acc))
	for _, candidate := range acc {
		out = append(out, *candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].EvidenceID < out[j].EvidenceID
	})
	return out
}

func v2RecallStringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func v2RecallCombinedSearchState(left, right string) string {
	if left == string(domain.V2SearchProjectionPending) || right == string(domain.V2SearchProjectionPending) {
		return string(domain.V2SearchProjectionPending)
	}
	if left == "" {
		return right
	}
	return left
}

func truncateV2RecallContext(value string) string {
	value = strings.TrimSpace(value)
	const max = 2000
	if len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max])
}
