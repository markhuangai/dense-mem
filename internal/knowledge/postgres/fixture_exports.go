package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CreateIngestForTest stages an evidence ingest for PostgreSQL integration
// fixtures. It uses the same owner transaction and source/lifecycle helpers as
// the canonical Knowledge writer without creating a second runtime port.
func (r *Store) CreateIngestForTest(ctx context.Context, input CreateIngestInput) (*EvidenceIngestResult, error) {
	input = normalizeCreateIngestInput(input)
	if input.IngestID == "" {
		input.IngestID = uuid.NewString()
	}
	if err := validateCreateIngestInput(input); err != nil {
		return nil, err
	}
	var result *EvidenceIngestResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		ingestID, created, err := insertKnowledgeIngest(ctx, tx, input)
		if err != nil {
			return err
		}
		if !created {
			rows, err := tx.WithContext(ctx).Raw(`SELECT fragment_id::text, evidence_index, content, content_hash, authority, COALESCE(source_id::text, ''), COALESCE(source_revision_id::text, '') FROM evidence_fragments WHERE team_id = ?::uuid AND ingest_id = ?::uuid ORDER BY evidence_index`, input.TeamID, ingestID).Rows()
			if err != nil {
				return err
			}
			defer rows.Close()
			var evidence []EvidenceFragment
			for rows.Next() {
				var item EvidenceFragment
				if err := rows.Scan(&item.FragmentID, &item.EvidenceIndex, &item.Content, &item.ContentHash, &item.Authority, &item.SourceID, &item.SourceRevisionID); err != nil {
					return err
				}
				evidence = append(evidence, item)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			result = &EvidenceIngestResult{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, Existing: true, Proposal: input.Proposal, Evidence: evidence}
			return nil
		}
		evidence := make([]EvidenceFragment, 0, len(input.Evidence))
		sources := make(map[string]SourceRevisionResult, len(input.Evidence))
		for index, item := range input.Evidence {
			var source *SourceRevisionResult
			if item.SourceKey != "" {
				advanced, err := advanceSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, SourceKey: item.SourceKey, SourceKind: sourceKindForEvidence(item.SourceType), Authority: item.Authority, RevisionToken: item.SourceRevisionToken, ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken, ContentHash: item.SourceRevisionContentHash, Envelope: item.SourceRevisionEnvelope}, sources)
				if err != nil {
					return err
				}
				source = advanced
			}
			fragment, err := insertEvidenceFragment(ctx, tx, input, ingestID, index, item, source)
			if err != nil {
				return err
			}
			evidence = append(evidence, fragment)
			if item.InitialEvent != nil {
				eventID, err := insertSecurityEvent(ctx, tx, SecurityEventInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, FragmentID: fragment.FragmentID, SecurityEventDraft: *item.InitialEvent})
				if err != nil {
					return err
				}
				_ = eventID
				if item.InitialEvent.Decision == "quarantine" {
					if err := insertEvidenceQuarantine(ctx, tx, input, ingestID, fragment.FragmentID, item.InitialEvent.Reason); err != nil {
						return err
					}
				}
			}
		}
		if err := validateRememberSubmissionSupersessionTargets(ctx, tx, input, ingestID); err != nil {
			return err
		}
		if err := applyEvidenceSupersessions(ctx, tx, input, ingestID, evidence); err != nil {
			return err
		}
		result = &EvidenceIngestResult{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, Existing: false, Proposal: input.Proposal, Evidence: evidence}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("test evidence ingest: %w", err)
	}
	return result, nil
}
