package repository

import (
	"context"
	"database/sql"
	"unicode/utf8"

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
