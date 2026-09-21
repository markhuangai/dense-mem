package postgres

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

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

type RememberInvocationDiagnosticInput = knowledgecontract.RememberInvocationDiagnosticInput
type RememberInvocationDiagnosticRecord = knowledgecontract.RememberInvocationDiagnosticRecord
type RememberInvocationDiagnosticFilter = knowledgecontract.RememberInvocationDiagnosticFilter
type RememberInvocationDiagnosticRecordPage = knowledgecontract.RememberInvocationDiagnosticRecordPage

var ErrRememberInvocationDiagnosticNotFound = knowledgecontract.ErrRememberInvocationDiagnosticNotFound

const rememberInvocationDiagnosticRetention = 7 * 24 * time.Hour

type rememberInvocationExchange struct {
	DiagnosticID        string    `json:"diagnostic_id,omitempty"`
	SequenceNo          int       `json:"sequence_no"`
	Kind                string    `json:"kind"`
	Component           string    `json:"component"`
	Model               string    `json:"model,omitempty"`
	RequestBody         string    `json:"request_body,omitempty"`
	ResponseBody        string    `json:"response_body,omitempty"`
	RequestContentType  string    `json:"request_content_type,omitempty"`
	ResponseContentType string    `json:"response_content_type,omitempty"`
	StatusCode          int       `json:"status_code,omitempty"`
	Outcome             string    `json:"outcome"`
	CaptureState        string    `json:"capture_state"`
	CaptureReason       string    `json:"capture_reason,omitempty"`
	CapturedAt          time.Time `json:"captured_at,omitempty"`
	ExpiresAt           time.Time `json:"expires_at,omitempty"`
}

