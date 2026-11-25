package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

type storedManagementDraft struct {
	Draft    topic.ManagementDraft `json:"draft"`
	Prepared topic.Publication     `json:"prepared"`
}

func (s *Store) withManagerScope(ctx context.Context, teamID string, write bool, fn func(*gorm.DB, scope, string) error) error {
	actor, ok := requestctx.ActorFromContext(ctx)
	grant := "read"
	if write {
		grant = "write"
	}
	if !ok || actor.TeamID.String() != teamID || actor.OwnerID == uuid.Nil || actor.Role != "manager" || !slices.Contains(actor.Grants, grant) {
		return topic.ErrUnauthorized
	}
	if s == nil || s.db == nil || s.rls == nil {
		return fmt.Errorf("topics: database and RLS required")
	}
	run := s.rls.WithTeamProfileReadOnlyRepeatableTx
	if write {
		run = s.rls.WithTeamProfileTx
	}
	return run(ctx, s.db, teamID, actor.OwnerID.String(), func(tx *gorm.DB) error {
		fence, err := sharedScope(ctx, tx, teamID)
		if err != nil {
			return err
		}
		return fn(tx, fence, actor.OwnerID.String())
	})
}

func draftScopeArgs(fence scope, owner string) []any {
	return []any{fence.TeamID, fence.SpaceID, fence.Generation, owner}
}

const draftScopeSQL = `team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND owner_profile_id=?::uuid`

func loadManagementDraft(tx *gorm.DB, fence scope, owner, id string, lock bool) (storedManagementDraft, error) {
	var result storedManagementDraft
	if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil || parsed.String() != id {
		return result, topic.ErrInvalid
	}
	if lock {
		var version int64
		err := tx.Raw(`SELECT version FROM topic_draft_heads WHERE `+draftScopeSQL+` AND draft_id=?::uuid FOR UPDATE`, append(draftScopeArgs(fence, owner), id)...).Row().Scan(&version)
		if errors.Is(err, sql.ErrNoRows) {
			return result, topic.ErrNotFound
		}
		if err != nil {
			return result, err
		}
	}
	query := `SELECT body FROM topic_draft_head_records WHERE ` + draftScopeSQL + ` AND draft_id=?::uuid`
	var body []byte
	err := tx.Raw(query, append(draftScopeArgs(fence, owner), id)...).Row().Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return result, topic.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(body, &result)
	if err == nil {
		if diagnostic := topic.PublicationDiagnosticFromContext(tx.Statement.Context); diagnostic != nil {
			diagnostic.DraftID, diagnostic.Draft = id, &result.Draft
		}
	}
	return result, err
}

func replayManagementDraft(tx *gorm.DB, fence scope, owner, key, hash string) (storedManagementDraft, bool, error) {
	var result storedManagementDraft
	var storedHash string
	var body []byte
	err := tx.Raw(`SELECT request_hash,body FROM topic_draft_revision_records WHERE `+draftScopeSQL+` AND operation_key=?`, append(draftScopeArgs(fence, owner), key)...).Row().Scan(&storedHash, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if storedHash != hash {
		return result, true, topic.ErrConflict
	}
	err = json.Unmarshal(body, &result)
	return result, true, err
}

func lockDraftOperation(tx *gorm.DB, fence scope, owner, key string) error {
	return tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, fence.TeamID+":"+fence.SpaceID+":"+fmt.Sprint(fence.Generation)+":"+owner+":"+key).Error
}

func persistManagementDraft(tx *gorm.DB, fence scope, owner, key, hash string, previous int64, stored storedManagementDraft) error {
	body, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	var written bool
	if err := tx.Raw(`SELECT dense_mem_persist_topic_draft(?::uuid,?::uuid,?,?::uuid,?,?,?,?::jsonb)`,
		fence.TeamID, fence.SpaceID, fence.Generation, owner, key, hash, previous, string(body)).Row().Scan(&written); err != nil {
		return err
	}
	if !written {
		return topic.ErrConflict
	}
	return nil
}

