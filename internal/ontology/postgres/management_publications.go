package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

const managementPublicationColumns = `result,origin,COALESCE(actor_id::text,''),reason,created_at,COALESCE(rollback_of::text,'')`

func scanManagementPublication(row maintenanceScanner) (ontology.ManagementPublication, error) {
	var item ontology.ManagementPublication
	var body []byte
	err := row.Scan(&body, &item.Origin, &item.ActorID, &item.Reason, &item.CreatedAt, &item.RollbackOf)
	if err == nil {
		err = json.Unmarshal(body, &item.PublicationResult)
	}
	return item, err
}

func (s *Store) ListManagementPublications(ctx context.Context, teamID string, after int64, limit int) (ontology.ManagementPublicationPage, error) {
	page := ontology.ManagementPublicationPage{Publications: []ontology.ManagementPublication{}}
	if after < 0 || limit < 1 || limit > ontology.MaxPageSize {
		return page, ontology.ErrInvalid
	}
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, _ string) error {
		rows, err := tx.Raw(`SELECT `+managementPublicationColumns+` FROM ontology_publications WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND revision>? ORDER BY revision LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, after, limit+1).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanManagementPublication(rows)
			if err != nil {
				return err
			}
			page.Publications = append(page.Publications, item)
		}
		if len(page.Publications) > limit {
			page.NextRevision = page.Publications[limit-1].Revision
			page.Publications = page.Publications[:limit]
		}
		return rows.Err()
	})
	return page, err
}

func (s *Store) GetManagementPublication(ctx context.Context, teamID, id string) (ontology.ManagementPublication, error) {
	var result ontology.ManagementPublication
	if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil || parsed.String() != id {
		return result, ontology.ErrInvalid
	}
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, _ string) error {
		var err error
		result, err = scanManagementPublication(tx.Raw(`SELECT `+managementPublicationColumns+` FROM ontology_publications WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND publication_id=?::uuid`, fence.TeamID, fence.SpaceID, fence.Generation, id).Row())
		if errors.Is(err, sql.ErrNoRows) {
			return ontology.ErrNotFound
		}
		if err != nil {
			return err
		}
		for _, ref := range result.Records {
			var body []byte
			if err := tx.Raw(`SELECT body FROM ontology_record_revisions WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND record_id=?::uuid AND version=?`, fence.TeamID, fence.SpaceID, fence.Generation, ref.ID, ref.Version).Row().Scan(&body); err != nil {
				return err
			}
			record, err := decodeRecord(body)
			if err != nil {
				return err
			}
			view, err := s.currentView(tx, fence, record)
			if err != nil {
				return err
			}
			result.RecordsView = append(result.RecordsView, view)
		}
		return nil
	})
	return result, err
}

func (s *Store) RollbackManagementPublication(ctx context.Context, teamID, id string, input ontology.DraftCommand, audit ontology.ManagementAudit) (ontology.PublicationResult, error) {
	var result ontology.PublicationResult
	if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil || parsed.String() != id {
		return result, ontology.ErrInvalid
	}
	if err := ontology.ValidateOperation(input.OperationKey, input.ExpectedRevision, input.Reason); err != nil {
		return result, err
	}
	err := s.withManagerScope(ctx, teamID, true, func(tx *gorm.DB, fence scope, owner string) error {
		publication := ontology.Publication{OperationKey: input.OperationKey, ExpectedRevision: input.ExpectedRevision, Reason: input.Reason, RollbackOf: id}
		hash, err := ontology.RequestHash(publication, "rollback", owner)
		if err != nil {
			return err
		}
		if err := lockDraftOperation(tx, fence, owner, input.OperationKey); err != nil {
			return err
		}
		var found bool
		result, found, err = replayPublication(tx, fence, input.OperationKey, hash)
		if err != nil || found {
			return err
		}
		var origin string
		err = tx.Raw(`SELECT origin FROM ontology_publications WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND publication_id=?::uuid`, fence.TeamID, fence.SpaceID, fence.Generation, id).Row().Scan(&origin)
		if errors.Is(err, sql.ErrNoRows) {
			return ontology.ErrNotFound
		}
		if err != nil {
			return err
		}
		if origin != "manager" && origin != "rollback" {
			return ontology.ErrInvalid
		}
		publication, err = prepareOntologyRollback(tx, fence, publication)
		if err != nil {
			return err
		}
		if _, _, err = validateManagementPublication(tx, fence, publication); err != nil {
			return err
		}
		result, err = s.commitPublication(tx, fence, publication, "rollback", owner, hash, "", "")
		if err != nil {
			return err
		}
		return insertManagementAudit(ctx, tx, audit, ontology.ManagementAuditEvent{Operation: "ONTOLOGY_ROLLBACK", EntityID: result.ID, Reason: input.Reason, Revision: result.Revision})
	})
	return result, err
}