func rememberInvocationExchanges(input []knowledgecontract.RememberAttemptDiagnosticInput, limits ...int) ([]rememberInvocationExchange, error) {
	maxBytes := 64 << 20
	if len(limits) > 0 && limits[0] > 0 {
		maxBytes = limits[0]
	}
	result := make([]rememberInvocationExchange, 0, len(input))
	for _, item := range input {
		result = append(result, rememberInvocationExchange{
			DiagnosticID: item.DiagnosticID, SequenceNo: item.SequenceNo, Kind: item.Kind,
			Component: item.Component, Model: item.Model, RequestBody: string(item.RequestBody),
			ResponseBody: string(item.ResponseBody), RequestContentType: item.RequestContentType,
			ResponseContentType: item.ResponseContentType, StatusCode: item.StatusCode, Outcome: item.Outcome,
			CaptureState: item.CaptureState, CapturedAt: item.CapturedAt, ExpiresAt: item.ExpiresAt,
			CaptureReason: item.CaptureReason,
		})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	for len(encoded) > maxBytes {
		trimmed := false
		for index := len(result) - 1; index >= 0 && len(encoded) > maxBytes; index-- {
			item := &result[index]
			if len(item.ResponseBody) > 0 {
				keep := len(item.ResponseBody) - (len(encoded) - maxBytes)
				if keep < 0 {
					keep = 0
				}
				item.ResponseBody = item.ResponseBody[:keep]
				if item.CaptureState != "unavailable" {
					item.CaptureState = "truncated"
				}
				trimmed = true
				encoded, _ = json.Marshal(result)
			}
			if len(item.RequestBody) > 0 && len(encoded) > maxBytes {
				keep := len(item.RequestBody) - (len(encoded) - maxBytes)
				if keep < 0 {
					keep = 0
				}
				item.RequestBody = item.RequestBody[:keep]
				if item.CaptureState != "unavailable" {
					item.CaptureState = "truncated"
				}
				trimmed = true
				encoded, _ = json.Marshal(result)
			}
		}
		if !trimmed {
			return nil, fmt.Errorf("remember invocation diagnostics exceed %d bytes", maxBytes)
		}
	}
	return result, nil
}

func rememberInvocationExchangesForJSONB(
	ctx context.Context,
	tx *gorm.DB,
	input []knowledgecontract.RememberAttemptDiagnosticInput,
	maxBytes int,
) ([]byte, int64, error) {
	compactLimit := maxBytes
	for range 16 {
		exchanges, err := rememberInvocationExchanges(input, compactLimit)
		if err != nil {
			return nil, 0, err
		}
		exchangesJSON, err := json.Marshal(exchanges)
		if err != nil {
			return nil, 0, err
		}
		var providerExchangeBytes int64
		if err := tx.WithContext(ctx).Raw(
			`SELECT octet_length(?::jsonb::text)`, string(exchangesJSON),
		).Row().Scan(&providerExchangeBytes); err != nil {
			return nil, 0, err
		}
		if providerExchangeBytes <= int64(maxBytes) {
			return exchangesJSON, providerExchangeBytes, nil
		}
		excess := providerExchangeBytes - int64(maxBytes)
		if excess >= int64(compactLimit) {
			return nil, 0, fmt.Errorf("remember invocation: provider exchanges exceed %d bytes", maxBytes)
		}
		compactLimit -= int(excess)
	}
	return nil, 0, fmt.Errorf("remember invocation: provider exchanges could not be bounded to %d bytes", maxBytes)
}

func validateRememberInvocationDiagnostic(input knowledgecontract.RememberInvocationDiagnosticInput) error {
	for name, value := range map[string]string{
		"team_id": input.TeamID, "owner_profile_id": input.OwnerProfileID, "invocation_id": input.InvocationID,
	} {
		if _, err := uuid.Parse(strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("remember invocation: %s is required: %w", name, err)
		}
	}
	if input.CanonicalAttemptID != "" {
		if _, err := uuid.Parse(input.CanonicalAttemptID); err != nil {
			return fmt.Errorf("remember invocation: canonical_attempt_id is invalid: %w", err)
		}
	}
	if input.Classification != "execution" && input.Classification != "replay" && input.Classification != "conflict" {
		return fmt.Errorf("remember invocation: classification is unsupported")
	}
	switch input.Outcome {
	case "completed", "evaluated_zero", "failed", "cancelled", "replayed", "conflict":
	default:
		return fmt.Errorf("remember invocation: outcome is unsupported")
	}
	if input.Duration < 0 {
		return fmt.Errorf("remember invocation: duration cannot be negative")
	}
	if err := validateRememberInvocationCapture(input.RequestCaptureState, input.RequestCaptureReason, "request"); err != nil {
		return err
	}
	if err := validateRememberInvocationCapture(input.CallerResponseCaptureState, input.CallerResponseCaptureReason, "response"); err != nil {
		return err
	}
	if len(input.RequestBody) > 16<<20 || len(input.CallerResponse) > 16<<20 {
		return fmt.Errorf("remember invocation: body exceeds %d bytes", 16<<20)
	}
	for index, exchange := range input.ProviderExchanges {
		if len(exchange.RequestBody) > 16<<20 || len(exchange.ResponseBody) > 16<<20 {
			return fmt.Errorf("remember invocation: provider exchange %d body exceeds %d bytes", index, 16<<20)
		}
		if err := validateRememberInvocationCapture(exchange.CaptureState, exchange.CaptureReason, fmt.Sprintf("provider exchange %d", index)); err != nil {
			return err
		}
	}
	return nil
}

func validateRememberInvocationCapture(state, reason, field string) error {
	switch strings.TrimSpace(state) {
	case "", "captured", "truncated", "not_captured", "hash_only", "provider_not_called", "no_response", "interrupted", "not_delivered", "unavailable":
	default:
		return fmt.Errorf("remember invocation: %s capture state %q is unsupported", field, state)
	}
	if len(reason) > 128 {
		return fmt.Errorf("remember invocation: %s capture reason exceeds 128 bytes", field)
	}
	return nil
}

func (r *Store) RecordRememberInvocationDiagnostic(ctx context.Context, input knowledgecontract.RememberInvocationDiagnosticInput) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.InvocationID = strings.TrimSpace(input.InvocationID)
	input.CanonicalAttemptID = strings.TrimSpace(input.CanonicalAttemptID)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	input.Classification = strings.TrimSpace(input.Classification)
	input.Outcome = strings.TrimSpace(input.Outcome)
	input.RequestCaptureState = strings.TrimSpace(input.RequestCaptureState)
	input.RequestCaptureReason = strings.TrimSpace(input.RequestCaptureReason)
	input.CallerResponseCaptureState = strings.TrimSpace(input.CallerResponseCaptureState)
	input.CallerResponseCaptureReason = strings.TrimSpace(input.CallerResponseCaptureReason)
	if input.Classification == "" {
		input.Classification = "execution"
	}
	if input.Outcome == "" {
		input.Outcome = "completed"
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC()
	}
	if input.CompletedAt.IsZero() {
		input.CompletedAt = input.CreatedAt
	}
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = input.CreatedAt.Add(rememberInvocationDiagnosticRetention)
	}
	if input.RequestCaptureState == "" {
		input.RequestCaptureState = knowledgecontract.DiagnosticCaptureState("", "", len(input.RequestBody), 0)
	}
	if input.CallerResponseCaptureState == "" {
		input.CallerResponseCaptureState = knowledgecontract.DiagnosticCaptureState("", "", 0, len(input.CallerResponse))
	}
	for index := range input.ProviderExchanges {
		exchange := &input.ProviderExchanges[index]
		exchange.CaptureState = strings.TrimSpace(exchange.CaptureState)
		exchange.CaptureReason = strings.TrimSpace(exchange.CaptureReason)
		if exchange.CaptureState == "" {
			exchange.CaptureState = knowledgecontract.DiagnosticCaptureState("", exchange.Outcome, len(exchange.RequestBody), len(exchange.ResponseBody))
		}
	}
	if err := validateRememberInvocationDiagnostic(input); err != nil {
		return err
	}
	if input.ExpiresAt.Before(input.CreatedAt) || input.ExpiresAt.After(input.CreatedAt.Add(rememberInvocationDiagnosticRetention)) {
		return fmt.Errorf("remember invocation: expiry is outside retention")
	}
	maxExchangeBytes := (64 << 20) - len(input.RequestBody) - len(input.CallerResponse)
	if maxExchangeBytes <= 0 {
		return fmt.Errorf("remember invocation: payload exceeds %d bytes", 64<<20)
	}
	requestBody := input.RequestBody
	if requestBody == nil {
		requestBody = []byte{}
	}
	responseBody := input.CallerResponse
	if responseBody == nil {
		responseBody = []byte{}
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if input.SpaceID != "" {
			var locked bool
			if err := tx.WithContext(ctx).Raw(`
				SELECT dense_mem_lock_memory_space(?::uuid, ?::uuid)
			`, input.TeamID, input.SpaceID).Row().Scan(&locked); err != nil {
				return err
			}
			if !locked {
				return fmt.Errorf("remember invocation: private memory space is unavailable")
			}
			var currentGeneration sql.NullInt64
			if err := tx.WithContext(ctx).Raw(`
				SELECT dense_mem_active_space_generation(?::uuid, ?::uuid)
			`, input.TeamID, input.SpaceID).Row().Scan(&currentGeneration); err != nil {
				return err
			}
			if !currentGeneration.Valid {
				return fmt.Errorf("remember invocation: private memory space is unavailable")
			}
			if currentGeneration.Int64 != input.SpaceGeneration {
				return fmt.Errorf("remember invocation: private memory space generation is stale")
			}
		}
		exchangesJSON, providerExchangeBytes, err := rememberInvocationExchangesForJSONB(ctx, tx, input.ProviderExchanges, maxExchangeBytes)
		if err != nil {
			return err
		}
		if int64(len(requestBody))+int64(len(responseBody))+providerExchangeBytes > 64<<20 {
			return fmt.Errorf("remember invocation: payload exceeds %d bytes", 64<<20)
		}
		return tx.WithContext(ctx).Exec(`
			INSERT INTO remember_invocation_diagnostics (
				team_id, invocation_id, owner_profile_id, space_id, space_generation,
				canonical_attempt_id, request_hash, correlation_id, classification, outcome,
				failed_phase, error_code, retryable, duration_ms, request_bytes, request_capture_state, request_capture_reason,
				provider_exchanges, response_bytes, response_capture_state, response_capture_reason, created_at, completed_at, expires_at)
			VALUES (?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, ?, NULLIF(?, '')::uuid,
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?)
				`, input.TeamID, input.InvocationID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
			input.CanonicalAttemptID, input.RequestHash, input.CorrelationID, input.Classification, input.Outcome,
			input.FailedPhase, input.ErrorCode, input.Retryable, input.Duration.Milliseconds(), requestBody, input.RequestCaptureState, input.RequestCaptureReason, string(exchangesJSON),
			responseBody, input.CallerResponseCaptureState, input.CallerResponseCaptureReason, input.CreatedAt.UTC(), input.CompletedAt.UTC(), input.ExpiresAt.UTC()).Error
	})
	if err != nil {
		return err
	}
	if input.SpaceID != "" {
		if err := r.synchronizeRememberAttemptDiagnosticHold(ctx, input.SpaceID); err != nil {
			return fmt.Errorf("%w: %v", ErrRememberFailureRetentionDegraded, err)
		}
	}
	return nil
}

