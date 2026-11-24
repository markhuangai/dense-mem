package postgres

import (
	"context"
	"errors"
	"time"

	assignment "github.com/markhuangai/dense-mem/internal/topicassignment/contract"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) CommitMaintenanceAssignments(ctx context.Context, claim ontology.MaintenanceClaim, publications []assignment.Publication) ([]ontology.OrganizationOutcome, error) {
	var outcomes []ontology.OrganizationOutcome
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		fence, err := sharedScope(ctx, tx, claim.TeamID)
		if err != nil {
			return err
		}
		if fence.SpaceID != claim.SpaceID || fence.Generation != claim.Generation {
			return ontology.ErrLeaseLost
		}
		if err := maintenancePublicationFence(ctx, tx, fence); err != nil {
			return err
		}
		input := assignment.Scope{TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation, OperationID: claim.ID}
		changed := make([]assignment.Publication, 0, len(publications))
		byEvidence := make(map[string]assignment.Outcome, len(publications))
		for _, publication := range publications {
			byEvidence[publication.EvidenceID] = publication.Outcome
			if !publication.Outcome.Unchanged {
				changed = append(changed, publication)
			}
		}
		if len(changed) > 0 {
			if err := s.PublishAssignmentsTx(ctx, tx, input, changed); err != nil {
				return err
			}
		}
		for _, source := range claim.Sources {
			var status, inputHash string
			err := tx.Raw(`SELECT status,input_hash FROM topic_assignment_outcomes WHERE team_id=?::uuid AND shared_space_id=?::uuid
				AND space_generation=? AND evidence_id=?::uuid AND (? OR operation_id=?::uuid) ORDER BY created_at DESC,outcome_id LIMIT 1`,
				fence.TeamID, fence.SpaceID, fence.Generation, source.ID, byEvidence[source.ID].Unchanged, claim.ID).Row().Scan(&status, &inputHash)
			if err != nil {
				return err
			}
			outcome := ontology.OrganizationOutcome{Source: source, Status: "organized"}
			if status == "uncategorized" || status == "ambiguous" {
				outcome.Status, outcome.Reason = status, status
			} else if status != "assigned" {
				return errors.New("topic assignment did not reach a valid outcome")
			}
			update := tx.Exec(`UPDATE topic_maintenance_sources SET last_assignment_input_hash=? WHERE team_id=?::uuid AND shared_space_id=?::uuid
				AND space_generation=? AND source_kind=? AND source_id=? AND revision=?`, inputHash, fence.TeamID, fence.SpaceID,
				fence.Generation, source.Kind, source.ID, claim.Revisions[ontology.SourceKey(source)])
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return ontology.ErrSourceStale
			}
			outcomes = append(outcomes, outcome)
		}
		phase := "batch_fence"
		result := ontology.OrganizationResult{Current: true, Outcomes: outcomes}
		return completeMaintenanceBatch(tx, claim, result, "", time.Now(), &phase)
	})
	return outcomes, err
}
