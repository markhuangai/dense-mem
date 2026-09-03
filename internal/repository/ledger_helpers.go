package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func insertEvidenceQuarantine(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string, fragmentID string, reason string) error {
	if err := lockEvidenceLifecycleTarget(ctx, tx, input.TeamID, fragmentID); err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO evidence_quarantines (
		    team_id, fragment_id, ingest_id, owner_profile_id, reason,
		    space_id, space_generation
		)
		SELECT ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?,
		       fragment.space_id, fragment.space_generation
		FROM evidence_fragments AS fragment
		WHERE fragment.team_id = ?::uuid
		  AND fragment.fragment_id = ?::uuid
		  AND fragment.ingest_id = ?::uuid
		  AND fragment.owner_profile_id = ?::uuid
		ON CONFLICT (team_id, fragment_id) DO NOTHING
	`, input.TeamID, fragmentID, ingestID, input.OwnerProfileID, strings.TrimSpace(reason),
		input.TeamID, fragmentID, ingestID, input.OwnerProfileID).Error
}

func insertSecuritySignals(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, eventID, spaceID string, spaceGeneration int64, signals []SecuritySignalInput) error {
	if len(signals) == 0 {
		return nil
	}

	values := make([]string, 0, len(signals))
	args := make([]any, 0, len(signals)*12)
	for i, signal := range signals {
		metadata, err := marshalJSON(signal.Metadata)
		if err != nil {
			return err
		}
		values = append(values, "(?::uuid, ?::uuid, ?, ?::uuid, ?, ?, ?, ?, ?, ?::jsonb, NULLIF(?, '')::uuid, NULLIF(?::bigint, 0))")
		args = append(args,
			teamID,
			eventID,
			i,
			ownerProfileID,
			signal.Kind,
			signal.Severity,
			signal.SpanStart,
			signal.SpanEnd,
			signal.Quote,
			string(metadata),
			spaceID,
			spaceGeneration,
		)
	}

	return tx.WithContext(ctx).Exec(`
		INSERT INTO evidence_security_signals (
		    team_id, security_event_id, signal_index, owner_profile_id,
		    kind, severity, span_start, span_end, quote, metadata,
		    space_id, space_generation
		) VALUES `+strings.Join(values, ", "), args...).Error
}

func ensureEvidenceEventOwnership(ctx context.Context, tx *gorm.DB, input SecurityEventInput) error {
	occurrenceID := input.OccurrenceID
	if occurrenceID == "" {
		occurrenceID = input.FragmentID
	}
	var exists bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM evidence_occurrences AS occurrence
			WHERE occurrence.team_id = ?::uuid
			  AND occurrence.occurrence_id = ?::uuid
			  AND occurrence.canonical_fragment_id = ?::uuid
			  AND occurrence.ingest_id = ?::uuid
			  AND occurrence.owner_profile_id = ?::uuid
		)
	`, input.TeamID, occurrenceID, input.FragmentID, input.IngestID, input.OwnerProfileID).Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: security event target evidence does not belong to owner and ingest", ErrSemanticOwnerMismatch)
	}
	return nil
}

func isPostgresUniqueConstraint(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func isPostgresForeignKeyConstraint(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == constraint
}

func translateSourceCreateError(err error) error {
	if err == nil {
		return nil
	}
	if isPostgresUniqueConstraint(err, "evidence_sources_owner_key_unique") {
		return fmt.Errorf("%w: source was created concurrently", ErrSourceRevisionConflict)
	}
	return err
}

func normalizeSecurityEventInput(input SecurityEventInput) SecurityEventInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.FragmentID = strings.TrimSpace(input.FragmentID)
	input.OccurrenceID = strings.TrimSpace(input.OccurrenceID)
	input.EvidenceOwnerProfileID = strings.TrimSpace(input.EvidenceOwnerProfileID)
	input.SecurityEventDraft = normalizeSecurityEventDraft(input.SecurityEventDraft)
	return input
}

