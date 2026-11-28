package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	assignment "github.com/markhuangai/dense-mem/internal/topics/assignment"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) CommitMaintenanceAssignments(ctx context.Context, claim topic.MaintenanceClaim, publications []assignment.Publication) ([]topic.OrganizationOutcome, error) {
	var outcomes []topic.OrganizationOutcome
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		fence, err := sharedScope(ctx, tx, claim.TeamID)
		if err != nil {
			return err
		}
		if fence.SpaceID != claim.SpaceID || fence.Generation != claim.Generation {
			return topic.ErrLeaseLost
		}
		if err := maintenancePublicationFence(topic.WithMaintenanceClaim(ctx, claim), tx, fence); err != nil {
			return err
		}
		if err := lockMaintenanceRun(tx, claim.RunID); err != nil {
			return err
		}
		if err := lockMaintenanceProgressTeam(tx, fence); err != nil {
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
			outcome := topic.OrganizationOutcome{Source: source, Status: "organized"}
			if status == "uncategorized" || status == "ambiguous" {
				outcome.Status, outcome.Reason = status, status
			} else if status != "assigned" {
				return errors.New("topic assignment did not reach a valid outcome")
			}
			update := tx.Exec(`UPDATE topic_maintenance_sources SET last_assignment_input_hash=? WHERE team_id=?::uuid AND shared_space_id=?::uuid
				AND space_generation=? AND source_kind=? AND source_id=? AND revision=?`, inputHash, fence.TeamID, fence.SpaceID,
				fence.Generation, source.Kind, source.ID, claim.Revisions[topic.SourceKey(source)])
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return topic.ErrSourceStale
			}
			outcomes = append(outcomes, outcome)
		}
		phase := "batch_fence"
		result := topic.OrganizationResult{Current: true, Outcomes: outcomes}
		return completeMaintenanceBatch(tx, claim, result, "", time.Now(), &phase)
	})
	return outcomes, err
}

func (s *Store) CommitMaintenanceItemAssignment(ctx context.Context, claim topic.MaintenanceClaim, publication assignment.Publication) (topic.OrganizationOutcome, error) {
	source := topic.SourceHandle{Kind: topic.EvidenceSource, ID: publication.EvidenceID, Version: 1}
	outcome := topic.OrganizationOutcome{Source: source}
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		fence, err := sharedScope(ctx, tx, claim.TeamID)
		if err != nil {
			return err
		}
		if fence.SpaceID != claim.SpaceID || fence.Generation != claim.Generation {
			return topic.ErrLeaseLost
		}
		if err := maintenancePublicationFence(topic.WithMaintenanceClaim(ctx, claim), tx, fence); err != nil {
			return err
		}
		if err := lockMaintenanceProgressTeam(tx, fence); err != nil {
			return err
		}
		snapshot, err := readSource(tx, fence, source)
		if err != nil {
			return err
		}
		fingerprint, err := topic.SourceFingerprint(snapshot)
		if err != nil {
			return err
		}
		if fingerprint != publication.ExpectedFingerprint {
			return topic.ErrSourceStale
		}
		if !publication.Outcome.Unchanged {
			input := assignment.Scope{TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation, OperationID: claim.ID}
			if err := s.PublishAssignmentsTx(ctx, tx, input, []assignment.Publication{publication}); err != nil {
				return err
			}
		}
		var status, inputHash string
		if err := tx.Raw(`SELECT status,input_hash FROM topic_assignment_outcomes WHERE team_id=?::uuid AND shared_space_id=?::uuid
			AND space_generation=? AND evidence_id=?::uuid AND (? OR operation_id=?::uuid) ORDER BY created_at DESC,outcome_id LIMIT 1`,
			fence.TeamID, fence.SpaceID, fence.Generation, source.ID, publication.Outcome.Unchanged, claim.ID).Row().Scan(&status, &inputHash); err != nil {
			return err
		}
		outcome.Status = "organized"
		if status == "uncategorized" || status == "ambiguous" {
			outcome.Status, outcome.Reason = status, status
		} else if status != "assigned" {
			return errors.New("topic assignment did not reach a valid outcome")
		}
		updated := tx.Exec(`UPDATE topic_maintenance_sources SET status=?,reason=?,last_assignment_input_hash=?,last_run_id=?::uuid,force_reassessment=false,updated_at=clock_timestamp()
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind=? AND source_id=?
			AND revision=? AND status IN ('pending','failed')`, outcome.Status, outcome.Reason, inputHash, claim.RunID,
			fence.TeamID, fence.SpaceID, fence.Generation, source.Kind, source.ID, claim.Revisions[topic.SourceKey(source)])
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			var current int64
			var status string
			err := tx.Raw(`SELECT revision,status FROM topic_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid
				AND space_generation=? AND source_kind=? AND source_id=?`, fence.TeamID, fence.SpaceID, fence.Generation, source.Kind, source.ID).Row().Scan(&current, &status)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			observed := "missing"
			if err == nil {
				observed = fmt.Sprintf("%d:%s", current, status)
			}
			return &topic.PublicationRejection{Cause: topic.ErrSourceStale, Reason: "maintenance_source_revision_or_state", RecordID: source.ID,
				Expected: fmt.Sprintf("%d:pending_or_failed", claim.Revisions[topic.SourceKey(source)]), Observed: observed}
		}
		return recordTeamMaintenanceProgress(tx, fence.TeamID, fence.SpaceID, fence.Generation)
	})
	return outcome, err
}
