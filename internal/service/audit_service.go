package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/requestctx"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// AuditLogEntry and AuditService remain source-compatible aliases while the
// access capability owns the consumer-facing audit port.
type AuditLogEntry = accessservice.AuditLogEntry
type AuditService = accessservice.AuditService

// AuditServiceImpl implements the AuditService interface.
type AuditServiceImpl struct {
	db     *gorm.DB
	logger *slog.Logger
	rls    postgres.RLSHelper
}

// Ensure AuditServiceImpl implements AuditService
var _ AuditService = (*AuditServiceImpl)(nil)

// NewAuditService creates a new audit service instance.
func NewAuditService(db *gorm.DB) *AuditServiceImpl {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	return &AuditServiceImpl{db: db, logger: logger, rls: postgres.NewRLS()}
}

// NewAuditServiceWithLogger creates a new audit service instance with a custom logger.
func NewAuditServiceWithLogger(db *gorm.DB, logger *slog.Logger) *AuditServiceImpl {
	return &AuditServiceImpl{db: db, logger: logger, rls: postgres.NewRLS()}
}

// sensitiveFields contains exact normalized field names redacted from audit data.
var sensitiveFields = map[string]struct{}{
	"access_token":     {},
	"accesstoken":      {},
	"ai_api_key":       {},
	"api_key":          {},
	"apikey":           {},
	"authorization":    {},
	"encrypted_secret": {},
	"embedding":        {},
	"embeddings":       {},
	"key_hash":         {},
	"password":         {},
	"raw_key":          {},
	"refresh_token":    {},
	"refreshtoken":     {},
	"secret":           {},
	"token":            {},
}

// redactPayload removes sensitive fields from audit maps.
// It returns a new map and does not modify the original.
func redactPayload(payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		return nil
	}

	redacted := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		if isSensitiveAuditField(key) {
			continue
		}
		redacted[key] = redactAuditValue(value)
	}
	return redacted
}

func isSensitiveAuditField(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	if _, ok := sensitiveFields[normalized]; ok {
		return true
	}
	compact := strings.ReplaceAll(normalized, "_", "")
	if _, ok := sensitiveFields[compact]; ok {
		return true
	}
	return strings.HasSuffix(normalized, "_api_key") ||
		strings.HasSuffix(normalized, "_password") ||
		strings.HasSuffix(normalized, "_secret") ||
		strings.HasSuffix(normalized, "_token") ||
		strings.HasSuffix(compact, "apikey") ||
		strings.HasSuffix(compact, "password") ||
		strings.HasSuffix(compact, "secret") ||
		strings.HasSuffix(compact, "token")
}

func redactAuditValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return redactPayload(typed)
	case []interface{}:
		redacted := make([]interface{}, len(typed))
		for i, child := range typed {
			redacted[i] = redactAuditValue(child)
		}
		return redacted
	}

	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return value
	}
	if reflected.Kind() == reflect.Map && reflected.Type().Key().Kind() == reflect.String {
		fields := make(map[string]interface{}, reflected.Len())
		iter := reflected.MapRange()
		for iter.Next() {
			fields[iter.Key().String()] = iter.Value().Interface()
		}
		return redactPayload(fields)
	}
	if reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Array {
		if reflected.Type().Elem().Kind() == reflect.Uint8 {
			return value
		}
		redacted := make([]interface{}, reflected.Len())
		for i := 0; i < reflected.Len(); i++ {
			redacted[i] = redactAuditValue(reflected.Index(i).Interface())
		}
		return redacted
	}
	return value
}

func auditClientIPValue(ctx context.Context, entry AuditLogEntry) any {
	clientIP := strings.TrimSpace(entry.ClientIP)
	if clientIP == "" {
		clientIP = requestctx.ClientIPFromContext(ctx)
	}
	if clientIP == "" {
		return nil
	}
	return clientIP
}

