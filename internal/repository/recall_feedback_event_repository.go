package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type RecallFeedbackEventRepository interface {
	RecordSnapshot(ctx context.Context, event domain.RecallFeedbackEvent) error
	RecordFeedback(ctx context.Context, event domain.RecallFeedbackEvent) error
	List(ctx context.Context, filter domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error)
	Get(ctx context.Context, recallID string) (*domain.RecallFeedbackEvent, error)
	PruneBefore(ctx context.Context, cutoff time.Time) error
}

var ErrRecallFeedbackEventNotFound = errors.New("recall feedback event not found")

type RecallFeedbackEventRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ RecallFeedbackEventRepository = (*RecallFeedbackEventRepositoryImpl)(nil)

func NewRecallFeedbackEventRepository(db *gorm.DB, rls postgres.RLSHelper) *RecallFeedbackEventRepositoryImpl {
	return &RecallFeedbackEventRepositoryImpl{db: db, rls: rls}
}

func (r *RecallFeedbackEventRepositoryImpl) RecordSnapshot(ctx context.Context, event domain.RecallFeedbackEvent) error {
	event = normalizeRecallFeedbackEvent(event)
	toolArgs, resultRefs, degradation, snapshotMetadata, err := marshalRecallFeedbackPayload(event)
	if err != nil {
		return err
	}
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		result := tx.Exec(`
				INSERT INTO recall_feedback_events (
					recall_id, created_at, updated_at, team_id, profile_id, key_id,
					space_id, space_generation,
					auth_method, tool_name, query, tool_args, result_refs,
				result_count, snapshot_state, contract_version, ranking_profile_version,
				embedding_contract_version, search_index_profile_version, search_state,
				degradation, snapshot_metadata
			) VALUES (
				$1, $2, $3, $4, $5, $6,
					$7, $8,
					$9, $10, $11, $12::jsonb, $13::jsonb,
					$14, $15, $16, $17,
					$18, $19, $20,
					$21::jsonb, $22::jsonb
			)
			ON CONFLICT (recall_id) DO UPDATE SET
				updated_at = EXCLUDED.updated_at,
				team_id = COALESCE(recall_feedback_events.team_id, EXCLUDED.team_id),
					profile_id = COALESCE(recall_feedback_events.profile_id, EXCLUDED.profile_id),
					key_id = COALESCE(recall_feedback_events.key_id, EXCLUDED.key_id),
					space_id = COALESCE(recall_feedback_events.space_id, EXCLUDED.space_id),
					space_generation = COALESCE(recall_feedback_events.space_generation, EXCLUDED.space_generation),
				auth_method = CASE
					WHEN recall_feedback_events.auth_method = '' THEN EXCLUDED.auth_method
					ELSE recall_feedback_events.auth_method
				END,
				tool_name = EXCLUDED.tool_name,
				query = EXCLUDED.query,
				tool_args = EXCLUDED.tool_args,
				result_refs = EXCLUDED.result_refs,
				result_count = EXCLUDED.result_count,
				snapshot_state = 'captured',
				contract_version = EXCLUDED.contract_version,
				ranking_profile_version = EXCLUDED.ranking_profile_version,
				embedding_contract_version = EXCLUDED.embedding_contract_version,
				search_index_profile_version = EXCLUDED.search_index_profile_version,
				search_state = EXCLUDED.search_state,
				degradation = EXCLUDED.degradation,
					snapshot_metadata = EXCLUDED.snapshot_metadata
				WHERE (recall_feedback_events.team_id IS NULL OR recall_feedback_events.team_id = EXCLUDED.team_id)
				  AND (recall_feedback_events.space_id IS NULL OR recall_feedback_events.space_id = EXCLUDED.space_id)
				  AND (recall_feedback_events.space_generation IS NULL OR recall_feedback_events.space_generation = EXCLUDED.space_generation)
			`,
			event.RecallID,
			event.CreatedAt.UTC(),
			event.UpdatedAt.UTC(),
			uuidPtrValue(event.TeamID),
			uuidPtrValue(event.ProfileID),
			uuidPtrValue(event.KeyID),
			uuidPtrValue(event.SpaceID),
			event.SpaceGeneration,
			event.AuthMethod,
			event.ToolName,
			event.Query,
			string(toolArgs),
			string(resultRefs),
			event.ResultCount,
			event.SnapshotState,
			event.ContractVersion,
			event.RankingProfileVersion,
			event.EmbeddingContractVersion,
			event.SearchIndexProfileVersion,
			event.SearchState,
			string(degradation),
			string(snapshotMetadata),
		)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrRecallFeedbackEventNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to record recall feedback snapshot: %w", err)
	}
	return nil
}

