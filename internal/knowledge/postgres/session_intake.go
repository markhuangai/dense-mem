package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"gorm.io/gorm"
)

var _ session.Repository = (*Store)(nil)

func (r *Store) LookupSession(ctx context.Context, scope session.Scope, key string) (*session.Submission, error) {
	var submission *session.Submission
	err := r.withTeamProfileTx(ctx, scope.TeamID, scope.OwnerProfileID, func(tx *gorm.DB) error {
		if err := sessionSpaceFence(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		submission, err = loadSessionByKey(ctx, tx, scope, key)
		if err != nil {
			return err
		}
		return loadSessionCheckpoints(ctx, tx, scope, submission)
	})
	if err != nil {
		return nil, err
	}
	return submission, nil
}

func (r *Store) WithSessionLock(ctx context.Context, scope session.Scope, req session.Request, fn func() error) error {
	key := "ingest_session:" + scope.SpaceID + ":" + session.IdentityHash(req, "")
	return r.withRememberLock(ctx, scope.TeamID, scope.OwnerProfileID, key, func(_ bool) error { return fn() }, false)
}

func sessionSpaceFence(ctx context.Context, tx *gorm.DB, scope session.Scope) error {
	for _, id := range []string{scope.TeamID, scope.OwnerProfileID, scope.SpaceID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil {
			return session.ErrUnauthorized
		}
	}
	if err := lockKnownEvidenceSpace(ctx, tx, scope.TeamID, scope.SpaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) || isPostgresLockNotAvailable(err) {
			return session.ErrStale
		}
		return err
	}
	var spaceID string
	err := tx.WithContext(ctx).Raw(`
		SELECT id::text FROM memory_spaces
		WHERE team_id = ?::uuid AND id = ?::uuid AND generation = ?
		  AND lifecycle_state = 'active' AND kind IN ('profile_private', 'credential_private')
		  AND dense_mem_space_allowed(id)
		  AND (kind <> 'profile_private' OR owner_profile_id = ?::uuid)
	`, scope.TeamID, scope.SpaceID, scope.SpaceGeneration, scope.OwnerProfileID).Row().Scan(&spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return session.ErrStale
	}
	return err
}

func (r *Store) StageSession(ctx context.Context, intake session.Intake) (*session.Submission, error) {
	scope, req := intake.Scope, intake.Request
	var submission *session.Submission
	err := r.withTeamProfileTx(ctx, scope.TeamID, scope.OwnerProfileID, func(tx *gorm.DB) error {
		if err := sessionSpaceFence(ctx, tx, scope); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 319))`, scope.SpaceID+":"+req.IdempotencyKey).Error; err != nil {
			return err
		}
		existing, err := loadSessionByKey(ctx, tx, scope, req.IdempotencyKey)
		if err == nil {
			if existing.Intake.RequestHash != intake.RequestHash || existing.Intake.Scope != scope {
				return session.ErrRequestConflict
			}
			submission = existing
			return loadSessionCheckpoints(ctx, tx, scope, existing)
		}
		if !errors.Is(err, session.ErrNotFound) {
			return err
		}
		intake.DuplicateResults = map[string]session.EventResult{}
		submission = &session.Submission{ID: uuid.NewString(), Intake: intake, NewEventIndices: []int{}, Prior: []session.PriorEvent{}, Extractions: map[int]json.RawMessage{}}
		seen := map[string]session.Event{}
		hasUnfinished := false
		for index, event := range req.Events {
			if previous, exists := seen[event.EventID]; exists {
				if !session.SameEvent(previous, event) {
					return session.ErrEventConflict
				}
				submission.DuplicateEventCount++
				continue
			}
			seen[event.EventID] = event
			var body []byte
			var result sql.NullString
			err := tx.WithContext(ctx).Raw(`
				SELECT body::text, result::text FROM session_events
				WHERE team_id = ?::uuid AND space_id = ?::uuid AND owner_profile_id = ?::uuid
				  AND identity_hash = ? AND space_generation = ?
			`, scope.TeamID, scope.SpaceID, scope.OwnerProfileID, session.IdentityHash(req, event.EventID), scope.SpaceGeneration).Row().Scan(&body, &result)
			if errors.Is(err, sql.ErrNoRows) {
				submission.AcceptedEventCount++
				submission.NewEventIndices = append(submission.NewEventIndices, index)
				continue
			}
			if err != nil {
				return err
			}
			var prior session.Event
			if err := json.Unmarshal(body, &prior); err != nil {
				return err
			}
			if !session.SameEvent(prior, event) {
				return session.ErrEventConflict
			}
			if !result.Valid {
				hasUnfinished = true
				continue
			}
			var duplicate session.EventResult
			if err := json.Unmarshal([]byte(result.String), &duplicate); err != nil {
				return err
			}
			submission.Intake.DuplicateResults[event.EventID] = duplicate
			submission.DuplicateEventCount++
		}
		if hasUnfinished {
			return session.ErrOriginalRequestRequired
		}
		if err := loadPriorSessionEvents(ctx, tx, scope, req, submission); err != nil {
			return err
		}
		filterSessionWindows(submission)
		intakeBody, err := json.Marshal(submission.Intake)
		if err != nil {
			return err
		}
		priorBody, err := json.Marshal(submission.Prior)
		if err != nil {
			return err
		}
		indices, err := json.Marshal(submission.NewEventIndices)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO session_submissions (team_id, owner_profile_id, space_id, space_generation,
			 submission_id, idempotency_key, request_hash, intake, prior_context, new_event_indices,
			 accepted_event_count, duplicate_event_count)
			VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, ?::jsonb, ?::jsonb, ?::jsonb, ?, ?)
		`, scope.TeamID, scope.OwnerProfileID, scope.SpaceID, scope.SpaceGeneration, submission.ID,
			req.IdempotencyKey, intake.RequestHash, string(intakeBody), string(priorBody), string(indices),
			submission.AcceptedEventCount, submission.DuplicateEventCount).Error; err != nil {
			return err
		}
		for _, index := range submission.NewEventIndices {
			event := req.Events[index]
			body, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Exec(`
				INSERT INTO session_events (team_id, owner_profile_id, space_id, space_generation, identity_hash,
				 submission_id, event_index, framework, app_name, user_id, session_id, event_id, body)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, ?::uuid, ?, ?, ?, ?, ?, ?, ?::jsonb)
			`, scope.TeamID, scope.OwnerProfileID, scope.SpaceID, scope.SpaceGeneration, session.IdentityHash(req, event.EventID),
				submission.ID, index, req.Framework, req.AppName, req.UserID, req.SessionID, event.EventID, string(body)).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return submission, nil
}

