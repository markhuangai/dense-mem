package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// EvidenceDiscoveryInputValidator rechecks the evidence snapshot immediately
// before a provider request. It is a separate capability so scheduler fakes
// and other repository ports do not need to own PostgreSQL eligibility policy;
// the evidence workflow requires this capability before dispatch.
type EvidenceDiscoveryInputValidator interface {
	ValidateEvidenceDiscoveryInputs(context.Context, string, EvidenceTarget, []EvidenceContext) error
}

var _ EvidenceDiscoveryInputValidator = (*SemanticRepositoryImpl)(nil)

// ValidateEvidenceDiscoveryInputs rechecks the target and the evidence
// contexts selected for a provider request under the current team boundary.
// Selection is a snapshot; the admission callback repeats this check after
// provider-gate acquisition so changed content never reaches the provider.
func (r *SemanticRepositoryImpl) ValidateEvidenceDiscoveryInputs(
	ctx context.Context,
	teamID string,
	target EvidenceTarget,
	contexts []EvidenceContext,
) error {
	teamID = strings.TrimSpace(teamID)
	target.EvidenceID = strings.TrimSpace(target.EvidenceID)
	target.FragmentID = strings.TrimSpace(target.FragmentID)
	if _, err := uuid.Parse(teamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if strings.TrimSpace(target.EvidenceID) == "" || strings.TrimSpace(target.EvidenceID) != strings.TrimSpace(target.FragmentID) {
		return ErrDreamSourceStale
	}
	if _, err := uuid.Parse(target.EvidenceID); err != nil {
		return ErrDreamSourceStale
	}
	if len(contexts) > 10 {
		contexts = contexts[:10]
	}
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		if err := validateEvidenceDiscoveryTargetInTx(ctx, tx, teamID, target); err != nil {
			return err
		}
		for index, evidence := range contexts {
			if err := validateEvidenceDiscoveryContextInTx(ctx, tx, teamID, evidence); err != nil {
				return fmt.Errorf("contexts[%d]: %w", index, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("dream: validate evidence discovery inputs: %w", err)
	}
	return nil
}

func validateEvidenceDiscoveryContextInTx(ctx context.Context, tx *gorm.DB, teamID string, evidence EvidenceContext) error {
	evidenceID := strings.TrimSpace(evidence.EvidenceID)
	fragmentID := strings.TrimSpace(evidence.FragmentID)
	if evidenceID == "" || evidenceID != fragmentID {
		return ErrDreamSourceStale
	}
	if _, err := uuid.Parse(fragmentID); err != nil {
		return ErrDreamSourceStale
	}
	var content string
	var sourceID, sourceRevisionID, sourceGroupKey, authority string
	err := tx.WithContext(ctx).Raw(`
		WITH latest_security AS (
			SELECT DISTINCT ON (security.team_id, security.fragment_id)
			       security.team_id, security.fragment_id, security.decision
			FROM evidence_security_events security
			WHERE security.team_id = ?::uuid
			ORDER BY security.team_id, security.fragment_id, security.created_at DESC, security.security_event_id DESC
		)
		SELECT fragment.content,
		       COALESCE(fragment.source_id::text, ''),
		       COALESCE(fragment.source_revision_id::text, ''),
		       CASE
		           WHEN fragment.source_id IS NOT NULL THEN 'source:' || fragment.source_id::text
		           WHEN btrim(fragment.source_ref) <> '' THEN 'owner:' || fragment.owner_profile_id::text || ':' || fragment.source_ref
		           ELSE 'ingest:' || fragment.ingest_id::text
		       END,
		       fragment.authority
		FROM evidence_fragments fragment
		JOIN teams team
		  ON team.id = fragment.team_id
		JOIN knowledge_ingests ingest
		  ON ingest.team_id = fragment.team_id
		 AND ingest.ingest_id = fragment.ingest_id
		LEFT JOIN evidence_sources source
		  ON source.team_id = fragment.team_id
		 AND source.source_id = fragment.source_id
		 AND source.space_id = fragment.space_id
		 AND source.space_generation = fragment.space_generation
		JOIN search_documents document
		  ON document.team_id = fragment.team_id
		 AND document.source_kind = 'evidence'
		 AND document.source_id = fragment.fragment_id
		 AND document.source_version = 1
		 AND document.search_state = 'current'
		 AND document.embedding IS NOT NULL
		 AND document.document_hash = regexp_replace(fragment.content_hash, '^sha256:', '')
		WHERE fragment.team_id = ?::uuid
		  AND team.status = 'active'
		  AND team.deleted_at IS NULL
		  AND fragment.fragment_id = ?::uuid
		  AND fragment.space_id = dense_mem_team_shared_space(fragment.team_id)
		  AND fragment.space_generation = dense_mem_team_shared_generation(fragment.team_id)
		  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  AND COALESCE(ingest.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_exact_aliases alias
		      WHERE alias.team_id = fragment.team_id
		        AND alias.alias_fragment_id = fragment.fragment_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_quarantines quarantine
		      WHERE quarantine.team_id = fragment.team_id
		        AND quarantine.fragment_id = fragment.fragment_id
		        AND quarantine.space_id = fragment.space_id
		        AND quarantine.space_generation = fragment.space_generation
		        AND quarantine.status = 'active'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_lifecycle_events lifecycle
		      WHERE lifecycle.team_id = fragment.team_id
		        AND lifecycle.target_fragment_id = fragment.fragment_id
		        AND lifecycle.space_id = fragment.space_id
		        AND lifecycle.space_generation = fragment.space_generation
		  )
		  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
		  AND EXISTS (
		      SELECT 1
		      FROM latest_security security
		      WHERE security.team_id = fragment.team_id
		        AND security.fragment_id = fragment.fragment_id
		        AND security.decision IN ('pass', 'released')
		  )
	`, teamID, teamID, fragmentID).Row().Scan(&content, &sourceID, &sourceRevisionID, &sourceGroupKey, &authority)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDreamSourceStale
	}
	if err != nil {
		return err
	}
	if content != evidence.Content || sourceID != evidence.SourceID || sourceRevisionID != evidence.SourceRevisionID ||
		sourceGroupKey != evidence.SourceGroupKey || authority != evidence.Authority {
		return ErrDreamSourceStale
	}
	return nil
}
