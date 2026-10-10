package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"gorm.io/gorm"
)

func (r *Store) CommitSession(ctx context.Context, scope session.Scope, id string, prepared *session.Prepared, result session.Result) (*session.Result, error) {
	if result.ProcessingState != "completed" || result.SubmissionID != id {
		return nil, session.ErrInvalidInput
	}
	err := r.withTeamProfileTx(ctx, scope.TeamID, scope.OwnerProfileID, func(tx *gorm.DB) error {
		if err := sessionSpaceFence(ctx, tx, scope); err != nil {
			return err
		}
		var key string
		if err := tx.WithContext(ctx).Raw(`SELECT idempotency_key FROM session_submissions
		 WHERE team_id = ?::uuid AND space_id = ?::uuid AND owner_profile_id = ?::uuid
		 AND submission_id = ?::uuid AND space_generation = ? FOR UPDATE`,
			scope.TeamID, scope.SpaceID, scope.OwnerProfileID, id, scope.SpaceGeneration).Row().Scan(&key); err != nil {
			return err
		}
		submission, err := loadSessionByKey(ctx, tx, scope, key)
		if err != nil {
			return err
		}
		if submission.Result != nil && submission.Result.ProcessingState == "completed" {
			result = *submission.Result
			return nil
		}
		if err := loadSessionCheckpoints(ctx, tx, scope, submission); err != nil {
			return err
		}
		if len(submission.Extractions) != len(submission.Intake.Windows) {
			return errors.New("session: complete extraction checkpoints are required")
		}
		if result.AcceptedEventCount != submission.AcceptedEventCount || result.DuplicateEventCount != submission.DuplicateEventCount {
			return session.ErrInvalidInput
		}
		if prepared != nil {
			input := prepared.Commit
			if input.TeamID != scope.TeamID || input.OwnerProfileID != scope.OwnerProfileID || input.SpaceID != scope.SpaceID || input.SpaceGeneration != scope.SpaceGeneration || input.IngestID != id || input.RequestHash != submission.Intake.RequestHash || input.IdempotencyKey != "session:"+id {
				return session.ErrStale
			}
			if len(submission.Linked) == 0 {
				return errors.New("session: complete linking checkpoint is required")
			}
			if err := validateSessionEvidence(input.Evidence, submission); err != nil {
				return err
			}
			committed, err := r.commitRememberInTx(ctx, tx, input, prepared.Embeddings)
			if err != nil {
				return err
			}
			if err := applySessionSemanticResult(&result, prepared, committed.PublicResult); err != nil {
				return err
			}
		}
		for _, index := range submission.NewEventIndices {
			if index >= len(result.Events) || result.Events[index].EventID != submission.Intake.Request.Events[index].EventID {
				return session.ErrInvalidInput
			}
			body, err := json.Marshal(result.Events[index])
			if err != nil {
				return err
			}
			updated := tx.WithContext(ctx).Exec(`UPDATE session_events SET result = ?::jsonb
			 WHERE team_id = ?::uuid AND space_id = ?::uuid AND owner_profile_id = ?::uuid
			 AND submission_id = ?::uuid AND event_index = ? AND space_generation = ? AND result IS NULL`,
				string(body), scope.TeamID, scope.SpaceID, scope.OwnerProfileID, id, index, scope.SpaceGeneration)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return session.ErrStale
			}
		}
		return insertSessionReceipt(ctx, tx, scope, id, result)
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func sessionInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), v == float64(int(v))
	case json.Number:
		n, err := strconv.Atoi(string(v))
		return n, err == nil
	default:
		return 0, false
	}
}

func validateSessionEvidence(evidence []EvidenceInput, submission *session.Submission) error {
	allowed := map[int]bool{}
	for _, index := range submission.NewEventIndices {
		allowed[index] = true
	}
	for _, item := range evidence {
		provenance, ok := item.Metadata["session"].(map[string]any)
		if !ok {
			return session.ErrInvalidInput
		}
		index, indexOK := sessionInt(provenance["event_index"])
		start, startOK := sessionInt(provenance["span_start"])
		end, endOK := sessionInt(provenance["span_end"])
		if !indexOK || !startOK || !endOK || !allowed[index] {
			return session.ErrInvalidInput
		}
		event := submission.Intake.Request.Events[index]
		runes := []rune(event.Text)
		if start < 0 || end <= start || end > len(runes) || item.Content != string(runes[start:end]) {
			return session.ErrInvalidInput
		}
		if provenance["event_id"] != event.EventID || provenance["session_id"] != submission.Intake.Request.SessionID {
			return session.ErrInvalidInput
		}
	}
	return nil
}

func applySessionSemanticResult(result *session.Result, prepared *session.Prepared, public map[string]any) error {
	body, err := json.Marshal(public)
	if err != nil {
		return err
	}
	var terminal struct {
		SearchState string `json:"search_state"`
		Evidence    []struct {
			EvidenceIndex int    `json:"evidence_index"`
			EvidenceID    string `json:"evidence_id"`
		} `json:"evidence"`
		RelationshipResults []SubmissionRelationshipResult `json:"relationship_results"`
		Warnings            []string                       `json:"warnings"`
	}
	if err := json.Unmarshal(body, &terminal); err != nil {
		return err
	}
	result.SearchState = terminal.SearchState
	result.RelationshipResults = terminal.RelationshipResults
	result.Warnings = append(result.Warnings, terminal.Warnings...)
	for _, evidence := range terminal.Evidence {
		if evidence.EvidenceIndex < 0 || evidence.EvidenceIndex >= len(prepared.Commit.Evidence) {
			return session.ErrInvalidInput
		}
		provenance, ok := prepared.Commit.Evidence[evidence.EvidenceIndex].Metadata["session"].(map[string]any)
		if !ok {
			return session.ErrInvalidInput
		}
		index, ok := sessionInt(provenance["event_index"])
		if !ok || index < 0 || index >= len(result.Events) {
			return session.ErrInvalidInput
		}
		if evidence.EvidenceID != "" {
			result.Events[index].EvidenceIDs = appendUniqueSessionID(result.Events[index].EvidenceIDs, evidence.EvidenceID)
		}
	}
	byID := map[string][]string{}
	for _, event := range result.Events {
		byID[event.EventID] = appendUniqueSessionIDs(byID[event.EventID], event.EvidenceIDs)
	}
	for index := range result.Events {
		result.Events[index].EvidenceIDs = append([]string{}, byID[result.Events[index].EventID]...)
	}
	return nil
}

func appendUniqueSessionID(ids []string, id string) []string {
	for _, previous := range ids {
		if previous == id {
			return ids
		}
	}
	return append(ids, id)
}

func appendUniqueSessionIDs(ids, additional []string) []string {
	for _, id := range additional {
		ids = appendUniqueSessionID(ids, id)
	}
	return ids
}
