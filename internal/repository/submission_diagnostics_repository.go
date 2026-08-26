package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrSubmissionDiagnosticNotFound = errors.New("remember attempt not found")

type SubmissionDiagnosticsRepository interface {
	ListSubmissionDiagnostics(context.Context, SubmissionDiagnosticFilter) (*SubmissionDiagnosticRecordPage, error)
	GetSubmissionDiagnostic(context.Context, string, string) (*SubmissionDiagnosticRecord, error)
}

type SubmissionDiagnosticFilter struct {
	TeamID          string
	ProcessingState string
	Limit           int
	Offset          int
}

// SubmissionDiagnosticRecord is the control-only projection of one terminal
// Remember attempt. It contains no placement or queue identifiers.
type SubmissionDiagnosticRecord struct {
	TeamID            string
	TeamName          string
	OwnerProfileID    string
	SubmissionID      string
	ProcessingState   string
	CorrelationID     string
	FailedPhase       string
	ErrorCode         string
	EvidenceCount     int
	RelationshipCount int
	DocumentCount     int
	AssessorTurns     int
	Duration          time.Duration
	CreatedAt         time.Time
	CompletedAt       *time.Time
	PublicResult      map[string]any
	Events            []SubmissionDiagnosticEvent
	Artifacts         []RememberFailureArtifactDescriptor
}

type SubmissionDiagnosticEvent struct {
	SequenceNo int
	Phase      string
	EventKind  string
	Outcome    string
	Metadata   map[string]any
	CreatedAt  time.Time
}

type SubmissionDiagnosticRecordPage struct {
	Records []SubmissionDiagnosticRecord
	Total   int64
}

var _ SubmissionDiagnosticsRepository = (*LedgerRepositoryImpl)(nil)

func normalizeRememberAttemptDiagnosticFilter(filter SubmissionDiagnosticFilter) SubmissionDiagnosticFilter {
	filter.TeamID = strings.TrimSpace(filter.TeamID)
	filter.ProcessingState = strings.TrimSpace(filter.ProcessingState)
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 100 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return filter
}

func validateRememberAttemptDiagnosticFilter(filter SubmissionDiagnosticFilter) error {
	if filter.TeamID != "" {
		if _, err := uuid.Parse(filter.TeamID); err != nil {
			return fmt.Errorf("team_id is invalid: %w", err)
		}
	}
	switch filter.ProcessingState {
	case "", "completed", "rejected", "quarantined", "failed", "replayed":
		return nil
	default:
		return fmt.Errorf("processing_state is unsupported")
	}
}

