package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

const (
	dreamDiagnosticRetention                     = 7 * 24 * time.Hour
	maxDreamDiagnosticBytes                      = 64 << 10
	maxDreamDiagnosticPage                       = 100
	dreamDiagnosticRunSummaryLockNamespace       = "dense-mem:dream-run-diagnostic:v1:"
	dreamDiagnosticRunSummaryLockHashSeed  int64 = 324
)

const dreamDiagnosticTraceStateJoin = `
			LEFT JOIN LATERAL (
				SELECT
					CASE
						WHEN capture.phase = 'run' AND jsonb_typeof(capture.details->'phase_trace_expected') = 'array' THEN
							EXISTS (
								SELECT 1
								FROM jsonb_to_recordset(capture.details->'phase_trace_expected')
									AS expected(phase text, hypothesis_id text, count bigint)
								WHERE (
									SELECT count(*)
									FROM dream_diagnostic_captures AS phase_capture
									WHERE phase_capture.team_id = capture.team_id
									  AND phase_capture.run_id = capture.run_id
									  AND phase_capture.phase = expected.phase
									  AND COALESCE(phase_capture.hypothesis_id::text, '') = expected.hypothesis_id
								) < expected.count
							)
						ELSE false
					END AS missing,
					CASE
						WHEN capture.phase = 'run' AND jsonb_typeof(capture.details->'phase_trace_expected') = 'array' THEN
							COALESCE(
								NULLIF(capture.details->>'phase_trace_deadline_at', '')::timestamptz,
								capture.created_at + INTERVAL '20 seconds'
							) <= CURRENT_TIMESTAMP
						ELSE false
					END AS deadline_elapsed
			) AS phase_trace ON true`

const dreamDiagnosticDetailsProjection = `CASE
	WHEN capture.phase = 'run' AND jsonb_typeof(capture.details->'phase_trace_expected') = 'array' THEN
		(capture.details - 'phase_trace_expected' - 'phase_trace_deadline_at') || jsonb_build_object(
			'phase_trace_pending',
				NOT COALESCE((capture.details->>'phase_trace_truncated')::boolean, false)
				AND phase_trace.missing
				AND NOT phase_trace.deadline_elapsed,
			'phase_trace_truncated',
				COALESCE((capture.details->>'phase_trace_truncated')::boolean, false)
				OR (phase_trace.missing AND phase_trace.deadline_elapsed)
		)
	ELSE capture.details
END`

var _ dreamcontract.DreamDiagnosticRepository = (*Store)(nil)