func normalizeSecurityEventDraft(input SecurityEventDraft) SecurityEventDraft {
	input.EventKind = strings.TrimSpace(input.EventKind)
	input.Decision = strings.TrimSpace(input.Decision)
	input.Reason = strings.TrimSpace(input.Reason)
	for i := range input.Signals {
		input.Signals[i].Kind = strings.TrimSpace(input.Signals[i].Kind)
		input.Signals[i].Severity = strings.TrimSpace(input.Signals[i].Severity)
		input.Signals[i].Quote = strings.TrimSpace(input.Signals[i].Quote)
	}
	return input
}

func validateSecurityEventInput(input SecurityEventInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.IngestID); err != nil {
		return fmt.Errorf("ingest_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.FragmentID); err != nil {
		return fmt.Errorf("fragment_id is required: %w", err)
	}
	if input.OccurrenceID != "" {
		if _, err := uuid.Parse(input.OccurrenceID); err != nil {
			return fmt.Errorf("occurrence_id is invalid: %w", err)
		}
	}
	if input.EvidenceOwnerProfileID != "" {
		if _, err := uuid.Parse(input.EvidenceOwnerProfileID); err != nil {
			return fmt.Errorf("evidence_owner_profile_id is invalid: %w", err)
		}
	}
	return validateSecurityEventDraft(input.SecurityEventDraft)
}

func validateSecurityEventDraft(input SecurityEventDraft) error {
	switch input.EventKind {
	case "deterministic_scan", "reviewer_signal", "verifier_signal", "quarantine_release":
	default:
		return fmt.Errorf("unsupported event_kind %q", input.EventKind)
	}
	switch input.Decision {
	case "pass", "guarded", "quarantine", "released":
	default:
		return fmt.Errorf("unsupported decision %q", input.Decision)
	}
	for i, signal := range input.Signals {
		switch signal.Kind {
		case "role_control_spoofing", "instruction_override", "prompt_secret_extraction", "tool_exfiltration", "obfuscated_instruction", "hidden_control_markup":
		default:
			return fmt.Errorf("signals[%d].kind is unsupported", i)
		}
		switch signal.Severity {
		case "low", "medium", "high", "critical":
		default:
			return fmt.Errorf("signals[%d].severity is unsupported", i)
		}
		if signal.SpanStart < 0 || signal.SpanEnd <= signal.SpanStart {
			return fmt.Errorf("signals[%d].span is invalid", i)
		}
	}
	return nil
}

func insertSecurityEvent(ctx context.Context, tx *gorm.DB, input SecurityEventInput) (string, error) {
	metadata, err := marshalJSON(input.Metadata)
	if err != nil {
		return "", err
	}
	occurrenceID := input.OccurrenceID
	if occurrenceID == "" {
		occurrenceID = input.FragmentID
	}
	evidenceOwnerID := input.EvidenceOwnerProfileID
	if evidenceOwnerID == "" {
		evidenceOwnerID = input.OwnerProfileID
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_security_events (
		    team_id, fragment_id, occurrence_id, evidence_owner_profile_id, ingest_id, owner_profile_id, event_kind, decision,
		    reason, metadata, space_id, space_generation
		)
		SELECT ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?::jsonb,
		       occurrence.space_id, occurrence.space_generation
		FROM evidence_occurrences AS occurrence
		WHERE occurrence.team_id = ?::uuid
		  AND occurrence.occurrence_id = ?::uuid
		  AND occurrence.ingest_id = ?::uuid
		  AND occurrence.owner_profile_id = ?::uuid
		RETURNING security_event_id::text, COALESCE(space_id::text, ''), COALESCE(space_generation, 0)
	`, input.TeamID, input.FragmentID, occurrenceID, evidenceOwnerID, input.IngestID, input.OwnerProfileID,
		input.EventKind, input.Decision, input.Reason, string(metadata),
		input.TeamID, occurrenceID, input.IngestID, input.OwnerProfileID).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		_ = rows.Close()
		return "", sql.ErrNoRows
	}
	var eventID string
	var spaceID string
	var spaceGeneration int64
	if err := rows.Scan(&eventID, &spaceID, &spaceGeneration); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if err := insertSecuritySignals(ctx, tx, input.TeamID, input.OwnerProfileID, eventID, spaceID, spaceGeneration, input.Signals); err != nil {
		return "", err
	}
	return eventID, nil
}

func marshalJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return data, nil
}

func pqStringArray(values []string) any {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	return pq.Array(normalized)
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}