// Append writes an audit log entry to the database.
// Payloads are automatically redacted to remove sensitive fields.
func (s *AuditServiceImpl) Append(ctx context.Context, entry AuditLogEntry) error {
	// Redact sensitive fields from payloads
	beforePayload := redactPayload(entry.BeforePayload)
	afterPayload := redactPayload(entry.AfterPayload)

	// Convert payloads to JSON
	var beforeJSON, afterJSON interface{}
	if beforePayload != nil {
		data, err := json.Marshal(beforePayload)
		if err != nil {
			return fmt.Errorf("failed to marshal before_payload: %w", err)
		}
		beforeJSON = data
	}
	if afterPayload != nil {
		data, err := json.Marshal(afterPayload)
		if err != nil {
			return fmt.Errorf("failed to marshal after_payload: %w", err)
		}
		afterJSON = data
	}

	// Convert metadata to JSON
	metadataJSON := []byte("{}")
	if entry.Metadata != nil {
		data, err := json.Marshal(redactPayload(entry.Metadata))
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}
		metadataJSON = data
	}

	// Use the current timestamp if not provided
	timestamp := entry.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}

	// Generate UUID in Go if not provided (not in SQL via COALESCE)
	id := entry.ID
	if id == "" {
		id = uuid.New().String()
	}
	clientIP := auditClientIPValue(ctx, entry)

	insertFn := func(tx *gorm.DB) error {
		memorySpaceID := entry.MemorySpaceID
		if memorySpaceID == nil && entry.EntityType == "api_key" && entry.ProfileID != nil {
			teamID, teamParseErr := uuid.Parse(strings.TrimSpace(*entry.ProfileID))
			if credentialID, credentialParseErr := uuid.Parse(strings.TrimSpace(entry.EntityID)); teamParseErr == nil && credentialParseErr == nil {
				var candidate sql.NullString
				lookupErr := tx.WithContext(ctx).Raw(`
					SELECT memory_space_id::text
					FROM credentials
					WHERE id = $1 AND team_id = $2
				`, credentialID, teamID).Row().Scan(&candidate)
				if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
					return lookupErr
				}
				if candidate.Valid && strings.TrimSpace(candidate.String) != "" {
					memorySpaceID = &candidate.String
				}
			}
		}
		return tx.Exec(`
			INSERT INTO audit_log (
				id, team_id, timestamp, operation, entity_type, entity_id,
				before_payload, after_payload, actor_profile_id, actor_role,
				client_ip, correlation_id, metadata, memory_space_id
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9, $10, $11, $12, $13, $14
			)
		`,
			id,
			entry.ProfileID,
			timestamp,
			entry.Operation,
			entry.EntityType,
			entry.EntityID,
			beforeJSON,
			afterJSON,
			entry.ActorKeyID,
			entry.ActorRole,
			clientIP,
			entry.CorrelationID,
			metadataJSON,
			memorySpaceID,
		).Error
	}

	var err error
	if s.rls != nil {
		err = s.rls.WithSystemTx(ctx, s.db, insertFn)
	} else {
		err = insertFn(s.db.WithContext(ctx))
	}
	if err != nil {
		return fmt.Errorf("failed to append audit log entry: %w", err)
	}

	return nil
}