func (r *RecallFeedbackEventRepositoryImpl) RecordFeedback(ctx context.Context, event domain.RecallFeedbackEvent) error {
	event = normalizeRecallFeedbackEvent(event)
	_, _, _, _, err := marshalRecallFeedbackPayload(event)
	if err != nil {
		return err
	}
	irrelevantRefs, err := marshalRecallFeedbackJudgedRefs(event.IrrelevantRefs)
	if err != nil {
		return err
	}
	dreamFeedback, err := marshalRecallFeedbackDreamFeedback(event.DreamFeedback)
	if err != nil {
		return err
	}
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE recall_feedback_events SET
				updated_at = $2,
				feedback_at = $3,
				key_id = COALESCE(recall_feedback_events.key_id, $4),
				auth_method = CASE
					WHEN recall_feedback_events.auth_method = '' THEN $5
					ELSE recall_feedback_events.auth_method
				END,
				used = $6,
				answer_supported = $7,
				quality = $8,
				missing_context = $9,
				irrelevant = $10,
				feedback_comment = $11,
					irrelevant_result_refs = $12::jsonb,
					dream_feedback = $13::jsonb
				WHERE recall_id = $1
				  AND team_id = $14
				  AND space_id = $15
				  AND space_generation = $16
		`,
			event.RecallID,
			event.UpdatedAt.UTC(),
			timePtrValue(event.FeedbackAt),
			uuidPtrValue(event.KeyID),
			event.AuthMethod,
			boolPtrValue(event.Used),
			boolPtrValue(event.AnswerSupported),
			event.Quality,
			boolPtrValue(event.MissingContext),
			boolPtrValue(event.Irrelevant),
			event.FeedbackComment,
			string(irrelevantRefs),
			string(dreamFeedback),
			uuidPtrValue(event.TeamID),
			uuidPtrValue(event.SpaceID),
			event.SpaceGeneration,
		)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrRecallFeedbackEventNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to record recall feedback: %w", err)
	}
	return nil
}

func (r *RecallFeedbackEventRepositoryImpl) List(ctx context.Context, filter domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error) {
	normalized := normalizeRecallFeedbackEventFilter(filter)
	where, args := recallFeedbackEventWhere(normalized)
	var page domain.RecallFeedbackEventPage

	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var total int64
		if err := tx.Raw(`SELECT count(*) FROM recall_feedback_events `+where, args...).Scan(&total).Error; err != nil {
			return err
		}
		page.Total = total

		queryArgs := append([]any{}, args...)
		queryArgs = append(queryArgs, normalized.Limit, normalized.Offset)
		rows, err := tx.Raw(`
			SELECT `+recallFeedbackEventColumns()+`
			FROM recall_feedback_events
			`+where+`
			ORDER BY created_at DESC, recall_id DESC
			LIMIT ? OFFSET ?
		`, queryArgs...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()

		items := make([]domain.RecallFeedbackEvent, 0)
		for rows.Next() {
			entry, err := scanRecallFeedbackEvent(rows)
			if err != nil {
				return err
			}
			items = append(items, entry)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		page.Items = items
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list recall feedback events: %w", err)
	}
	return &page, nil
}

func (r *RecallFeedbackEventRepositoryImpl) Get(ctx context.Context, recallID string) (*domain.RecallFeedbackEvent, error) {
	recallID = strings.TrimSpace(recallID)
	if recallID == "" {
		return nil, nil
	}
	var event *domain.RecallFeedbackEvent
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT `+recallFeedbackEventColumns()+`
			FROM recall_feedback_events
			WHERE recall_id = ?
			  AND (space_id IS NULL OR `+activeSemanticSpaceGenerationSQL("recall_feedback_events")+`)
			LIMIT 1
		`, recallID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return nil
		}
		parsed, err := scanRecallFeedbackEvent(rows)
		if err != nil {
			return err
		}
		event = &parsed
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get recall feedback event: %w", err)
	}
	return event, nil
}