func (r *Store) ListDreamDiagnostics(ctx context.Context, input dreamcontract.DreamDiagnosticListInput) (dreamcontract.DreamDiagnosticPage, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.HypothesisID = strings.TrimSpace(input.HypothesisID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return dreamcontract.DreamDiagnosticPage{}, fmt.Errorf("dream diagnostic team_id is required: %w", err)
	}
	if input.RunID == "" && input.HypothesisID == "" {
		return dreamcontract.DreamDiagnosticPage{}, fmt.Errorf("run_id or hypothesis_id is required")
	}
	if input.RunID != "" {
		if _, err := uuid.Parse(input.RunID); err != nil {
			return dreamcontract.DreamDiagnosticPage{}, fmt.Errorf("dream diagnostic run_id is required: %w", err)
		}
	}
	if input.HypothesisID != "" {
		if _, err := uuid.Parse(input.HypothesisID); err != nil {
			return dreamcontract.DreamDiagnosticPage{}, fmt.Errorf("dream diagnostic hypothesis_id is required: %w", err)
		}
	}
	if input.RunID == "" {
		input.RunID = uuid.Nil.String()
	}
	if _, err := uuid.Parse(input.RunID); err != nil {
		return dreamcontract.DreamDiagnosticPage{}, fmt.Errorf("dream diagnostic run_id is required: %w", err)
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > maxDreamDiagnosticPage {
		limit = maxDreamDiagnosticPage
	}
	cursor, err := decodeDreamDiagnosticCursor(input.Cursor)
	if err != nil {
		return dreamcontract.DreamDiagnosticPage{}, err
	}
	page := dreamcontract.DreamDiagnosticPage{Items: []dreamcontract.DreamDiagnosticCapture{}}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		query := `
			SELECT capture.capture_id::text, capture.run_id::text, COALESCE(capture.hypothesis_id::text, ''), capture.phase, capture.outcome, capture.cause,
			       CASE WHEN capture.expires_at <= CURRENT_TIMESTAMP THEN '{}'::jsonb ELSE ` + dreamDiagnosticDetailsProjection + ` END,
			       '{}'::jsonb,
			       CASE WHEN capture.expires_at <= CURRENT_TIMESTAMP THEN 'expired' ELSE capture.capture_state END,
			       CASE WHEN capture.expires_at <= CURRENT_TIMESTAMP THEN 'retention_expired' ELSE capture.capture_reason END,
			       capture.captured_at, capture.expires_at, capture.created_at
			FROM dream_diagnostic_captures AS capture
			` + dreamDiagnosticTraceStateJoin + `
			WHERE capture.team_id = ?::uuid`
		args := []any{input.TeamID}
		if input.RunID != uuid.Nil.String() {
			query += " AND capture.run_id = ?::uuid"
			args = append(args, input.RunID)
		}
		if input.HypothesisID != "" {
			query += " AND capture.hypothesis_id = ?::uuid"
			args = append(args, input.HypothesisID)
		}
		if cursor != nil {
			query += " AND (capture.created_at, capture.capture_id) < (?, ?::uuid)"
			args = append(args, cursor.createdAt, cursor.captureID)
		}
		query += " ORDER BY capture.created_at DESC, capture.capture_id DESC LIMIT ?"
		args = append(args, limit+1)
		rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanDreamDiagnosticCapture(rows, input.TeamID)
			if err != nil {
				return err
			}
			item.Payload = nil
			page.Items = append(page.Items, *item)
		}
		return rows.Err()
	})
	if err != nil {
		return dreamcontract.DreamDiagnosticPage{}, fmt.Errorf("dream diagnostic list: %w", err)
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items = page.Items[:limit]
		page.NextCursor = encodeDreamDiagnosticCursor(last.CreatedAt, last.CaptureID)
	}
	return page, nil
}

func (r *Store) GetDreamDiagnostic(ctx context.Context, teamID, runID, captureID string) (*dreamcontract.DreamDiagnosticCapture, error) {
	teamID, runID, captureID = strings.TrimSpace(teamID), strings.TrimSpace(runID), strings.TrimSpace(captureID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("dream diagnostic team_id is required: %w", err)
	}
	if _, err := uuid.Parse(runID); err != nil {
		return nil, fmt.Errorf("dream diagnostic run_id is required: %w", err)
	}
	if _, err := uuid.Parse(captureID); err != nil {
		return nil, fmt.Errorf("dream diagnostic capture_id is required: %w", err)
	}
	var result *dreamcontract.DreamDiagnosticCapture
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		row := tx.WithContext(ctx).Raw(`
			SELECT capture.capture_id::text, capture.run_id::text, COALESCE(capture.hypothesis_id::text, ''), capture.phase, capture.outcome, capture.cause,
			       CASE WHEN capture.expires_at <= CURRENT_TIMESTAMP THEN '{}'::jsonb ELSE `+dreamDiagnosticDetailsProjection+` END,
			       CASE WHEN capture.expires_at <= CURRENT_TIMESTAMP THEN '{}'::jsonb ELSE capture.payload END,
			       CASE WHEN capture.expires_at <= CURRENT_TIMESTAMP THEN 'expired' ELSE capture.capture_state END,
			       CASE WHEN capture.expires_at <= CURRENT_TIMESTAMP THEN 'retention_expired' ELSE capture.capture_reason END,
			       capture.captured_at, capture.expires_at, capture.created_at
			FROM dream_diagnostic_captures AS capture
			`+dreamDiagnosticTraceStateJoin+`
			WHERE capture.team_id = ?::uuid AND capture.run_id = ?::uuid AND capture.capture_id = ?::uuid
		`, teamID, runID, captureID).Row()
		item, err := scanDreamDiagnosticCaptureRow(row, teamID)
		if errors.Is(err, sql.ErrNoRows) {
			return dreamcontract.ErrDreamDiagnosticNotFound
		}
		if err != nil {
			return err
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream diagnostic get: %w", err)
	}
	return result, nil
}