// List retrieves audit log entries for a specific team with pagination.
// Entries are returned in descending order by timestamp (most recent first).
func (s *AuditServiceImpl) List(ctx context.Context, teamID string, limit, offset int) ([]AuditLogEntry, int, error) {
	var entries []AuditLogEntry
	queryFn := func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, team_id, timestamp, operation, entity_type, entity_id,
			       before_payload, after_payload, actor_profile_id, actor_role,
			       client_ip, correlation_id, metadata, memory_space_id::text
			FROM audit_log AS audit
			WHERE audit.team_id = $1
			  AND (
				  audit.memory_space_id IS NULL
				  OR EXISTS (
					  SELECT 1
					  FROM memory_spaces AS space
					  WHERE space.id = audit.memory_space_id
					    AND space.team_id = audit.team_id
					    AND space.lifecycle_state = 'active'
				  )
			  )
			ORDER BY timestamp DESC
			LIMIT $2 OFFSET $3
		`, teamID, limit, offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var entry AuditLogEntry
			var beforePayloadJSON, afterPayloadJSON, metadataJSON []byte
			var profileIDNullable sql.NullString
			var actorKeyIDNullable sql.NullString
			var clientIPNullable sql.NullString
			var memorySpaceIDNullable sql.NullString

			err := rows.Scan(
				&entry.ID,
				&profileIDNullable,
				&entry.Timestamp,
				&entry.Operation,
				&entry.EntityType,
				&entry.EntityID,
				&beforePayloadJSON,
				&afterPayloadJSON,
				&actorKeyIDNullable,
				&entry.ActorRole,
				&clientIPNullable,
				&entry.CorrelationID,
				&metadataJSON,
				&memorySpaceIDNullable,
			)
			if err != nil {
				return err
			}

			// Handle nullable fields
			if profileIDNullable.Valid {
				entry.ProfileID = &profileIDNullable.String
			}
			if actorKeyIDNullable.Valid {
				entry.ActorKeyID = &actorKeyIDNullable.String
			}
			if clientIPNullable.Valid {
				entry.ClientIP = clientIPNullable.String
			}
			if memorySpaceIDNullable.Valid {
				entry.MemorySpaceID = &memorySpaceIDNullable.String
			}

			// Parse JSON payloads
			if len(beforePayloadJSON) > 0 {
				if err := json.Unmarshal(beforePayloadJSON, &entry.BeforePayload); err != nil {
					entry.BeforePayload = nil
				}
			}
			if len(afterPayloadJSON) > 0 {
				if err := json.Unmarshal(afterPayloadJSON, &entry.AfterPayload); err != nil {
					entry.AfterPayload = nil
				}
			}
			if len(metadataJSON) > 0 {
				if err := json.Unmarshal(metadataJSON, &entry.Metadata); err != nil {
					entry.Metadata = nil
				}
			}

			entries = append(entries, entry)
		}

		return rows.Err()
	}

	var total int
	if s.rls != nil {
		if err := s.rls.WithTeamTx(ctx, s.db, teamID, queryFn); err != nil {
			return nil, 0, fmt.Errorf("failed to query audit log: %w", err)
		}
		if err := s.rls.WithTeamTx(ctx, s.db, teamID, func(tx *gorm.DB) error {
			return tx.Raw(`
				SELECT COUNT(*)
				FROM audit_log AS audit
				WHERE audit.team_id = $1
				  AND (
					  audit.memory_space_id IS NULL
					  OR EXISTS (
						  SELECT 1
						  FROM memory_spaces AS space
						  WHERE space.id = audit.memory_space_id
						    AND space.team_id = audit.team_id
						    AND space.lifecycle_state = 'active'
					  )
				  )
			`, teamID).Scan(&total).Error
		}); err != nil {
			return nil, 0, fmt.Errorf("failed to count audit log entries: %w", err)
		}
	} else {
		if err := queryFn(s.db.WithContext(ctx)); err != nil {
			return nil, 0, fmt.Errorf("failed to query audit log: %w", err)
		}
		if err := s.db.WithContext(ctx).Raw(`
			SELECT COUNT(*)
			FROM audit_log AS audit
			WHERE audit.team_id = $1
			  AND (
				  audit.memory_space_id IS NULL
				  OR EXISTS (
					  SELECT 1
					  FROM memory_spaces AS space
					  WHERE space.id = audit.memory_space_id
					    AND space.team_id = audit.team_id
					    AND space.lifecycle_state = 'active'
				  )
			  )
		`, teamID).Scan(&total).Error; err != nil {
			return nil, 0, fmt.Errorf("failed to count audit log entries: %w", err)
		}
	}

	return entries, total, nil
}

// TeamCreated logs a team creation event using retained audit schema values.
func (s *AuditServiceImpl) TeamCreated(ctx context.Context, teamID string, afterPayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	entry := AuditLogEntry{
		ProfileID:     &teamID,
		Operation:     "CREATE",
		EntityType:    "profile",
		EntityID:      teamID,
		AfterPayload:  afterPayload,
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}
	return s.Append(ctx, entry)
}

// TeamUpdated logs a team update event using retained audit schema values.
func (s *AuditServiceImpl) TeamUpdated(ctx context.Context, teamID string, beforePayload, afterPayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	entry := AuditLogEntry{
		ProfileID:     &teamID,
		Operation:     "UPDATE",
		EntityType:    "profile",
		EntityID:      teamID,
		BeforePayload: beforePayload,
		AfterPayload:  afterPayload,
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}
	return s.Append(ctx, entry)
}

// TeamDeleteBlocked logs when a team deletion was blocked.
func (s *AuditServiceImpl) TeamDeleteBlocked(ctx context.Context, teamID string, beforePayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string, reason string) error {
	entry := AuditLogEntry{
		ProfileID:     &teamID,
		Operation:     "DELETE_BLOCKED",
		EntityType:    "profile",
		EntityID:      teamID,
		BeforePayload: beforePayload,
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
		Metadata: map[string]interface{}{
			"reason": reason,
		},
	}
	return s.Append(ctx, entry)
}

// TeamDeleted logs a team deletion event using retained audit schema values.
func (s *AuditServiceImpl) TeamDeleted(ctx context.Context, teamID string, beforePayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	entry := AuditLogEntry{
		ProfileID:     &teamID,
		Operation:     "DELETE",
		EntityType:    "profile",
		EntityID:      teamID,
		BeforePayload: beforePayload,
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}
	return s.Append(ctx, entry)
}

// CredentialCreated logs credential creation using retained audit schema values.
func (s *AuditServiceImpl) CredentialCreated(ctx context.Context, teamID *string, credentialID string, afterPayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	entry := AuditLogEntry{
		ProfileID:     teamID,
		Operation:     "CREATE",
		EntityType:    "api_key",
		EntityID:      credentialID,
		AfterPayload:  afterPayload,
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}
	return s.Append(ctx, entry)
}

// CredentialRevoked logs credential revocation using retained audit schema values.
func (s *AuditServiceImpl) CredentialRevoked(ctx context.Context, teamID *string, credentialID string, beforePayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	entry := AuditLogEntry{
		ProfileID:     teamID,
		Operation:     "REVOKE",
		EntityType:    "api_key",
		EntityID:      credentialID,
		BeforePayload: beforePayload,
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}
	return s.Append(ctx, entry)
}

// AuthFailure logs an authentication failure event.
func (s *AuditServiceImpl) AuthFailure(ctx context.Context, profileID *string, entityType, entityID string, metadata map[string]interface{}, clientIP, correlationID string) error {
	entry := AuditLogEntry{
		ProfileID:     profileID,
		Operation:     "AUTH_FAILURE",
		EntityType:    entityType,
		EntityID:      entityID,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
		Metadata:      metadata,
	}
	return s.Append(ctx, entry)
}

// CrossTeamDenied logs a cross-team access denial using retained audit values.
func (s *AuditServiceImpl) CrossTeamDenied(ctx context.Context, actorTeamID, targetTeamID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["actor_profile_id"] = actorTeamID
	metadata["target_team_id"] = targetTeamID
	metadata["denied_operation"] = operation

	entry := AuditLogEntry{
		ProfileID:     &targetTeamID,
		Operation:     "CROSS_PROFILE_DENIED",
		EntityType:    "profile",
		EntityID:      targetTeamID,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
		Metadata:      metadata,
	}
	return s.Append(ctx, entry)
}

// RateLimited logs a rate limiting event.
func (s *AuditServiceImpl) RateLimited(ctx context.Context, profileID *string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["limited_operation"] = operation

	entry := AuditLogEntry{
		ProfileID:     profileID,
		Operation:     "RATE_LIMITED",
		EntityType:    "request",
		EntityID:      correlationID,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
		Metadata:      metadata,
	}
	return s.Append(ctx, entry)
}

// SystemQuery logs a system-scoped query event.
func (s *AuditServiceImpl) SystemQuery(ctx context.Context, queryType string, metadata map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["query_type"] = queryType

	entry := AuditLogEntry{
		Operation:     "SYSTEM_QUERY",
		EntityType:    "system",
		EntityID:      queryType,
		ActorKeyID:    actorKeyID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
		Metadata:      metadata,
	}
	return s.Append(ctx, entry)
}

// InvariantViolation logs an invariant violation event.
func (s *AuditServiceImpl) InvariantViolation(ctx context.Context, entityType, entityID string, violation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["violation_description"] = violation

	entry := AuditLogEntry{
		Operation:     "INVARIANT_VIOLATION",
		EntityType:    entityType,
		EntityID:      entityID,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
		Metadata:      metadata,
	}
	return s.Append(ctx, entry)
}
