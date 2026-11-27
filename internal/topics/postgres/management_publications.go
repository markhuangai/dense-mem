package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

const managementPublicationColumns = `result,origin,COALESCE(actor_id::text,''),reason,created_at,COALESCE(rollback_of::text,'')`

func scanManagementPublication(row maintenanceScanner) (topic.ManagementPublication, error) {
	var item topic.ManagementPublication
	var body []byte
	err := row.Scan(&body, &item.Origin, &item.ActorID, &item.Reason, &item.CreatedAt, &item.RollbackOf)
	if err == nil {
		err = json.Unmarshal(body, &item.PublicationResult)
	}
	return item, err
}

func (s *Store) ListManagementPublications(ctx context.Context, teamID string, after int64, limit int) (topic.ManagementPublicationPage, error) {
	page := topic.ManagementPublicationPage{Publications: []topic.ManagementPublication{}}
	if after < 0 || limit < 1 || limit > topic.MaxPageSize {
		return page, topic.ErrInvalid
	}
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, _ string) error {
		rows, err := tx.Raw(`SELECT `+managementPublicationColumns+` FROM topic_publications WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND revision>? ORDER BY revision LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, after, limit+1).Rows()
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

func (s *Store) GetManagementPublication(ctx context.Context, teamID, id string) (topic.ManagementPublication, error) {
	var result topic.ManagementPublication
	if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil || parsed.String() != id {
		return result, topic.ErrInvalid
	}
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, _ string) error {
		var err error
		result, err = scanManagementPublication(tx.Raw(`SELECT `+managementPublicationColumns+` FROM topic_publications WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND publication_id=?::uuid`, fence.TeamID, fence.SpaceID, fence.Generation, id).Row())
		if errors.Is(err, sql.ErrNoRows) {
			return topic.ErrNotFound
		}
		if err != nil {
			return err
		}
		var records []topic.Record
		rows, err := tx.Raw(`SELECT body FROM topic_revision_records WHERE
            team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND publication_id=?::uuid ORDER BY record_id`,
			fence.TeamID, fence.SpaceID, fence.Generation, id).Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
			var body []byte
			if err := rows.Scan(&body); err != nil {
				rows.Close()
				return err
			}
			record, err := decodeRecord(body)
			if err != nil {
				rows.Close()
				return err
			}
			records = append(records, record)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		result.RecordsView, err = s.organizationViews(tx, fence, records)
		return err
	})
	return result, err
}

func (s *Store) RollbackManagementPublication(ctx context.Context, teamID, id string, input topic.DraftCommand, audit topic.ManagementAudit) (topic.PublicationResult, error) {
	var result topic.PublicationResult
	if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil || parsed.String() != id {
		return result, topic.ErrInvalid
	}
	if err := topic.ValidateOperation(input.OperationKey, input.ExpectedRevision, input.Reason); err != nil {
		return result, err
	}
	err := s.withManagerScope(ctx, teamID, true, func(tx *gorm.DB, fence scope, owner string) error {
		publication := topic.Publication{OperationKey: input.OperationKey, ExpectedRevision: input.ExpectedRevision, Reason: input.Reason, RollbackOf: id}
		hash, err := topic.RequestHash(publication, "rollback", owner)
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
		err = tx.Raw(`SELECT origin FROM topic_publications WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND publication_id=?::uuid`, fence.TeamID, fence.SpaceID, fence.Generation, id).Row().Scan(&origin)
		if errors.Is(err, sql.ErrNoRows) {
			return topic.ErrNotFound
		}
		if err != nil {
			return err
		}
		if diagnostic := topic.PublicationDiagnosticFromContext(ctx); diagnostic != nil {
			diagnostic.RollbackOf = id
		}
		if origin != "manager" && origin != "rollback" {
			return topic.ErrInvalid
		}
		publication, err = prepareTopicRollback(tx, fence, publication)
		if err != nil {
			return err
		}
		if _, _, err = validateManagementPublication(tx, fence, publication); err != nil {
			return err
		}
		result, err = s.commitPublication(tx, fence, publication, "rollback", owner, hash, "")
		if err != nil {
			return err
		}
		return insertManagementAudit(ctx, tx, audit, topic.ManagementAuditEvent{Operation: "TOPIC_ROLLBACK", EntityID: result.ID, Reason: input.Reason, Revision: result.Revision})
	})
	return result, err
}

func (s *Store) SaveManagementEdits(ctx context.Context, teamID string, input topic.SaveEditsInput, audit topic.ManagementAudit) (topic.PublicationResult, error) {
	var result topic.PublicationResult
	draft := topic.SaveDraftInput{OperationKey: input.OperationKey, Reason: input.Reason, Edits: input.Edits}
	err := s.withManagerScope(ctx, teamID, true, func(tx *gorm.DB, fence scope, owner string) error {
		hash, err := topic.ManagementRequestHash("edits", owner, input)
		if err != nil {
			return err
		}
		if err := lockDraftOperation(tx, fence, owner, input.OperationKey); err != nil {
			return err
		}
		if err := lockOrganizationCatalog(tx, fence); err != nil {
			return err
		}
		var found bool
		result, found, err = replayPublication(tx, fence, input.OperationKey, hash)
		if err != nil || found {
			return err
		}
		if err := topic.ValidateSaveDraft(draft); err != nil {
			return err
		}
		draft.Edits = append([]topic.ManagementEdit(nil), input.Edits...)
		for i, edit := range draft.Edits {
			if edit.RecordID == "" {
				continue
			}
			var version, definitionVersion int64
			err := tx.Raw(`SELECT version,definition_version FROM topics
                WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND topic_id=?::uuid AND NOT retired`,
				fence.TeamID, fence.SpaceID, fence.Generation, edit.RecordID).Row().Scan(&version, &definitionVersion)
			if errors.Is(err, sql.ErrNoRows) {
				return topic.ErrNotFound
			}
			if err != nil {
				return err
			}
			if version != edit.ExpectedVersion {
				return topic.PublicationVersionRejection("record_version", edit.RecordID, edit.ExpectedVersion, version)
			}
			draft.Edits[i].ExpectedVersion = definitionVersion
		}
		current, err := managementEditContext(tx, fence, uuid.NewString(), draft, false)
		if err != nil {
			return err
		}
		draft.ExpectedRevision = current.Revision
		publication, err := topic.CompileManagementEdits(draft, current)
		if err != nil {
			return err
		}
		result, err = s.writePublication(tx, fence, publication, "manager", owner, hash, "", false, nil)
		if err != nil {
			return err
		}
		return insertManagementAudit(ctx, tx, audit, topic.ManagementAuditEvent{Operation: "TOPIC_PUBLISH", EntityID: result.ID, Reason: input.Reason, Revision: result.Revision})
	})
	return result, err
}

func (s *Store) ReplayManagementRollback(ctx context.Context, teamID, id string, input topic.DraftCommand) (topic.PublicationResult, error) {
	var result topic.PublicationResult
	err := s.withManagerScope(ctx, teamID, true, func(tx *gorm.DB, fence scope, owner string) error {
		command := topic.Publication{OperationKey: input.OperationKey, ExpectedRevision: input.ExpectedRevision, Reason: input.Reason, RollbackOf: id}
		hash, err := topic.RequestHash(command, "rollback", owner)
		if err != nil {
			return err
		}
		var found bool
		var actorID sql.NullString
		err = tx.Raw(`SELECT actor_id::text FROM topic_publications
            WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND operation_key=?`,
			fence.TeamID, fence.SpaceID, fence.Generation, input.OperationKey).Row().Scan(&actorID)
		if errors.Is(err, sql.ErrNoRows) {
			return topic.ErrLegacyMutation
		}
		if err != nil {
			return err
		}
		if actorID.String != owner {
			return topic.ErrConflict
		}
		result, found, err = replayPublication(tx, fence, input.OperationKey, hash)
		if err != nil || found {
			return err
		}
		return topic.ErrLegacyMutation
	})
	return result, err
}