func (r *Store) ListRememberInvocationDiagnostics(ctx context.Context, filter knowledgecontract.RememberInvocationDiagnosticFilter) (*knowledgecontract.RememberInvocationDiagnosticRecordPage, error) {
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	page := &knowledgecontract.RememberInvocationDiagnosticRecordPage{Records: []knowledgecontract.RememberInvocationDiagnosticRecord{}}
	err := r.withSystemReadOnlyRepeatableTx(ctx, func(tx *gorm.DB) error {
		where := `WHERE (NULLIF(?, '')::uuid IS NULL OR team_id = NULLIF(?, '')::uuid)
			AND (NULLIF(?, '')::uuid IS NULL OR owner_profile_id = NULLIF(?, '')::uuid)
			AND (? = '' OR outcome = ?)`
		if err := tx.WithContext(ctx).Raw(`SELECT count(*) FROM remember_invocation_diagnostics `+where,
			filter.TeamID, filter.TeamID, filter.OwnerProfileID, filter.OwnerProfileID, filter.Outcome, filter.Outcome).Scan(&page.Total).Error; err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, owner_profile_id::text, invocation_id::text,
			       COALESCE(canonical_attempt_id::text, ''), COALESCE(space_id::text, ''),
			       space_generation, request_hash, correlation_id, classification, outcome,
			       failed_phase, error_code, retryable, duration_ms, created_at, completed_at, expires_at,
			       retained_by_legal_hold
			FROM remember_invocation_diagnostics `+where+`
			ORDER BY created_at DESC, invocation_id DESC LIMIT ? OFFSET ?`,
			filter.TeamID, filter.TeamID, filter.OwnerProfileID, filter.OwnerProfileID, filter.Outcome, filter.Outcome,
			filter.Limit, filter.Offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, err := scanRememberInvocationDiagnostic(rows, false)
			if err != nil {
				return err
			}
			page.Records = append(page.Records, value)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list remember invocation diagnostics: %w", err)
	}
	return page, nil
}

func (r *Store) GetRememberInvocationDiagnostic(ctx context.Context, teamID, invocationID string) (*knowledgecontract.RememberInvocationDiagnosticRecord, error) {
	teamID, invocationID = strings.TrimSpace(teamID), strings.TrimSpace(invocationID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(invocationID); err != nil {
		return nil, fmt.Errorf("invocation_id is required: %w", err)
	}
	var result *knowledgecontract.RememberInvocationDiagnosticRecord
	err := r.withSystemReadOnlyRepeatableTx(ctx, func(tx *gorm.DB) error {
		row := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, owner_profile_id::text, invocation_id::text,
			       COALESCE(canonical_attempt_id::text, ''), COALESCE(space_id::text, ''),
			       space_generation, request_hash, correlation_id, classification, outcome,
			       failed_phase, error_code, retryable, duration_ms, request_bytes, request_capture_state, request_capture_reason,
			       provider_exchanges, response_bytes, response_capture_state, response_capture_reason, created_at, completed_at, expires_at, retained_by_legal_hold
			FROM remember_invocation_diagnostics
			WHERE team_id = ?::uuid AND invocation_id = ?::uuid`, teamID, invocationID).Row()
		value, err := scanRememberInvocationDiagnostic(row, true)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRememberInvocationDiagnosticNotFound
		}
		if err != nil {
			return err
		}
		result = &value
		hideExpiredRememberInvocationDiagnostic(result)
		return nil
	})
	if errors.Is(err, ErrRememberInvocationDiagnosticNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("get remember invocation diagnostic: %w", err)
	}
	return result, nil
}

