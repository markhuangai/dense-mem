package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const submissionAssessmentMaxKnownEvidenceIDs = 4000

// ListSubmissionAssessmentKnownEvidence loads only explicitly requested
// evidence that is visible to the authenticated team/profile transaction and
// remains eligible for support context. Missing or ineligible IDs are omitted
// deliberately so callers can return one generic not-supported disposition.
func (r *SemanticRepositoryImpl) ListSubmissionAssessmentKnownEvidence(
	ctx context.Context,
	input SubmissionAssessmentKnownEvidenceInput,
) (SubmissionAssessmentKnownEvidenceResult, error) {
	input = normalizeSubmissionAssessmentKnownEvidenceInput(input)
	if err := validateSubmissionAssessmentKnownEvidenceInput(input); err != nil {
		return SubmissionAssessmentKnownEvidenceResult{}, err
	}
	result := SubmissionAssessmentKnownEvidenceResult{Evidence: []SubmissionAssessmentKnownEvidence{}}
	if len(input.EvidenceIDs) == 0 {
		return result, nil
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT fragment.team_id::text,
			       fragment.fragment_id::text,
			       fragment.ingest_id::text,
			       fragment.owner_profile_id::text,
			       fragment.content,
			       fragment.content_hash,
			       fragment.authority,
			       COALESCE(fragment.source_id::text, ''),
			       COALESCE(fragment.source_revision_id::text, ''),
			       COALESCE(source.current_revision_id::text, ''),
			       fragment.space_id::text,
			       fragment.space_generation
			FROM evidence_fragments AS fragment
			JOIN knowledge_ingests AS ingest
			  ON ingest.team_id = fragment.team_id
			 AND ingest.ingest_id = fragment.ingest_id
			 AND ingest.owner_profile_id = fragment.owner_profile_id
			JOIN memory_spaces AS space
			  ON space.team_id = fragment.team_id
			 AND space.id = fragment.space_id
			LEFT JOIN evidence_sources AS source
			  ON source.team_id = fragment.team_id
			 AND source.source_id = fragment.source_id
			 AND source.owner_profile_id = fragment.owner_profile_id
			LEFT JOIN evidence_quarantines AS quarantine
			  ON quarantine.team_id = fragment.team_id
			 AND quarantine.fragment_id = fragment.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_lifecycle_events AS lifecycle
			  ON lifecycle.team_id = fragment.team_id
			 AND lifecycle.target_fragment_id = fragment.fragment_id
			WHERE fragment.team_id = ?::uuid
			  AND fragment.fragment_id = ANY(?::uuid[])
			  AND ingest.status = 'completed'
			  AND space.lifecycle_state = 'active'
			  AND (space.kind = 'team_shared' OR dense_mem_space_allowed(space.id))
			  AND fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
			  AND quarantine.quarantine_id IS NULL
			  AND lifecycle.lifecycle_event_id IS NULL
			  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
			  AND NOT (
			      ingest.source_summary = 'overdue conflict deletion-only derivation'
			      AND ingest.metadata ->> 'conflict_resolution_deletion_only' = 'true'
			  )
			ORDER BY fragment.fragment_id
		`, input.TeamID, pq.Array(input.EvidenceIDs)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item SubmissionAssessmentKnownEvidence
			if err := rows.Scan(
				&item.TeamID,
				&item.EvidenceID,
				&item.IngestID,
				&item.OwnerProfileID,
				&item.Content,
				&item.ContentHash,
				&item.Authority,
				&item.SourceID,
				&item.SourceRevisionID,
				&item.CurrentSourceRevisionID,
				&item.SpaceID,
				&item.SpaceGeneration,
			); err != nil {
				return err
			}
			item.FragmentID = item.EvidenceID
			result.Evidence = append(result.Evidence, item)
		}
		return rows.Err()
	})
	if err != nil {
		return SubmissionAssessmentKnownEvidenceResult{}, fmt.Errorf("semantic: list submission assessment known evidence: %w", err)
	}
	return result, nil
}

func normalizeSubmissionAssessmentKnownEvidenceInput(input SubmissionAssessmentKnownEvidenceInput) SubmissionAssessmentKnownEvidenceInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	seen := make(map[string]struct{}, len(input.EvidenceIDs))
	ids := make([]string, 0, len(input.EvidenceIDs))
	for _, evidenceID := range input.EvidenceIDs {
		evidenceID = strings.TrimSpace(evidenceID)
		if evidenceID == "" {
			continue
		}
		if parsed, err := uuid.Parse(evidenceID); err == nil {
			evidenceID = parsed.String()
		}
		if _, exists := seen[evidenceID]; exists {
			continue
		}
		seen[evidenceID] = struct{}{}
		ids = append(ids, evidenceID)
	}
	input.EvidenceIDs = ids
	return input
}

func validateSubmissionAssessmentKnownEvidenceInput(input SubmissionAssessmentKnownEvidenceInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if len(input.EvidenceIDs) > submissionAssessmentMaxKnownEvidenceIDs {
		return fmt.Errorf("evidence_ids must contain at most %d entries", submissionAssessmentMaxKnownEvidenceIDs)
	}
	for _, evidenceID := range input.EvidenceIDs {
		if _, err := uuid.Parse(evidenceID); err != nil {
			return fmt.Errorf("evidence_id is invalid: %w", err)
		}
	}
	return nil
}
