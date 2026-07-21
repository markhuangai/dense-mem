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

func insertV2SecuritySignals(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, eventID string, signals []V2SecuritySignalInput) error {
	if len(signals) == 0 {
		return nil
	}

	values := make([]string, 0, len(signals))
	args := make([]any, 0, len(signals)*10)
	for i, signal := range signals {
		metadata, err := marshalV2JSON(signal.Metadata)
		if err != nil {
			return err
		}
		values = append(values, "(?::uuid, ?::uuid, ?, ?::uuid, ?, ?, ?, ?, ?, ?::jsonb)")
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
		)
	}

	return tx.WithContext(ctx).Exec(`
		INSERT INTO evidence_security_signals (
		    team_id, security_event_id, signal_index, owner_profile_id,
		    kind, severity, span_start, span_end, quote, metadata
		) VALUES `+strings.Join(values, ", "), args...).Error
}

func ensureV2EvidenceEventOwnership(ctx context.Context, tx *gorm.DB, input V2SecurityEventInput) error {
	var exists bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM evidence_fragments AS fragment
			JOIN knowledge_ingests AS ingest
			  ON ingest.team_id = fragment.team_id
			 AND ingest.ingest_id = fragment.ingest_id
			 AND ingest.owner_profile_id = fragment.owner_profile_id
			WHERE fragment.team_id = ?::uuid
			  AND fragment.fragment_id = ?::uuid
			  AND fragment.ingest_id = ?::uuid
			  AND fragment.owner_profile_id = ?::uuid
		)
	`, input.TeamID, input.FragmentID, input.IngestID, input.OwnerProfileID).Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: security event target evidence does not belong to owner and ingest", ErrV2SemanticOwnerMismatch)
	}
	return nil
}

func isPostgresUniqueConstraint(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func translateV2SourceCreateError(err error) error {
	if err == nil {
		return nil
	}
	if isPostgresUniqueConstraint(err, "evidence_sources_owner_key_unique") {
		return fmt.Errorf("%w: source was created concurrently", ErrV2SourceRevisionConflict)
	}
	return err
}

func hydrateV2PlacementItemSearchStates(ctx context.Context, tx *gorm.DB, teamID string, ownerProfileID string, items []V2PlacementItem) error {
	ids := make([]string, 0)
	seen := map[string]struct{}{}
	for _, item := range items {
		for _, id := range v2PlacementResultStringSlice(item.Result, "search_document_ids") {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT search_document_id::text, search_state
		FROM search_documents
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND search_document_id = ANY(?::uuid[])
	`, teamID, ownerProfileID, pq.Array(ids)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	statesByID := map[string]string{}
	for rows.Next() {
		var id string
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			return err
		}
		statesByID[id] = state
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		documentIDs := v2PlacementResultStringSlice(items[i].Result, "search_document_ids")
		if len(documentIDs) == 0 {
			continue
		}
		states := make([]string, 0, len(documentIDs))
		for _, id := range documentIDs {
			if state := statesByID[id]; state != "" {
				states = append(states, state)
			}
		}
		if len(states) == 0 {
			continue
		}
		if items[i].Result == nil {
			items[i].Result = map[string]any{}
		}
		items[i].Result["search_document_states"] = states
	}
	return nil
}

func v2PlacementResultStringSlice(result map[string]any, key string) []string {
	if len(result) == 0 {
		return nil
	}
	switch values := result[key].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if str, ok := value.(string); ok && strings.TrimSpace(str) != "" {
				out = append(out, strings.TrimSpace(str))
			}
		}
		return out
	default:
		return nil
	}
}

func normalizeV2SecurityEventInput(input V2SecurityEventInput) V2SecurityEventInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.FragmentID = strings.TrimSpace(input.FragmentID)
	input.V2SecurityEventDraft = normalizeV2SecurityEventDraft(input.V2SecurityEventDraft)
	return input
}

func normalizeV2SecurityEventDraft(input V2SecurityEventDraft) V2SecurityEventDraft {
	input.EventKind = strings.TrimSpace(input.EventKind)
	input.Decision = strings.TrimSpace(input.Decision)
	input.ScanPolicyHash = strings.TrimSpace(input.ScanPolicyHash)
	input.Reason = strings.TrimSpace(input.Reason)
	for i := range input.Signals {
		input.Signals[i].Kind = strings.TrimSpace(input.Signals[i].Kind)
		input.Signals[i].Severity = strings.TrimSpace(input.Signals[i].Severity)
		input.Signals[i].Quote = strings.TrimSpace(input.Signals[i].Quote)
	}
	return input
}

func validateV2SecurityEventInput(input V2SecurityEventInput) error {
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
	return validateV2SecurityEventDraft(input.V2SecurityEventDraft)
}

func validateV2SecurityEventDraft(input V2SecurityEventDraft) error {
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

func insertV2SecurityEvent(ctx context.Context, tx *gorm.DB, input V2SecurityEventInput) (string, error) {
	metadata, err := marshalV2JSON(input.Metadata)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_security_events (
		    team_id, fragment_id, ingest_id, owner_profile_id, event_kind, decision,
		    scan_policy_hash, reason, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?::jsonb
		)
		RETURNING security_event_id::text
	`, input.TeamID, input.FragmentID, input.IngestID, input.OwnerProfileID,
		input.EventKind, input.Decision, input.ScanPolicyHash, input.Reason, string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		_ = rows.Close()
		return "", sql.ErrNoRows
	}
	var eventID string
	if err := rows.Scan(&eventID); err != nil {
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
	if err := insertV2SecuritySignals(ctx, tx, input.TeamID, input.OwnerProfileID, eventID, input.Signals); err != nil {
		return "", err
	}
	return eventID, nil
}

func marshalV2JSON(value map[string]any) ([]byte, error) {
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