type rememberInvocationScanner interface {
	Scan(...any) error
}

func scanRememberInvocationDiagnostic(scanner rememberInvocationScanner, includeBodies bool) (knowledgecontract.RememberInvocationDiagnosticRecord, error) {
	var value knowledgecontract.RememberInvocationDiagnosticRecord
	var canonicalAttemptID, spaceID string
	var requestBody, responseBody []byte
	var exchangesJSON []byte
	var completedAt *time.Time
	var durationMS int64
	var requestCaptureState, requestCaptureReason, responseCaptureState, responseCaptureReason string
	args := []any{
		&value.TeamID, &value.OwnerProfileID, &value.InvocationID, &canonicalAttemptID, &spaceID,
		&value.SpaceGeneration, &value.RequestHash, &value.CorrelationID, &value.Classification, &value.Outcome,
		&value.FailedPhase, &value.ErrorCode, &value.Retryable, &durationMS,
	}
	if includeBodies {
		args = append(args, &requestBody, &requestCaptureState, &requestCaptureReason, &exchangesJSON, &responseBody, &responseCaptureState, &responseCaptureReason)
	}
	args = append(args, &value.CreatedAt, &completedAt, &value.ExpiresAt, &value.RetainedByLegalHold)
	if err := scanner.Scan(args...); err != nil {
		return value, err
	}
	value.CanonicalAttemptID, value.SpaceID = canonicalAttemptID, spaceID
	value.Duration = time.Duration(durationMS) * time.Millisecond
	value.RequestBody, value.CallerResponse = requestBody, responseBody
	value.RequestCaptureState, value.RequestCaptureReason = requestCaptureState, requestCaptureReason
	value.CallerResponseCaptureState, value.CallerResponseCaptureReason = responseCaptureState, responseCaptureReason
	if completedAt != nil {
		value.CompletedAt = *completedAt
	}
	if len(exchangesJSON) > 0 {
		var exchanges []rememberInvocationExchange
		if err := json.Unmarshal(exchangesJSON, &exchanges); err != nil {
			return value, err
		}
		value.ProviderExchanges = make([]knowledgecontract.RememberAttemptDiagnosticInput, 0, len(exchanges))
		for _, item := range exchanges {
			value.ProviderExchanges = append(value.ProviderExchanges, knowledgecontract.RememberAttemptDiagnosticInput{
				DiagnosticID: item.DiagnosticID, SequenceNo: item.SequenceNo, Kind: item.Kind, Component: item.Component,
				Model: item.Model, RequestBody: []byte(item.RequestBody), ResponseBody: []byte(item.ResponseBody),
				RequestContentType: item.RequestContentType, ResponseContentType: item.ResponseContentType,
				StatusCode: item.StatusCode, Outcome: item.Outcome, CaptureState: item.CaptureState,
				CaptureReason: item.CaptureReason,
				CapturedAt:    item.CapturedAt, ExpiresAt: item.ExpiresAt,
			})
		}
	}
	return value, nil
}