func (r *LedgerRepositoryImpl) ListSubmissionDiagnostics(ctx context.Context, filter SubmissionDiagnosticFilter) (*SubmissionDiagnosticRecordPage, error) {
	filter = normalizeRememberAttemptDiagnosticFilter(filter)
	if err := validateRememberAttemptDiagnosticFilter(filter); err != nil {
		return nil, err
	}
	page := &SubmissionDiagnosticRecordPage{Records: []SubmissionDiagnosticRecord{}}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(*)
			FROM remember_attempts AS attempt
			JOIN teams AS team ON team.id = attempt.team_id
			WHERE (? = '' OR attempt.team_id = NULLIF(?, '')::uuid)
			  AND (? = '' OR attempt.outcome = ?)
		`, filter.TeamID, filter.TeamID, filter.ProcessingState, filter.ProcessingState).Scan(&page.Total).Error; err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT attempt.team_id::text, team.name, attempt.owner_profile_id::text,
			       attempt.attempt_id::text, attempt.outcome,
			       attempt.correlation_id, attempt.failed_phase, attempt.error_code,
			       attempt.evidence_count, attempt.relationship_count,
			       attempt.document_count, attempt.assessor_turns,
			       attempt.duration_ms, attempt.created_at, attempt.completed_at,
			       attempt.public_result
			FROM remember_attempts AS attempt
			JOIN teams AS team ON team.id = attempt.team_id
			WHERE (? = '' OR attempt.team_id = NULLIF(?, '')::uuid)
			  AND (? = '' OR attempt.outcome = ?)
			ORDER BY attempt.created_at DESC, attempt.attempt_id DESC
			LIMIT ? OFFSET ?
		`, filter.TeamID, filter.TeamID, filter.ProcessingState, filter.ProcessingState, filter.Limit, filter.Offset).Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
			record, err := scanRememberAttemptDiagnostic(rows)
			if err != nil {
				return err
			}
			page.Records = append(page.Records, record)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for index := range page.Records {
			record := &page.Records[index]
			record.Events, err = loadRememberAttemptEvents(ctx, tx, record.TeamID, record.SubmissionID)
			if err != nil {
				return err
			}
			record.Artifacts, err = loadRememberFailureArtifactDescriptors(ctx, tx, record.TeamID, record.SubmissionID)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

func (r *LedgerRepositoryImpl) GetSubmissionDiagnostic(ctx context.Context, teamID, submissionID string) (*SubmissionDiagnosticRecord, error) {
	teamID, submissionID = strings.TrimSpace(teamID), strings.TrimSpace(submissionID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is invalid: %w", err)
	}
	if _, err := uuid.Parse(submissionID); err != nil {
		return nil, fmt.Errorf("submission_id is invalid: %w", err)
	}
	var record *SubmissionDiagnosticRecord
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT attempt.team_id::text, team.name, attempt.owner_profile_id::text,
			       attempt.attempt_id::text, attempt.outcome,
			       attempt.correlation_id, attempt.failed_phase, attempt.error_code,
			       attempt.evidence_count, attempt.relationship_count,
			       attempt.document_count, attempt.assessor_turns,
			       attempt.duration_ms, attempt.created_at, attempt.completed_at,
			       attempt.public_result
			FROM remember_attempts AS attempt
			JOIN teams AS team ON team.id = attempt.team_id
			WHERE attempt.team_id = ?::uuid AND attempt.attempt_id = ?::uuid
		`, teamID, submissionID).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			_ = rows.Close()
			return ErrSubmissionDiagnosticNotFound
		}
		value, err := scanRememberAttemptDiagnostic(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		value.Events, err = loadRememberAttemptEvents(ctx, tx, teamID, submissionID)
		if err != nil {
			return err
		}
		value.Artifacts, err = loadRememberFailureArtifactDescriptors(ctx, tx, teamID, submissionID)
		if err != nil {
			return err
		}
		record = &value
		return nil
	})
	if errors.Is(err, ErrSubmissionDiagnosticNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return record, nil
}

type rememberAttemptDiagnosticScanner interface {
	Scan(...any) error
}

func scanRememberAttemptDiagnostic(scanner rememberAttemptDiagnosticScanner) (SubmissionDiagnosticRecord, error) {
	var record SubmissionDiagnosticRecord
	var completedAt sql.NullTime
	var publicJSON []byte
	var durationMS int64
	if err := scanner.Scan(
		&record.TeamID, &record.TeamName, &record.OwnerProfileID,
		&record.SubmissionID, &record.ProcessingState,
		&record.CorrelationID, &record.FailedPhase, &record.ErrorCode,
		&record.EvidenceCount, &record.RelationshipCount,
		&record.DocumentCount, &record.AssessorTurns,
		&durationMS, &record.CreatedAt, &completedAt, &publicJSON,
	); err != nil {
		return SubmissionDiagnosticRecord{}, err
	}
	record.Duration = time.Duration(durationMS) * time.Millisecond
	if completedAt.Valid {
		value := completedAt.Time.UTC()
		record.CompletedAt = &value
	}
	if len(publicJSON) > 0 {
		if err := json.Unmarshal(publicJSON, &record.PublicResult); err != nil {
			return SubmissionDiagnosticRecord{}, err
		}
	}
	return record, nil
}

func loadRememberAttemptEvents(ctx context.Context, tx *gorm.DB, teamID, attemptID string) ([]SubmissionDiagnosticEvent, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT sequence_no, phase, event_kind, outcome, metadata, created_at
		FROM remember_attempt_events
		WHERE team_id = ?::uuid AND attempt_id = ?::uuid
		ORDER BY sequence_no ASC
	`, teamID, attemptID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []SubmissionDiagnosticEvent{}
	for rows.Next() {
		var event SubmissionDiagnosticEvent
		var metadataJSON []byte
		if err := rows.Scan(&event.SequenceNo, &event.Phase, &event.EventKind, &event.Outcome, &metadataJSON, &event.CreatedAt); err != nil {
			return nil, err
		}
		if len(metadataJSON) > 0 {
			if err := json.Unmarshal(metadataJSON, &event.Metadata); err != nil {
				return nil, err
			}
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func loadRememberFailureArtifactDescriptors(ctx context.Context, tx *gorm.DB, teamID, attemptID string) ([]RememberFailureArtifactDescriptor, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT artifact_id::text, artifact_kind, content_type, byte_count,
		       content_sha256, captured_at, expires_at
		FROM remember_failure_artifacts
		WHERE team_id = ?::uuid AND attempt_id = ?::uuid
		ORDER BY captured_at ASC, artifact_id ASC
	`, teamID, attemptID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RememberFailureArtifactDescriptor{}
	for rows.Next() {
		var item RememberFailureArtifactDescriptor
		if err := rows.Scan(&item.ArtifactID, &item.ArtifactKind, &item.ContentType, &item.ByteCount, &item.ContentSHA256, &item.CapturedAt, &item.ExpiresAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