func (r *RecallFeedbackEventRepositoryImpl) PruneBefore(ctx context.Context, cutoff time.Time) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.Exec("DELETE FROM recall_feedback_events WHERE created_at < ?", cutoff.UTC()).Error
	})
	if err != nil {
		return fmt.Errorf("failed to prune recall feedback events: %w", err)
	}
	return nil
}

func (r *RecallFeedbackEventRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithSystemTx(ctx, r.db, fn)
	}
	return r.db.WithContext(ctx).Transaction(fn)
}

func normalizeRecallFeedbackEvent(event domain.RecallFeedbackEvent) domain.RecallFeedbackEvent {
	event.RecallID = strings.TrimSpace(event.RecallID)
	event.AuthMethod = strings.TrimSpace(event.AuthMethod)
	event.ToolName = strings.TrimSpace(event.ToolName)
	if event.ToolName == "" {
		event.ToolName = "recall_memory"
	}
	event.Query = strings.TrimSpace(event.Query)
	event.Quality = strings.ToLower(strings.TrimSpace(event.Quality))
	event.FeedbackComment = strings.TrimSpace(event.FeedbackComment)
	event.SnapshotState = strings.TrimSpace(event.SnapshotState)
	if event.SnapshotState == "" {
		event.SnapshotState = domain.RecallFeedbackSnapshotCaptured
	}
	if event.ToolArgs == nil {
		event.ToolArgs = map[string]any{}
	}
	if event.ResultRefs == nil {
		event.ResultRefs = []domain.RecallFeedbackResultRef{}
	}
	if event.IrrelevantRefs == nil {
		event.IrrelevantRefs = []domain.RecallFeedbackJudgedResultRef{}
	}
	if event.DreamFeedback == nil {
		event.DreamFeedback = []domain.RecallFeedbackDreamFeedback{}
	}
	if event.Degradation == nil {
		event.Degradation = map[string]any{}
	}
	if event.SnapshotMetadata == nil {
		event.SnapshotMetadata = map[string]any{}
	}
	event.ResultCount = len(event.ResultRefs)
	now := time.Now().UTC()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if event.UpdatedAt.IsZero() {
		event.UpdatedAt = now
	}
	return event
}

func normalizeRecallFeedbackEventFilter(filter domain.RecallFeedbackEventFilter) domain.RecallFeedbackEventFilter {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	filter.Quality = strings.ToLower(strings.TrimSpace(filter.Quality))
	return filter
}

func recallFeedbackEventWhere(filter domain.RecallFeedbackEventFilter) (string, []any) {
	clauses := []string{"WHERE (space_id IS NULL OR " + activeSemanticSpaceGenerationSQL("recall_feedback_events") + ")"}
	args := []any{}
	if filter.TeamID != nil {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID.String())
	}
	if filter.ProfileID != nil {
		clauses = append(clauses, "profile_id = ?")
		args = append(args, filter.ProfileID.String())
	}
	if filter.Quality != "" {
		clauses = append(clauses, "quality = ?")
		args = append(args, filter.Quality)
	} else if !filter.IncludePending {
		clauses = append(clauses, "quality <> ''")
	}
	if filter.MissingContext != nil {
		clauses = append(clauses, "missing_context IS "+boolSQL(*filter.MissingContext))
	}
	if filter.Irrelevant != nil {
		clauses = append(clauses, "irrelevant IS "+boolSQL(*filter.Irrelevant))
	}
	if filter.From != nil {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, filter.From.UTC())
	}
	if filter.To != nil {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, filter.To.UTC())
	}
	return strings.Join(clauses, " AND "), args
}