func insertManagementAudit(ctx context.Context, tx *gorm.DB, prepare topic.ManagementAudit, event topic.ManagementAuditEvent) error {
	if prepare == nil {
		return topic.ErrAuditUnavailable
	}
	entry, err := prepare(event)
	if err == nil {
		err = storage.InsertAuditEntryTx(ctx, tx, entry)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", topic.ErrAuditUnavailable, err)
	}
	return nil
}

func (s *Store) SaveManagementDraft(ctx context.Context, teamID, id string, input topic.SaveDraftInput, prepare topic.ManagementPrepare, audit topic.ManagementAudit) (topic.ManagementDraft, error) {
	var result storedManagementDraft
	if err := topic.ValidateOperation(input.OperationKey, input.ExpectedRevision, input.Reason); err != nil {
		return result.Draft, err
	}
	if prepare == nil || (id == "" && input.ExpectedVersion != 0) {
		return result.Draft, topic.ErrInvalid
	}
	hash, err := topic.ManagementRequestHash("save", id, input)
	if err != nil {
		return result.Draft, err
	}
	err = s.withManagerScope(ctx, teamID, true, func(tx *gorm.DB, fence scope, owner string) error {
		if err := lockDraftOperation(tx, fence, owner, input.OperationKey); err != nil {
			return err
		}
		var found bool
		var err error
		result, found, err = replayManagementDraft(tx, fence, owner, input.OperationKey, hash)
		if err != nil || found {
			return err
		}
		if err := topic.ValidateSaveDraft(input); err != nil {
			return err
		}
		if id == "" {
			id = uuid.NewString()
		} else {
			result, err = loadManagementDraft(tx, fence, owner, id, true)
			if err != nil {
				return err
			}
			if result.Draft.Version != input.ExpectedVersion {
				return topic.PublicationVersionRejection("draft_version", id, input.ExpectedVersion, result.Draft.Version)
			}
			if result.Draft.State != "open" {
				return &topic.PublicationRejection{Cause: topic.ErrConflict, Reason: "draft_state", RecordID: id, Expected: "open", Observed: result.Draft.State}
			}
		}
		if diagnostic := topic.PublicationDiagnosticFromContext(ctx); diagnostic != nil {
			diagnostic.DraftID = id
		}
		current, err := managementEditContext(tx, fence, id, input)
		if err != nil {
			return err
		}
		publication, err := prepare(current)
		if err != nil {
			return err
		}
		if diagnostic := topic.PublicationDiagnosticFromContext(ctx); diagnostic != nil {
			diagnostic.Publication = &publication
		}
		result = storedManagementDraft{Draft: topic.ManagementDraft{ID: id, Version: input.ExpectedVersion + 1, State: "open", BaseRevision: input.ExpectedRevision, Reason: input.Reason, Edits: input.Edits, UpdatedAt: time.Now().UTC()}, Prepared: publication}
		if err := persistManagementDraft(tx, fence, owner, input.OperationKey, hash, input.ExpectedVersion, result); err != nil {
			return err
		}
		return insertManagementAudit(ctx, tx, audit, topic.ManagementAuditEvent{Operation: "TOPIC_DRAFT_SAVE", EntityID: id, Reason: input.Reason, Version: result.Draft.Version, Revision: input.ExpectedRevision})
	})
	return result.Draft, err
}

func (s *Store) GetManagementDraft(ctx context.Context, teamID, id string) (topic.ManagementDraft, error) {
	var result storedManagementDraft
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, owner string) error {
		var err error
		result, err = loadManagementDraft(tx, fence, owner, id, false)
		return err
	})
	return result.Draft, err
}