func filterSessionWindows(submission *session.Submission) {
	newIndices := map[int]bool{}
	for _, index := range submission.NewEventIndices {
		newIndices[index] = true
	}
	windows := []session.Window{}
	for _, window := range submission.Intake.Windows {
		core := []session.Segment{}
		for _, segment := range window.Core {
			if newIndices[segment.EventIndex] {
				core = append(core, segment)
			}
		}
		if len(core) == 0 {
			continue
		}
		window.Core = core
		if window.Before != nil && !newIndices[window.Before.EventIndex] {
			window.Before = nil
		}
		if window.After != nil && !newIndices[window.After.EventIndex] {
			window.After = nil
		}
		windows = append(windows, window)
	}
	submission.Intake.Windows = windows
}

func loadSessionByKey(ctx context.Context, tx *gorm.DB, scope session.Scope, key string) (*session.Submission, error) {
	submission := &session.Submission{}
	var intake, prior, indices []byte
	var result sql.NullString
	err := tx.WithContext(ctx).Raw(`
		SELECT submission_id::text, intake::text, prior_context::text, new_event_indices::text,
		 accepted_event_count, duplicate_event_count, result::text FROM session_submissions
		WHERE team_id = ?::uuid AND space_id = ?::uuid AND owner_profile_id = ?::uuid
		 AND idempotency_key = ? AND space_generation = ? FOR UPDATE
	`, scope.TeamID, scope.SpaceID, scope.OwnerProfileID, key, scope.SpaceGeneration).Row().Scan(
		&submission.ID, &intake, &prior, &indices, &submission.AcceptedEventCount, &submission.DuplicateEventCount, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	for _, pair := range []struct {
		raw    []byte
		target any
	}{{intake, &submission.Intake}, {prior, &submission.Prior}, {indices, &submission.NewEventIndices}} {
		if err := json.Unmarshal(pair.raw, pair.target); err != nil {
			return nil, err
		}
	}
	if result.Valid {
		submission.Result = &session.Result{}
		if err := json.Unmarshal([]byte(result.String), submission.Result); err != nil {
			return nil, err
		}
	}
	return submission, nil
}

func loadPriorSessionEvents(ctx context.Context, tx *gorm.DB, scope session.Scope, req session.Request, submission *session.Submission) error {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT body::text, result::text FROM session_events
		WHERE team_id = ?::uuid AND space_id = ?::uuid AND owner_profile_id = ?::uuid AND space_generation = ?
		 AND framework = ? AND app_name = ? AND user_id = ? AND session_id = ? AND result IS NOT NULL
		ORDER BY created_at DESC, identity_hash DESC LIMIT ?
	`, scope.TeamID, scope.SpaceID, scope.OwnerProfileID, scope.SpaceGeneration,
		req.Framework, req.AppName, req.UserID, req.SessionID, session.ContextEvents).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var body, result []byte
		if err := rows.Scan(&body, &result); err != nil {
			return err
		}
		var event session.PriorEvent
		var outcome session.EventResult
		if err := json.Unmarshal(body, &event.Event); err != nil {
			return err
		}
		if err := json.Unmarshal(result, &outcome); err != nil {
			return err
		}
		event.EvidenceIDs = outcome.EvidenceIDs
		submission.Prior = append(submission.Prior, event)
	}
	return rows.Err()
}

func loadSessionCheckpoints(ctx context.Context, tx *gorm.DB, scope session.Scope, submission *session.Submission) error {
	submission.Extractions = map[int]json.RawMessage{}
	rows, err := tx.WithContext(ctx).Raw(`SELECT window_index, body::text FROM session_extraction_checkpoints
	 WHERE team_id = ?::uuid AND space_id = ?::uuid AND owner_profile_id = ?::uuid
	 AND submission_id = ?::uuid AND space_generation = ? ORDER BY window_index`,
		scope.TeamID, scope.SpaceID, scope.OwnerProfileID, submission.ID, scope.SpaceGeneration).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var body []byte
		if err := rows.Scan(&index, &body); err != nil {
			return err
		}
		if index == -1 {
			submission.Linked = body
		} else {
			submission.Extractions[index] = body
		}
	}
	return rows.Err()
}
