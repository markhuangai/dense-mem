// Package audit owns audit event construction, redaction, and read policy.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/audit/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

// Service owns the consumer-facing audit policy while Store owns persistence.
type Service struct {
	store contract.Store
}

var _ accessservice.AuditService = (*Service)(nil)

// New constructs the audit application service around its persistence port.
func New(store contract.Store) *Service {
	return &Service{store: store}
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

// RedactPayload removes sensitive fields from audit maps and returns a new map.
func RedactPayload(payload map[string]interface{}) map[string]interface{} {
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
		return RedactPayload(typed)
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
		return RedactPayload(fields)
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

// AuditClientIPValue selects an explicit entry address, then the authenticated
// request context, and finally SQL NULL when neither is available.
func AuditClientIPValue(ctx context.Context, entry accessservice.AuditLogEntry) any {
	clientIP := strings.TrimSpace(entry.ClientIP)
	if clientIP == "" {
		clientIP = requestctx.ClientIPFromContext(ctx)
	}
	if clientIP == "" {
		return nil
	}
	return clientIP
}

// Append validates and normalizes an event before handing it to persistence.
func (s *Service) Append(ctx context.Context, entry accessservice.AuditLogEntry) error {
	beforeJSON, err := marshalPayload("before_payload", entry.BeforePayload)
	if err != nil {
		return err
	}
	afterJSON, err := marshalPayload("after_payload", entry.AfterPayload)
	if err != nil {
		return err
	}
	metadataJSON, err := marshalMetadata(entry.Metadata)
	if err != nil {
		return err
	}

	timestamp := entry.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	id := entry.ID
	if id == "" {
		id = uuid.New().String()
	}
	lookup := credentialMemorySpaceLookup(entry)
	if s == nil || s.store == nil {
		return errors.New("audit: store is required")
	}

	err = s.store.Append(ctx, contract.Entry{
		ID:                          id,
		ProfileID:                   entry.ProfileID,
		MemorySpaceID:               entry.MemorySpaceID,
		Timestamp:                   timestamp,
		Operation:                   entry.Operation,
		EntityType:                  entry.EntityType,
		EntityID:                    entry.EntityID,
		BeforePayload:               beforeJSON,
		AfterPayload:                afterJSON,
		ActorKeyID:                  entry.ActorKeyID,
		ActorRole:                   entry.ActorRole,
		ClientIP:                    AuditClientIPValue(ctx, entry),
		CorrelationID:               entry.CorrelationID,
		Metadata:                    metadataJSON,
		CredentialMemorySpaceLookup: lookup,
	})
	if err != nil {
		return fmt.Errorf("failed to append audit log entry: %w", err)
	}
	return nil
}

func credentialMemorySpaceLookup(entry accessservice.AuditLogEntry) *contract.CredentialMemorySpaceLookup {
	if entry.MemorySpaceID != nil || entry.EntityType != "api_key" || entry.ProfileID == nil {
		return nil
	}
	teamID, teamErr := uuid.Parse(strings.TrimSpace(*entry.ProfileID))
	credentialID, credentialErr := uuid.Parse(strings.TrimSpace(entry.EntityID))
	if teamErr != nil || credentialErr != nil {
		return nil
	}
	return &contract.CredentialMemorySpaceLookup{TeamID: teamID, CredentialID: credentialID}
}

func marshalPayload(field string, payload map[string]interface{}) ([]byte, error) {
	if payload == nil {
		return nil, nil
	}
	data, err := json.Marshal(RedactPayload(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s: %w", field, err)
	}
	return data, nil
}

func marshalMetadata(metadata map[string]interface{}) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
	}
	data, err := json.Marshal(RedactPayload(metadata))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}
	return data, nil
}

// List reads team-scoped entries through the persistence port and converts them
// to the existing producer-facing Access contract.
func (s *Service) List(ctx context.Context, teamID string, limit, offset int) ([]accessservice.AuditLogEntry, int, error) {
	if s == nil || s.store == nil {
		return nil, 0, errors.New("failed to query audit log: audit: store is required")
	}
	entries, err := s.store.List(ctx, teamID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query audit log: %w", err)
	}
	total, err := s.store.Count(ctx, teamID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count audit log entries: %w", err)
	}
	result := make([]accessservice.AuditLogEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, accessservice.AuditLogEntry{
			ID:            entry.ID,
			ProfileID:     entry.ProfileID,
			MemorySpaceID: entry.MemorySpaceID,
			Timestamp:     entry.Timestamp,
			Operation:     entry.Operation,
			EntityType:    entry.EntityType,
			EntityID:      entry.EntityID,
			BeforePayload: decodePayload(entry.BeforePayload),
			AfterPayload:  decodePayload(entry.AfterPayload),
			ActorKeyID:    entry.ActorKeyID,
			ActorRole:     entry.ActorRole,
			ClientIP:      clientIPString(entry.ClientIP),
			CorrelationID: entry.CorrelationID,
			Metadata:      decodePayload(entry.Metadata),
		})
	}
	return result, total, nil
}

func decodePayload(raw []byte) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return payload
}

func clientIPString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func (s *Service) appendEvent(ctx context.Context, entry accessservice.AuditLogEntry) error {
	return s.Append(ctx, entry)
}