func recallFeedbackEventColumns() string {
	return `
			recall_id, created_at, updated_at, feedback_at,
			team_id::text, profile_id::text, key_id::text, space_id::text, space_generation,
		auth_method, tool_name, query, tool_args, result_refs,
		result_count, snapshot_state, contract_version, ranking_profile_version,
		embedding_contract_version, search_index_profile_version, search_state,
		degradation, snapshot_metadata, used, answer_supported,
		quality, missing_context, irrelevant, feedback_comment,
		irrelevant_result_refs, dream_feedback
	`
}

func scanRecallFeedbackEvent(rows *sql.Rows) (domain.RecallFeedbackEvent, error) {
	var (
		event               domain.RecallFeedbackEvent
		feedbackAtRaw       sql.NullTime
		teamIDRaw           sql.NullString
		profileIDRaw        sql.NullString
		keyIDRaw            sql.NullString
		spaceIDRaw          sql.NullString
		spaceGenerationRaw  sql.NullInt64
		toolArgsRaw         []byte
		resultRefsRaw       []byte
		degradationRaw      []byte
		snapshotMetadataRaw []byte
		usedRaw             sql.NullBool
		answerSupportedRaw  sql.NullBool
		missingContextRaw   sql.NullBool
		irrelevantRaw       sql.NullBool
		irrelevantRefsRaw   []byte
		dreamFeedbackRaw    []byte
	)
	if err := rows.Scan(
		&event.RecallID,
		&event.CreatedAt,
		&event.UpdatedAt,
		&feedbackAtRaw,
		&teamIDRaw,
		&profileIDRaw,
		&keyIDRaw,
		&spaceIDRaw,
		&spaceGenerationRaw,
		&event.AuthMethod,
		&event.ToolName,
		&event.Query,
		&toolArgsRaw,
		&resultRefsRaw,
		&event.ResultCount,
		&event.SnapshotState,
		&event.ContractVersion,
		&event.RankingProfileVersion,
		&event.EmbeddingContractVersion,
		&event.SearchIndexProfileVersion,
		&event.SearchState,
		&degradationRaw,
		&snapshotMetadataRaw,
		&usedRaw,
		&answerSupportedRaw,
		&event.Quality,
		&missingContextRaw,
		&irrelevantRaw,
		&event.FeedbackComment,
		&irrelevantRefsRaw,
		&dreamFeedbackRaw,
	); err != nil {
		return domain.RecallFeedbackEvent{}, err
	}
	if feedbackAtRaw.Valid {
		event.FeedbackAt = &feedbackAtRaw.Time
	}
	event.TeamID = parseNullableUUID(teamIDRaw)
	event.ProfileID = parseNullableUUID(profileIDRaw)
	event.KeyID = parseNullableUUID(keyIDRaw)
	event.SpaceID = parseNullableUUID(spaceIDRaw)
	if spaceGenerationRaw.Valid {
		event.SpaceGeneration = spaceGenerationRaw.Int64
	}
	event.Used = nullableBoolPtr(usedRaw)
	event.AnswerSupported = nullableBoolPtr(answerSupportedRaw)
	event.MissingContext = nullableBoolPtr(missingContextRaw)
	event.Irrelevant = nullableBoolPtr(irrelevantRaw)
	event.ToolArgs = map[string]any{}
	if len(toolArgsRaw) > 0 {
		if err := json.Unmarshal(toolArgsRaw, &event.ToolArgs); err != nil {
			return domain.RecallFeedbackEvent{}, fmt.Errorf("invalid recall_feedback_events.tool_args JSON: %w", err)
		}
	}
	event.ResultRefs = []domain.RecallFeedbackResultRef{}
	if len(resultRefsRaw) > 0 {
		if err := json.Unmarshal(resultRefsRaw, &event.ResultRefs); err != nil {
			return domain.RecallFeedbackEvent{}, fmt.Errorf("invalid recall_feedback_events.result_refs JSON: %w", err)
		}
	}
	event.Degradation = map[string]any{}
	if len(degradationRaw) > 0 {
		if err := json.Unmarshal(degradationRaw, &event.Degradation); err != nil {
			return domain.RecallFeedbackEvent{}, fmt.Errorf("invalid recall_feedback_events.degradation JSON: %w", err)
		}
	}
	event.SnapshotMetadata = map[string]any{}
	if len(snapshotMetadataRaw) > 0 {
		if err := json.Unmarshal(snapshotMetadataRaw, &event.SnapshotMetadata); err != nil {
			return domain.RecallFeedbackEvent{}, fmt.Errorf("invalid recall_feedback_events.snapshot_metadata JSON: %w", err)
		}
	}
	event.IrrelevantRefs = []domain.RecallFeedbackJudgedResultRef{}
	if len(irrelevantRefsRaw) > 0 {
		if err := json.Unmarshal(irrelevantRefsRaw, &event.IrrelevantRefs); err != nil {
			return domain.RecallFeedbackEvent{}, fmt.Errorf("invalid recall_feedback_events.irrelevant_result_refs JSON: %w", err)
		}
	}
	event.DreamFeedback = []domain.RecallFeedbackDreamFeedback{}
	if len(dreamFeedbackRaw) > 0 {
		if err := json.Unmarshal(dreamFeedbackRaw, &event.DreamFeedback); err != nil {
			return domain.RecallFeedbackEvent{}, fmt.Errorf("invalid recall_feedback_events.dream_feedback JSON: %w", err)
		}
	}
	return event, nil
}

