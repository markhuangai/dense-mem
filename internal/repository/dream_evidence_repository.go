package repository

import (
	"context"
	"database/sql"
	"unicode/utf8"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

const maxDreamEvidenceExcerptRunes = 4_000

func listDreamInputEvidence(ctx context.Context, tx *gorm.DB, teamID string, input DreamInput) ([]DreamEvidence, error) {
	switch input.Status {
	case "active":
		return listActiveDreamEvidence(ctx, tx, teamID, input.RelationshipID)
	case "pending_evidence":
		return listPendingDreamEvidence(ctx, tx, teamID, input.RelationshipID)
	default:
		return nil, nil
	}
}

func listDreamInputEvidenceBatch(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	inputs []DreamInput,
) (map[string][]DreamEvidence, error) {
	activeIDs := make([]string, 0, len(inputs))
	pendingIDs := make([]string, 0, len(inputs))
	seen := map[string]struct{}{}
	for _, input := range inputs {
		if input.RelationshipID == "" {
			continue
		}
		key := input.Status + "\x00" + input.RelationshipID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		switch input.Status {
		case "active":
			activeIDs = append(activeIDs, input.RelationshipID)
		case "pending_evidence":
			pendingIDs = append(pendingIDs, input.RelationshipID)
		}
	}
	evidenceByRelationship := make(map[string][]DreamEvidence, len(inputs))
	activeEvidence, err := listActiveDreamEvidenceBatch(ctx, tx, teamID, activeIDs)
	if err != nil {
		return nil, err
	}
	for relationshipID, evidence := range activeEvidence {
		evidenceByRelationship[relationshipID] = evidence
	}
	pendingEvidence, err := listPendingDreamEvidenceBatch(ctx, tx, teamID, pendingIDs)
	if err != nil {
		return nil, err
	}
	for relationshipID, evidence := range pendingEvidence {
		evidenceByRelationship[relationshipID] = evidence
	}
	return evidenceByRelationship, nil
}

func listActiveDreamEvidence(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) ([]DreamEvidence, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH latest_support_decision AS (
			SELECT DISTINCT ON (support_id)
			       support_id, decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			ORDER BY support_id, created_at DESC, support_decision_id DESC
		)
		SELECT support.support_id::text,
		       '' AS observation_id,
		       support.fragment_id::text,
		       COALESCE(support.source_id::text, ''),
		       COALESCE(support.source_revision_id::text, ''),
		       support.source_group_key,
		       support.authority,
		       support.span_start,
		       support.span_end,
		       fragment.content
		FROM relationship_evidence_supports support
		JOIN latest_support_decision decision
		  ON decision.support_id = support.support_id
		JOIN evidence_fragments fragment
		  ON fragment.team_id = support.team_id
		 AND fragment.fragment_id = support.fragment_id
		LEFT JOIN evidence_sources source
		  ON source.team_id = support.team_id
		 AND source.source_id = support.source_id
		LEFT JOIN evidence_quarantines quarantine
		  ON quarantine.team_id = support.team_id
		 AND quarantine.fragment_id = support.fragment_id
		 AND quarantine.status = 'active'
		LEFT JOIN evidence_lifecycle_events lifecycle
		  ON lifecycle.team_id = support.team_id
		 AND lifecycle.target_fragment_id = support.fragment_id
		WHERE support.team_id = ?::uuid
		  AND support.relationship_id = ?::uuid
		  AND decision.decision IN ('grant', 'reinstate')
		  AND btrim(support.source_group_key) <> ''
		  AND quarantine.quarantine_id IS NULL
		  AND lifecycle.lifecycle_event_id IS NULL
		  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
		ORDER BY CASE support.authority
		           WHEN 'authoritative' THEN 0
		           WHEN 'primary' THEN 1
		           WHEN 'secondary' THEN 2
		           ELSE 3
		         END,
		         support.created_at DESC,
		         support.support_id
		LIMIT 2
	`, teamID, relationshipID, teamID, relationshipID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDreamEvidence(rows)
}

func listActiveDreamEvidenceBatch(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipIDs []string,
) (map[string][]DreamEvidence, error) {
	if len(relationshipIDs) == 0 {
		return map[string][]DreamEvidence{}, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH latest_support_decision AS (
			SELECT DISTINCT ON (support_id)
			       support_id, decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			  AND relationship_id = ANY(?::uuid[])
			ORDER BY support_id, created_at DESC, support_decision_id DESC
		), ranked AS (
			SELECT support.relationship_id::text AS relationship_id,
			       support.support_id::text,
			       '' AS observation_id,
			       support.fragment_id::text,
			       COALESCE(support.source_id::text, '') AS source_id,
			       COALESCE(support.source_revision_id::text, '') AS source_revision_id,
			       support.source_group_key,
			       support.authority,
			       support.span_start,
			       support.span_end,
			       fragment.content,
			       row_number() OVER (
			           PARTITION BY support.relationship_id
			           ORDER BY CASE support.authority
			                    WHEN 'authoritative' THEN 0
			                    WHEN 'primary' THEN 1
			                    WHEN 'secondary' THEN 2
			                    ELSE 3
			                  END,
			                  support.created_at DESC,
			                  support.support_id
			       ) AS evidence_rank
			FROM relationship_evidence_supports support
			JOIN latest_support_decision decision
			  ON decision.support_id = support.support_id
			JOIN evidence_fragments fragment
			  ON fragment.team_id = support.team_id
			 AND fragment.fragment_id = support.fragment_id
			LEFT JOIN evidence_sources source
			  ON source.team_id = support.team_id
			 AND source.source_id = support.source_id
			LEFT JOIN evidence_quarantines quarantine
			  ON quarantine.team_id = support.team_id
			 AND quarantine.fragment_id = support.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_lifecycle_events lifecycle
			  ON lifecycle.team_id = support.team_id
			 AND lifecycle.target_fragment_id = support.fragment_id
			WHERE support.team_id = ?::uuid
			  AND support.relationship_id = ANY(?::uuid[])
			  AND decision.decision IN ('grant', 'reinstate')
			  AND btrim(support.source_group_key) <> ''
			  AND quarantine.quarantine_id IS NULL
			  AND lifecycle.lifecycle_event_id IS NULL
			  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
		)
		SELECT relationship_id, support_id, observation_id, fragment_id,
		       source_id, source_revision_id, source_group_key, authority,
		       span_start, span_end, content
		FROM ranked
		WHERE evidence_rank <= 2
		ORDER BY relationship_id, evidence_rank
	`, teamID, pq.Array(relationshipIDs), teamID, pq.Array(relationshipIDs)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDreamEvidenceByRelationship(rows)
}

func listPendingDreamEvidence(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) ([]DreamEvidence, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH selected AS (
			SELECT observation.observation_id,
			       observation.evidence
			FROM relationship_observations observation
			JOIN verification_events verification
			  ON verification.team_id = observation.team_id
			 AND verification.observation_id = observation.observation_id
			JOIN placement_assessments assessment
			  ON assessment.team_id = verification.team_id
			 AND assessment.assessment_id = verification.assessment_id
			JOIN review_tasks review
			  ON review.team_id = verification.team_id
			 AND review.assessment_id = verification.assessment_id
			WHERE observation.team_id = ?::uuid
			  AND observation.relationship_id = ?::uuid
			  AND verification.evidence_verdict IN ('insufficient', 'entailed')
			  AND verification.gate_result = 'below_write_threshold'
			  AND review.status = 'expired'
			  AND NOT EXISTS (
			      SELECT 1
			      FROM review_tasks open_review
			      WHERE open_review.team_id = observation.team_id
			        AND open_review.relationship_id = observation.relationship_id
			        AND open_review.status IN ('open', 'acknowledged')
			  )
			ORDER BY verification.created_at DESC, verification.verification_event_id DESC
			LIMIT 1
		), observation_evidence AS (
			SELECT selected.observation_id,
			       item.value AS evidence
			FROM selected
			CROSS JOIN LATERAL jsonb_array_elements(selected.evidence) AS item(value)
		)
		SELECT '' AS support_id,
		       observation_evidence.observation_id::text,
		       fragment.fragment_id::text,
		       COALESCE(fragment.source_id::text, ''),
		       COALESCE(fragment.source_revision_id::text, ''),
		       COALESCE(NULLIF(fragment.source_ref, ''), 'pending_observation'),
		       fragment.authority,
		       CASE WHEN observation_evidence.evidence->>'start' ~ '^[0-9]+$'
		            THEN (observation_evidence.evidence->>'start')::integer ELSE -1 END,
		       CASE WHEN observation_evidence.evidence->>'end' ~ '^[0-9]+$'
		            THEN (observation_evidence.evidence->>'end')::integer ELSE -1 END,
		       fragment.content
		FROM observation_evidence
		JOIN evidence_fragments fragment
		  ON fragment.team_id = ?::uuid
		 AND fragment.fragment_id = CASE
		       WHEN observation_evidence.evidence->>'fragment_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		       THEN (observation_evidence.evidence->>'fragment_id')::uuid
		       ELSE NULL
		     END
		LEFT JOIN evidence_sources source
		  ON source.team_id = fragment.team_id
		 AND source.source_id = fragment.source_id
		LEFT JOIN evidence_quarantines quarantine
		  ON quarantine.team_id = fragment.team_id
		 AND quarantine.fragment_id = fragment.fragment_id
		 AND quarantine.status = 'active'
		LEFT JOIN evidence_lifecycle_events lifecycle
		  ON lifecycle.team_id = fragment.team_id
		 AND lifecycle.target_fragment_id = fragment.fragment_id
		WHERE quarantine.quarantine_id IS NULL
		  AND lifecycle.lifecycle_event_id IS NULL
		  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
		ORDER BY fragment.evidence_index
		LIMIT 2
	`, teamID, relationshipID, teamID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDreamEvidence(rows)
}

func listPendingDreamEvidenceBatch(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipIDs []string,
) (map[string][]DreamEvidence, error) {
	if len(relationshipIDs) == 0 {
		return map[string][]DreamEvidence{}, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH selected AS (
			SELECT DISTINCT ON (observation.relationship_id)
			       observation.relationship_id,
			       observation.observation_id,
			       observation.evidence
			FROM relationship_observations observation
			JOIN verification_events verification
			  ON verification.team_id = observation.team_id
			 AND verification.observation_id = observation.observation_id
			JOIN placement_assessments assessment
			  ON assessment.team_id = verification.team_id
			 AND assessment.assessment_id = verification.assessment_id
			JOIN review_tasks review
			  ON review.team_id = verification.team_id
			 AND review.assessment_id = verification.assessment_id
			WHERE observation.team_id = ?::uuid
			  AND observation.relationship_id = ANY(?::uuid[])
			  AND verification.evidence_verdict IN ('insufficient', 'entailed')
			  AND verification.gate_result = 'below_write_threshold'
			  AND review.status = 'expired'
			  AND NOT EXISTS (
			      SELECT 1
			      FROM review_tasks open_review
			      WHERE open_review.team_id = observation.team_id
			        AND open_review.relationship_id = observation.relationship_id
			        AND open_review.status IN ('open', 'acknowledged')
			  )
			ORDER BY observation.relationship_id,
			         verification.created_at DESC,
			         verification.verification_event_id DESC
		), observation_evidence AS (
			SELECT selected.relationship_id,
			       selected.observation_id,
			       item.value AS evidence
			FROM selected
			CROSS JOIN LATERAL jsonb_array_elements(selected.evidence) AS item(value)
		), ranked AS (
			SELECT observation_evidence.relationship_id::text AS relationship_id,
			       '' AS support_id,
			       observation_evidence.observation_id::text,
			       fragment.fragment_id::text,
			       COALESCE(fragment.source_id::text, '') AS source_id,
			       COALESCE(fragment.source_revision_id::text, '') AS source_revision_id,
			       COALESCE(NULLIF(fragment.source_ref, ''), 'pending_observation') AS source_group_key,
			       fragment.authority,
			       CASE WHEN observation_evidence.evidence->>'start' ~ '^[0-9]+$'
			            THEN (observation_evidence.evidence->>'start')::integer ELSE -1 END AS span_start,
			       CASE WHEN observation_evidence.evidence->>'end' ~ '^[0-9]+$'
			            THEN (observation_evidence.evidence->>'end')::integer ELSE -1 END AS span_end,
			       fragment.content,
			       row_number() OVER (
			           PARTITION BY observation_evidence.relationship_id
			           ORDER BY fragment.evidence_index
			       ) AS evidence_rank
			FROM observation_evidence
			JOIN evidence_fragments fragment
			  ON fragment.team_id = ?::uuid
			 AND fragment.fragment_id = CASE
			       WHEN observation_evidence.evidence->>'fragment_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
			       THEN (observation_evidence.evidence->>'fragment_id')::uuid
			       ELSE NULL
			     END
			LEFT JOIN evidence_sources source
			  ON source.team_id = fragment.team_id
			 AND source.source_id = fragment.source_id
			LEFT JOIN evidence_quarantines quarantine
			  ON quarantine.team_id = fragment.team_id
			 AND quarantine.fragment_id = fragment.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_lifecycle_events lifecycle
			  ON lifecycle.team_id = fragment.team_id
			 AND lifecycle.target_fragment_id = fragment.fragment_id
			WHERE quarantine.quarantine_id IS NULL
			  AND lifecycle.lifecycle_event_id IS NULL
			  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
		)
		SELECT relationship_id, support_id, observation_id, fragment_id,
		       source_id, source_revision_id, source_group_key, authority,
		       span_start, span_end, content
		FROM ranked
		WHERE evidence_rank <= 2
		ORDER BY relationship_id, evidence_rank
	`, teamID, pq.Array(relationshipIDs), teamID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDreamEvidenceByRelationship(rows)
}

func scanDreamEvidence(rows *sql.Rows) ([]DreamEvidence, error) {
	evidence := []DreamEvidence{}
	for rows.Next() {
		var excerpt DreamEvidence
		var content string
		if err := rows.Scan(
			&excerpt.SupportID,
			&excerpt.ObservationID,
			&excerpt.FragmentID,
			&excerpt.SourceID,
			&excerpt.SourceRevisionID,
			&excerpt.SourceGroupKey,
			&excerpt.Authority,
			&excerpt.SpanStart,
			&excerpt.SpanEnd,
			&content,
		); err != nil {
			return nil, err
		}
		exact, ok := dreamExactEvidenceExcerpt(content, excerpt.SpanStart, excerpt.SpanEnd)
		if !ok {
			continue
		}
		excerpt.Content = exact
		evidence = append(evidence, excerpt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return evidence, nil
}

func scanDreamEvidenceByRelationship(rows *sql.Rows) (map[string][]DreamEvidence, error) {
	evidenceByRelationship := map[string][]DreamEvidence{}
	for rows.Next() {
		var relationshipID string
		var excerpt DreamEvidence
		var content string
		if err := rows.Scan(
			&relationshipID,
			&excerpt.SupportID,
			&excerpt.ObservationID,
			&excerpt.FragmentID,
			&excerpt.SourceID,
			&excerpt.SourceRevisionID,
			&excerpt.SourceGroupKey,
			&excerpt.Authority,
			&excerpt.SpanStart,
			&excerpt.SpanEnd,
			&content,
		); err != nil {
			return nil, err
		}
		exact, ok := dreamExactEvidenceExcerpt(content, excerpt.SpanStart, excerpt.SpanEnd)
		if !ok {
			continue
		}
		excerpt.Content = exact
		evidenceByRelationship[relationshipID] = append(evidenceByRelationship[relationshipID], excerpt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return evidenceByRelationship, nil
}

func dreamExactEvidenceExcerpt(content string, start, end int) (string, bool) {
	runes := []rune(content)
	if start < 0 || end <= start || end > len(runes) {
		return "", false
	}
	excerpt := string(runes[start:end])
	if excerpt == "" || utf8.RuneCountInString(excerpt) > maxDreamEvidenceExcerptRunes {
		return "", false
	}
	return excerpt, true
}