func hideExpiredRememberInvocationDiagnostic(value *knowledgecontract.RememberInvocationDiagnosticRecord) {
	if value == nil || value.RetainedByLegalHold || value.ExpiresAt.IsZero() || time.Now().UTC().Before(value.ExpiresAt) {
		return
	}
	value.RequestBody = nil
	value.RequestCaptureState = "expired"
	value.RequestCaptureReason = ""
	value.CallerResponse = nil
	value.CallerResponseCaptureState = "expired"
	value.CallerResponseCaptureReason = ""
	for index := range value.ProviderExchanges {
		value.ProviderExchanges[index].RequestBody = nil
		value.ProviderExchanges[index].ResponseBody = nil
		value.ProviderExchanges[index].CaptureState = "expired"
		value.ProviderExchanges[index].CaptureReason = ""
	}
}

func (r *Store) purgeExpiredRememberInvocationDiagnostics(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 || batchSize > 100 {
		batchSize = 100
	}
	type purgeCandidate struct {
		teamID, invocationID string
	}
	var deleted int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.remember_attempt_diagnostic_purge', 'true', true)").Error; err != nil {
			return err
		}
		candidates := make([]purgeCandidate, 0, batchSize)
		appendGlobalCandidates := func(limit int) error {
			globalRows, err := tx.WithContext(ctx).Raw(`
				SELECT diagnostic.team_id::text, diagnostic.invocation_id::text
				FROM remember_invocation_diagnostics AS diagnostic
				WHERE diagnostic.space_id IS NULL
				  AND diagnostic.expires_at <= clock_timestamp()
				ORDER BY diagnostic.expires_at ASC, diagnostic.team_id ASC, diagnostic.invocation_id ASC
				LIMIT ?
			`, limit).Rows()
			if err != nil {
				return err
			}
			for globalRows.Next() {
				var candidate purgeCandidate
				if err := globalRows.Scan(&candidate.teamID, &candidate.invocationID); err != nil {
					_ = globalRows.Close()
					return err
				}
				candidates = append(candidates, candidate)
			}
			if err := globalRows.Err(); err != nil {
				_ = globalRows.Close()
				return err
			}
			if err := globalRows.Close(); err != nil {
				return err
			}
			return nil
		}
		if err := appendGlobalCandidates(batchSize); err != nil {
			return err
		}
		if remaining := batchSize - len(candidates); remaining > 0 {
			privateRows, err := tx.WithContext(ctx).Raw(`
				SELECT diagnostic.team_id::text, diagnostic.invocation_id::text
				FROM remember_invocation_diagnostics AS diagnostic
				JOIN memory_spaces AS space
				  ON space.team_id = diagnostic.team_id AND space.id = diagnostic.space_id
				WHERE diagnostic.space_id IS NOT NULL
				  AND diagnostic.expires_at <= clock_timestamp()
				  AND NOT EXISTS (
					SELECT 1 FROM private_memory_legal_holds AS hold
					WHERE hold.space_id = diagnostic.space_id
					  AND hold.released_at IS NULL
				  )
				ORDER BY diagnostic.expires_at ASC, diagnostic.team_id ASC, diagnostic.invocation_id ASC
				LIMIT ?
				FOR KEY SHARE OF space
			`, remaining).Rows()
			if err != nil {
				return err
			}
			for privateRows.Next() {
				var candidate purgeCandidate
				if err := privateRows.Scan(&candidate.teamID, &candidate.invocationID); err != nil {
					_ = privateRows.Close()
					return err
				}
				candidates = append(candidates, candidate)
			}
			if err := privateRows.Err(); err != nil {
				_ = privateRows.Close()
				return err
			}
			if err := privateRows.Close(); err != nil {
				return err
			}
		}
		for _, candidate := range candidates {
			result := tx.WithContext(ctx).Exec(`
				DELETE FROM remember_invocation_diagnostics AS diagnostic
				WHERE diagnostic.team_id = ?::uuid
				  AND diagnostic.invocation_id = ?::uuid
				  AND diagnostic.expires_at <= clock_timestamp()
				  AND (diagnostic.space_id IS NULL OR NOT EXISTS (
					SELECT 1 FROM private_memory_legal_holds AS hold
					WHERE hold.space_id = diagnostic.space_id
					  AND hold.released_at IS NULL
				  ))
			`, candidate.teamID, candidate.invocationID)
			if result.Error != nil {
				return result.Error
			}
			deleted += result.RowsAffected
		}
		return nil
	})
	return int(deleted), err
}

func drainExpiredRememberInvocationDiagnostics(ctx context.Context, r *Store) (int, error) {
	deletedTotal := 0
	for {
		deleted, err := r.purgeExpiredRememberInvocationDiagnostics(ctx, 100)
		deletedTotal += deleted
		if err != nil || deleted < 100 {
			return deletedTotal, err
		}
		if err := ctx.Err(); err != nil {
			return deletedTotal, err
		}
	}
}