func marshalRecallFeedbackPayload(event domain.RecallFeedbackEvent) ([]byte, []byte, []byte, []byte, error) {
	toolArgs, err := json.Marshal(nonNilMap(event.ToolArgs))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to encode recall feedback tool args: %w", err)
	}
	refs := event.ResultRefs
	if refs == nil {
		refs = []domain.RecallFeedbackResultRef{}
	}
	resultRefs, err := json.Marshal(refs)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to encode recall feedback result refs: %w", err)
	}
	degradation, err := json.Marshal(nonNilMap(event.Degradation))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to encode recall feedback degradation: %w", err)
	}
	snapshotMetadata, err := json.Marshal(nonNilMap(event.SnapshotMetadata))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to encode recall feedback snapshot metadata: %w", err)
	}
	return toolArgs, resultRefs, degradation, snapshotMetadata, nil
}

func marshalRecallFeedbackJudgedRefs(refs []domain.RecallFeedbackJudgedResultRef) ([]byte, error) {
	if refs == nil {
		refs = []domain.RecallFeedbackJudgedResultRef{}
	}
	encoded, err := json.Marshal(refs)
	if err != nil {
		return nil, fmt.Errorf("failed to encode recall feedback irrelevant result refs: %w", err)
	}
	return encoded, nil
}

func marshalRecallFeedbackDreamFeedback(feedback []domain.RecallFeedbackDreamFeedback) ([]byte, error) {
	if feedback == nil {
		feedback = []domain.RecallFeedbackDreamFeedback{}
	}
	encoded, err := json.Marshal(feedback)
	if err != nil {
		return nil, fmt.Errorf("failed to encode recall feedback dream feedback: %w", err)
	}
	return encoded, nil
}

func nullableBoolPtr(raw sql.NullBool) *bool {
	if !raw.Valid {
		return nil
	}
	value := raw.Bool
	return &value
}

func boolPtrValue(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func timePtrValue(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC()
}

func boolSQL(value bool) string {
	if value {
		return "TRUE"
	}
	return "FALSE"
}