func (s *Store) ListManagementDrafts(ctx context.Context, teamID, cursor string, limit int) (topic.DraftPage, error) {
	page := topic.DraftPage{Drafts: []topic.ManagementDraft{}}
	if limit < 1 || limit > topic.MaxPageSize {
		return page, topic.ErrInvalid
	}
	if cursor != "" {
		if id, err := uuid.Parse(cursor); err != nil || id.String() != cursor {
			return page, topic.ErrInvalid
		}
	}
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, owner string) error {
		rows, err := tx.Raw(`SELECT body FROM topic_draft_head_records WHERE `+draftScopeSQL+` AND draft_id>COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY draft_id LIMIT ?`, append(draftScopeArgs(fence, owner), cursor, limit+1)...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body []byte
			if err := rows.Scan(&body); err != nil {
				return err
			}
			var stored storedManagementDraft
			if err := json.Unmarshal(body, &stored); err != nil {
				return err
			}
			page.Drafts = append(page.Drafts, stored.Draft)
		}
		if len(page.Drafts) > limit {
			page.NextCursor = page.Drafts[limit-1].ID
			page.Drafts = page.Drafts[:limit]
		}
		return rows.Err()
	})
	return page, err
}

func (s *Store) CommandManagementDraft(ctx context.Context, teamID, id, action string, input topic.DraftCommand, audit topic.ManagementAudit) (topic.ManagementDraft, error) {
	var result storedManagementDraft
	if err := topic.ValidateOperation(input.OperationKey, input.ExpectedRevision, input.Reason); err != nil {
		return result.Draft, err
	}
	if input.ExpectedVersion < 1 || (action != "publish" && action != "discard") {
		return result.Draft, topic.ErrInvalid
	}
	hash, err := topic.ManagementRequestHash(action, id, input)
	if err != nil {
		return result.Draft, err
	}
	err = s.withManagerScope(ctx, teamID, true, func(tx *gorm.DB, fence scope, owner string) error {
		if err := lockDraftOperation(tx, fence, owner, input.OperationKey); err != nil {
			return err
		}
		var found bool
		var err error
		result, found, err = replayManagementDraft(tx, fence, owner, input.OperationKey, hash)
		if err != nil || found {
			return err
		}
		result, err = loadManagementDraft(tx, fence, owner, id, true)
		if err != nil {
			return err
		}
		if result.Draft.Version != input.ExpectedVersion {
			return topic.PublicationVersionRejection("draft_version", id, input.ExpectedVersion, result.Draft.Version)
		}
		if result.Draft.State != "open" {
			return &topic.PublicationRejection{Cause: topic.ErrConflict, Reason: "draft_state", RecordID: id, Expected: "open", Observed: result.Draft.State}
		}
		event := topic.ManagementAuditEvent{Operation: "TOPIC_DRAFT_DISCARD", EntityID: id, Reason: input.Reason, Version: input.ExpectedVersion + 1, Revision: input.ExpectedRevision}
		result.Draft.State = "discarded"
		if action == "publish" {
			if result.Draft.BaseRevision != input.ExpectedRevision {
				return topic.PublicationVersionRejection("draft_revision", id, input.ExpectedRevision, result.Draft.BaseRevision)
			}
			publication := result.Prepared
			publication.OperationKey, publication.Reason = input.OperationKey, input.Reason
			if _, _, err := validateManagementPublication(tx, fence, publication); err != nil {
				return err
			}
			published, err := s.commitPublication(tx, fence, publication, "manager", owner, hash, "")
			if err != nil {
				return err
			}
			result.Draft.State, result.Draft.Publication = "published", &published
			event.Operation, event.EntityID, event.Revision = "TOPIC_PUBLISH", published.ID, published.Revision
		}
		result.Draft.Version++
		result.Draft.Reason = input.Reason
		result.Draft.UpdatedAt = time.Now().UTC()
		if err := persistManagementDraft(tx, fence, owner, input.OperationKey, hash, input.ExpectedVersion, result); err != nil {
			return err
		}
		return insertManagementAudit(ctx, tx, audit, event)
	})
	return result.Draft, err
}

var _ topic.ManagementRepository = (*Store)(nil)