// TeamCreated logs a team creation event.
func (s *Service) TeamCreated(ctx context.Context, teamID string, afterPayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	return s.appendEvent(ctx, accessservice.AuditLogEntry{ProfileID: &teamID, Operation: "CREATE", EntityType: "profile", EntityID: teamID, AfterPayload: afterPayload, ActorKeyID: actorCredentialID, ActorRole: actorRole, ClientIP: clientIP, CorrelationID: correlationID})
}

// TeamUpdated logs a team update event.
func (s *Service) TeamUpdated(ctx context.Context, teamID string, beforePayload, afterPayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	return s.appendEvent(ctx, accessservice.AuditLogEntry{ProfileID: &teamID, Operation: "UPDATE", EntityType: "profile", EntityID: teamID, BeforePayload: beforePayload, AfterPayload: afterPayload, ActorKeyID: actorCredentialID, ActorRole: actorRole, ClientIP: clientIP, CorrelationID: correlationID})
}

// TeamDeleteBlocked logs when a team deletion was blocked.
func (s *Service) TeamDeleteBlocked(ctx context.Context, teamID string, beforePayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string, reason string) error {
	return s.appendEvent(ctx, accessservice.AuditLogEntry{ProfileID: &teamID, Operation: "DELETE_BLOCKED", EntityType: "profile", EntityID: teamID, BeforePayload: beforePayload, ActorKeyID: actorCredentialID, ActorRole: actorRole, ClientIP: clientIP, CorrelationID: correlationID, Metadata: map[string]interface{}{"reason": reason}})
}

// TeamDeleted logs a team deletion event.
func (s *Service) TeamDeleted(ctx context.Context, teamID string, beforePayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	return s.appendEvent(ctx, accessservice.AuditLogEntry{ProfileID: &teamID, Operation: "DELETE", EntityType: "profile", EntityID: teamID, BeforePayload: beforePayload, ActorKeyID: actorCredentialID, ActorRole: actorRole, ClientIP: clientIP, CorrelationID: correlationID})
}

// CredentialCreated logs credential creation.
func (s *Service) CredentialCreated(ctx context.Context, teamID *string, credentialID string, afterPayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	return s.appendEvent(ctx, accessservice.AuditLogEntry{ProfileID: teamID, Operation: "CREATE", EntityType: "api_key", EntityID: credentialID, AfterPayload: afterPayload, ActorKeyID: actorCredentialID, ActorRole: actorRole, ClientIP: clientIP, CorrelationID: correlationID})
}

// CredentialRevoked logs credential revocation.
func (s *Service) CredentialRevoked(ctx context.Context, teamID *string, credentialID string, beforePayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	return s.appendEvent(ctx, accessservice.AuditLogEntry{ProfileID: teamID, Operation: "REVOKE", EntityType: "api_key", EntityID: credentialID, BeforePayload: beforePayload, ActorKeyID: actorCredentialID, ActorRole: actorRole, ClientIP: clientIP, CorrelationID: correlationID})
}

// AuthFailure logs an authentication failure event.
func (s *Service) AuthFailure(ctx context.Context, profileID *string, entityType, entityID string, metadata map[string]interface{}, clientIP, correlationID string) error {
	return s.appendEvent(ctx, accessservice.AuditLogEntry{ProfileID: profileID, Operation: "AUTH_FAILURE", EntityType: entityType, EntityID: entityID, ClientIP: clientIP, CorrelationID: correlationID, Metadata: metadata})
}

// CrossTeamDenied logs a cross-team access denial.
func (s *Service) CrossTeamDenied(ctx context.Context, actorTeamID, targetTeamID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["actor_profile_id"] = actorTeamID
	metadata["target_team_id"] = targetTeamID
	metadata["denied_operation"] = operation
	return s.appendEvent(ctx, accessservice.AuditLogEntry{ProfileID: &targetTeamID, Operation: "CROSS_PROFILE_DENIED", EntityType: "profile", EntityID: targetTeamID, ClientIP: clientIP, CorrelationID: correlationID, Metadata: metadata})
}

// RateLimited logs a rate-limiting event.
func (s *Service) RateLimited(ctx context.Context, profileID *string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["limited_operation"] = operation
	return s.appendEvent(ctx, accessservice.AuditLogEntry{ProfileID: profileID, Operation: "RATE_LIMITED", EntityType: "request", EntityID: correlationID, ClientIP: clientIP, CorrelationID: correlationID, Metadata: metadata})
}

// SystemQuery logs a system-scoped query event.
func (s *Service) SystemQuery(ctx context.Context, queryType string, metadata map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["query_type"] = queryType
	return s.appendEvent(ctx, accessservice.AuditLogEntry{Operation: "SYSTEM_QUERY", EntityType: "system", EntityID: queryType, ActorKeyID: actorKeyID, ActorRole: actorRole, ClientIP: clientIP, CorrelationID: correlationID, Metadata: metadata})
}

// InvariantViolation logs an invariant violation event.
func (s *Service) InvariantViolation(ctx context.Context, entityType, entityID string, violation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["violation_description"] = violation
	return s.appendEvent(ctx, accessservice.AuditLogEntry{Operation: "INVARIANT_VIOLATION", EntityType: entityType, EntityID: entityID, ClientIP: clientIP, CorrelationID: correlationID, Metadata: metadata})
}