func (r *Store) PurgeExpiredDreamDiagnostics(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 || batchSize > maxDreamDiagnosticPage {
		batchSize = maxDreamDiagnosticPage
	}
	var deleted int64
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			WITH expired_tombstones AS (
				SELECT team_id, capture_id
				FROM dream_diagnostic_captures
				WHERE capture_state = 'expired'
				  AND (tombstone_expires_at IS NULL OR tombstone_expires_at <= clock_timestamp())
				ORDER BY expires_at, capture_id
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			)
			DELETE FROM dream_diagnostic_captures capture
			USING expired_tombstones
			WHERE capture.team_id = expired_tombstones.team_id
			  AND capture.capture_id = expired_tombstones.capture_id
		`, batchSize)
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		remaining := batchSize - int(deleted)
		if remaining <= 0 {
			return nil
		}
		result = tx.WithContext(ctx).Exec(`
			UPDATE dream_diagnostic_captures
			SET payload = '{}'::jsonb,
			    details = '{}'::jsonb,
			    capture_state = 'expired',
			    capture_reason = 'retention_expired',
			    tombstone_expires_at = CURRENT_TIMESTAMP + INTERVAL '24 hours'
			WHERE (team_id, capture_id) IN (
				SELECT team_id, capture_id
				FROM dream_diagnostic_captures
				WHERE expires_at <= clock_timestamp()
				  AND capture_state <> 'expired'
				ORDER BY expires_at, capture_id
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			)
			`, remaining)
		deleted += result.RowsAffected
		return result.Error
	})
	if err != nil {
		return 0, fmt.Errorf("dream diagnostic purge: %w", err)
	}
	return int(deleted), nil
}

func scanDreamDiagnosticCapture(rows *sql.Rows, teamID string) (*dreamcontract.DreamDiagnosticCapture, error) {
	var item dreamcontract.DreamDiagnosticCapture
	var details []byte
	var payload []byte
	var captured sql.NullTime
	if err := rows.Scan(&item.CaptureID, &item.RunID, &item.HypothesisID, &item.Phase, &item.Outcome, &item.Cause, &details, &payload,
		&item.CaptureState, &item.CaptureReason, &captured, &item.ExpiresAt, &item.CreatedAt); err != nil {
		return nil, err
	}
	item.TeamID = teamID
	if string(payload) != "{}" {
		item.Payload = append([]byte(nil), payload...)
	}
	return finishDreamDiagnosticCapture(&item, details, captured)
}

func scanDreamDiagnosticCaptureRow(row *sql.Row, teamID string) (*dreamcontract.DreamDiagnosticCapture, error) {
	var item dreamcontract.DreamDiagnosticCapture
	var details []byte
	var payload []byte
	var captured sql.NullTime
	if err := row.Scan(&item.CaptureID, &item.RunID, &item.HypothesisID, &item.Phase, &item.Outcome, &item.Cause, &details, &payload,
		&item.CaptureState, &item.CaptureReason, &captured, &item.ExpiresAt, &item.CreatedAt); err != nil {
		return nil, err
	}
	item.TeamID = teamID
	if string(payload) != "{}" {
		item.Payload = append([]byte(nil), payload...)
	}
	return finishDreamDiagnosticCapture(&item, details, captured)
}

func finishDreamDiagnosticCapture(item *dreamcontract.DreamDiagnosticCapture, details []byte, captured sql.NullTime) (*dreamcontract.DreamDiagnosticCapture, error) {
	item.Details = map[string]any{}
	if len(details) > 0 {
		if err := json.Unmarshal(details, &item.Details); err != nil {
			return nil, err
		}
	}
	if captured.Valid {
		value := captured.Time.UTC()
		item.CapturedAt = &value
	}
	item.ExpiresAt = item.ExpiresAt.UTC()
	item.CreatedAt = item.CreatedAt.UTC()
	return item, nil
}

type dreamDiagnosticCursor struct {
	createdAt time.Time
	captureID string
}

func encodeDreamDiagnosticCursor(createdAt time.Time, captureID string) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + captureID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeDreamDiagnosticCursor(value string) (*dreamDiagnosticCursor, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, dreamcontract.ErrInvalidDreamDiagnosticCursor
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 2 {
		return nil, dreamcontract.ErrInvalidDreamDiagnosticCursor
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, dreamcontract.ErrInvalidDreamDiagnosticCursor
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return nil, dreamcontract.ErrInvalidDreamDiagnosticCursor
	}
	return &dreamDiagnosticCursor{createdAt: createdAt, captureID: parts[1]}, nil
}
