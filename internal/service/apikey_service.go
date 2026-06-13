package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
)

// CreateAPIKeyRequest represents a request to create a standard API key.
// Every standard key belongs to one team profile. Scopes are limited to the two
// supported permission sets: read-only or read/write.
type CreateAPIKeyRequest struct {
	Name               string     `json:"name"`
	RateLimit          int        `json:"rate_limit"`
	ExpiresAt          *time.Time `json:"expires_at"`
	Scopes             []string   `json:"scopes"`
	Role               string     `json:"role"`
	SSOOwnerIdentityID *uuid.UUID `json:"-"`
}

const (
	APIKeyScopeRead   = "read"
	APIKeyScopeWrite  = "write"
	APIKeyRoleManager = "manager"
	APIKeyRoleMember  = "member"
)

var standardAPIKeyScopes = []string{APIKeyScopeRead, APIKeyScopeWrite}

// StandardAPIKeyScopes returns the default read/write scope set.
func StandardAPIKeyScopes() []string {
	return append([]string(nil), standardAPIKeyScopes...)
}

// NormalizeAPIKeyScopes returns the canonical scope set for a key creation
// request. Empty input preserves the historical default: read/write.
func NormalizeAPIKeyScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return StandardAPIKeyScopes(), nil
	}

	var hasRead, hasWrite bool
	for _, raw := range scopes {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case APIKeyScopeRead:
			hasRead = true
		case APIKeyScopeWrite:
			hasWrite = true
		default:
			return nil, httperr.New(httperr.VALIDATION_ERROR, "api key scopes must be either [read] or [read, write]")
		}
	}

	switch {
	case hasRead && hasWrite:
		return StandardAPIKeyScopes(), nil
	case hasRead:
		return []string{APIKeyScopeRead}, nil
	default:
		return nil, httperr.New(httperr.VALIDATION_ERROR, "api key scopes must be either [read] or [read, write]")
	}
}

func NormalizeAPIKeyRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "":
		return "", nil
	case APIKeyRoleManager:
		return APIKeyRoleManager, nil
	case APIKeyRoleMember:
		return APIKeyRoleMember, nil
	default:
		return "", httperr.New(httperr.VALIDATION_ERROR, "api key role must be either manager or member")
	}
}

func normalizeTeamProfileName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", httperr.New(httperr.VALIDATION_ERROR, "profile name is required")
	}
	if len([]rune(trimmed)) > 100 {
		return "", httperr.New(httperr.VALIDATION_ERROR, "profile name must be at most 100 characters")
	}
	return trimmed, nil
}

func teamProfileNameConflict(err error, name string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return httperr.New(httperr.CONFLICT, fmt.Sprintf("profile with name '%s' already exists for this team", name))
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return httperr.New(httperr.CONFLICT, fmt.Sprintf("profile with name '%s' already exists for this team", name))
	}
	return nil
}

func apiKeyCreateConflict(err error, name string) error {
	if uniqueViolationName(err) == "idx_team_profiles_sso_owner_team_active_unique" {
		return httperr.New(httperr.CONFLICT, "sso-owned api key already exists for this team")
	}
	return teamProfileNameConflict(err, name)
}

func uniqueViolationName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return pqErr.Constraint
	}
	return ""
}

// KeySessionInvalidator invalidates key-bound sessions. Redis-backed deployments
// delete matching session keys; no-Redis deployments inject a non-nil no-op.
type KeySessionInvalidator interface {
	InvalidateKeySessions(ctx context.Context, profileID, keyID string) error
}

// ProfileStatePurger removes cache, session, and stream state for a profile.
// Redis-backed deployments delete matching keys; no-Redis deployments inject a
// non-nil no-op.
type ProfileStatePurger interface {
	PurgeProfileState(ctx context.Context, profileID string) error
}

// APIKeyService is the companion interface for API key business logic.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type APIKeyService interface {
	CreateStandardKey(ctx context.Context, profileID uuid.UUID, req CreateAPIKeyRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.APIKey, string, error)
	ListByProfile(ctx context.Context, profileID uuid.UUID, limit, offset int) ([]*domain.APIKey, error)
	CountByProfile(ctx context.Context, profileID uuid.UUID) (int64, error)
	// GetByIDForProfile returns the key only when it belongs to profileID; NOT_FOUND otherwise (no existence oracle).
	GetByIDForProfile(ctx context.Context, profileID, id uuid.UUID) (*domain.APIKey, error)
	GetSSOOwnedKey(ctx context.Context, profileID, identityID uuid.UUID) (*domain.APIKey, error)
	// RevokeForProfile revokes the key only when it belongs to profileID; NOT_FOUND otherwise.
	RevokeForProfile(ctx context.Context, profileID, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error
	// DeleteForProfile hard-deletes the key only when it belongs to profileID; NOT_FOUND otherwise.
	DeleteForProfile(ctx context.Context, profileID, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error
	// UpdateNameForProfile renames the team profile without changing key material.
	UpdateNameForProfile(ctx context.Context, profileID, id uuid.UUID, name string, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.APIKey, error)
	// UpdateRoleForProfile changes the team profile role without changing key material.
	UpdateRoleForProfile(ctx context.Context, profileID, id uuid.UUID, role string, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.APIKey, error)
	// RotateForProfile rotates key material in place for a named team profile.
	RotateForProfile(ctx context.Context, profileID, id uuid.UUID, req CreateAPIKeyRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.APIKey, string, error)
}

// APIKeyServiceImpl implements the APIKeyService interface.
type APIKeyServiceImpl struct {
	repo               repository.APIKeyRepository
	profileService     ProfileService
	auditService       AuditService
	sessionInvalidator KeySessionInvalidator
	statePurger        ProfileStatePurger
	logger             *slog.Logger
}

// Ensure APIKeyServiceImpl implements APIKeyService
var _ APIKeyService = (*APIKeyServiceImpl)(nil)

// NewAPIKeyService creates a new API key service instance.
func NewAPIKeyService(
	repo repository.APIKeyRepository,
	profileService ProfileService,
	auditService AuditService,
	sessionInvalidator KeySessionInvalidator,
	statePurger ProfileStatePurger,
) *APIKeyServiceImpl {
	return &APIKeyServiceImpl{
		repo:               repo,
		profileService:     profileService,
		auditService:       auditService,
		sessionInvalidator: sessionInvalidator,
		statePurger:        statePurger,
		logger:             slog.Default(),
	}
}

// NewAPIKeyServiceWithLogger creates a new API key service instance with a custom logger.
func NewAPIKeyServiceWithLogger(
	repo repository.APIKeyRepository,
	profileService ProfileService,
	auditService AuditService,
	sessionInvalidator KeySessionInvalidator,
	statePurger ProfileStatePurger,
	logger *slog.Logger,
) *APIKeyServiceImpl {
	return &APIKeyServiceImpl{
		repo:               repo,
		profileService:     profileService,
		auditService:       auditService,
		sessionInvalidator: sessionInvalidator,
		statePurger:        statePurger,
		logger:             logger,
	}
}

// logAuditError logs an audit service error with structured logging.
func (s *APIKeyServiceImpl) logAuditError(err error, operation, keyID, correlationID string) {
	if s.logger == nil {
		return
	}
	s.logger.Error("audit_log_write_failed",
		slog.String("error", err.Error()),
		slog.String("operation", operation),
		slog.String("key_id", keyID),
		slog.String("correlation_id", correlationID),
	)
}

// CreateStandardKey creates a new standard API key for a profile.
// Returns the created key and the plaintext raw key (returned exactly once).
func (s *APIKeyServiceImpl) CreateStandardKey(ctx context.Context, profileID uuid.UUID, req CreateAPIKeyRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.APIKey, string, error) {
	// Verify profile exists
	_, err := s.profileService.Get(ctx, profileID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to verify profile: %w", err)
	}

	scopes, err := NormalizeAPIKeyScopes(req.Scopes)
	if err != nil {
		return nil, "", err
	}

	role, err := NormalizeAPIKeyRole(req.Role)
	if err != nil {
		return nil, "", err
	}
	if role == "" {
		count, err := s.repo.CountByProfile(ctx, profileID)
		if err != nil {
			return nil, "", fmt.Errorf("failed to count team profiles: %w", err)
		}
		if count == 0 {
			role = APIKeyRoleManager
		} else {
			role = APIKeyRoleMember
		}
	}
	if role == APIKeyRoleManager {
		scopes = StandardAPIKeyScopes()
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "default profile"
	}

	// Generate raw key
	rawKey, err := crypto.GenerateRawKeyForProfile(name)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate raw key: %w", err)
	}

	// Hash the key
	keyHash, err := crypto.HashKey(rawKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to hash key: %w", err)
	}

	// Create the key record
	key := &domain.APIKey{
		ProfileID:          profileID,
		TeamID:             profileID,
		Label:              name,
		Name:               name,
		KeyHash:            keyHash,
		KeyPrefix:          crypto.GetKeyPrefix(rawKey),
		KeySuffix:          crypto.GetKeySuffix(rawKey),
		Scopes:             scopes,
		Role:               role,
		RateLimit:          req.RateLimit,
		ExpiresAt:          req.ExpiresAt,
		SSOOwnerIdentityID: req.SSOOwnerIdentityID,
	}

	if err := s.repo.CreateStandardKey(ctx, key); err != nil {
		if conflict := apiKeyCreateConflict(err, name); conflict != nil {
			return nil, "", conflict
		}
		return nil, "", fmt.Errorf("failed to create api key: %w", err)
	}

	// Audit the creation (without the raw key or hash)
	afterPayload := map[string]interface{}{
		"id":           key.ID.String(),
		"team_id":      key.ProfileID.String(),
		"profile_name": key.GetProfileName(),
		"scopes":       key.Scopes,
		"rate_limit":   key.RateLimit,
		"role":         key.GetRole(),
	}
	if key.ExpiresAt != nil {
		afterPayload["expires_at"] = key.ExpiresAt.Format(time.RFC3339)
	}

	profileIDStr := profileID.String()
	if err := s.auditService.APIKeyCreated(ctx, &profileIDStr, key.ID.String(), afterPayload, actorKeyID, actorRole, clientIP, correlationID); err != nil {
		// Log the audit failure but don't fail the operation
		s.logAuditError(err, "CREATE", key.ID.String(), correlationID)
	}

	// Return the key (without hash) and the raw key (shown exactly once)
	key.KeyHash = ""
	return key, rawKey, nil
}

// UpdateRoleForProfile changes a team profile's administrative role without
// changing key material or data scopes.
func (s *APIKeyServiceImpl) UpdateRoleForProfile(ctx context.Context, profileID, id uuid.UUID, role string, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.APIKey, error) {
	normalizedRole, err := NormalizeAPIKeyRole(role)
	if err != nil {
		return nil, err
	}
	if normalizedRole == "" {
		return nil, httperr.New(httperr.VALIDATION_ERROR, "api key role is required")
	}

	key, err := s.repo.GetByIDForProfile(ctx, profileID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get team profile: %w", err)
	}
	if key == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team profile with id '%s' not found", id.String()))
	}
	currentRole := key.GetRole()
	if currentRole == normalizedRole {
		key.KeyHash = ""
		key.Role = normalizedRole
		return key, nil
	}

	beforePayload := map[string]interface{}{
		"team_id":    profileID.String(),
		"profile_id": key.ID.String(),
		"role":       currentRole,
	}

	rows, err := s.repo.UpdateRoleForProfile(ctx, profileID, id, normalizedRole)
	if err != nil {
		return nil, fmt.Errorf("failed to update team profile role: %w", err)
	}
	if rows == 0 {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team profile with id '%s' not found", id.String()))
	}

	key.KeyHash = ""
	key.Role = normalizedRole

	profileIDStr := profileID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:     &profileIDStr,
		Operation:     "UPDATE",
		EntityType:    "team_profile",
		EntityID:      key.ID.String(),
		BeforePayload: beforePayload,
		AfterPayload: map[string]interface{}{
			"team_id":    profileID.String(),
			"profile_id": key.ID.String(),
			"role":       normalizedRole,
		},
		ActorKeyID:    actorKeyID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}); err != nil {
		s.logAuditError(err, "UPDATE_ROLE", key.ID.String(), correlationID)
	}

	return key, nil
}

// UpdateNameForProfile renames a team profile without rotating its key.
func (s *APIKeyServiceImpl) UpdateNameForProfile(ctx context.Context, profileID, id uuid.UUID, name string, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.APIKey, error) {
	normalizedName, err := normalizeTeamProfileName(name)
	if err != nil {
		return nil, err
	}

	key, err := s.repo.GetByIDForProfile(ctx, profileID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get team profile: %w", err)
	}
	if key == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team profile with id '%s' not found", id.String()))
	}

	currentName := key.GetProfileName()
	if currentName == normalizedName {
		key.KeyHash = ""
		key.Name = normalizedName
		key.Label = normalizedName
		return key, nil
	}

	beforePayload := map[string]interface{}{
		"team_id":      profileID.String(),
		"profile_id":   key.ID.String(),
		"profile_name": currentName,
	}

	rows, err := s.repo.UpdateNameForProfile(ctx, profileID, id, normalizedName)
	if err != nil {
		if conflict := teamProfileNameConflict(err, normalizedName); conflict != nil {
			return nil, conflict
		}
		return nil, fmt.Errorf("failed to update team profile name: %w", err)
	}
	if rows == 0 {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team profile with id '%s' not found", id.String()))
	}

	key.KeyHash = ""
	key.Name = normalizedName
	key.Label = normalizedName

	profileIDStr := profileID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:     &profileIDStr,
		Operation:     "UPDATE",
		EntityType:    "team_profile",
		EntityID:      key.ID.String(),
		BeforePayload: beforePayload,
		AfterPayload: map[string]interface{}{
			"team_id":      profileID.String(),
			"profile_id":   key.ID.String(),
			"profile_name": normalizedName,
		},
		ActorKeyID:    actorKeyID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}); err != nil {
		s.logAuditError(err, "UPDATE", key.ID.String(), correlationID)
	}

	return key, nil
}

// RotateForProfile rotates a team profile's API key in place.
func (s *APIKeyServiceImpl) RotateForProfile(ctx context.Context, profileID, id uuid.UUID, req CreateAPIKeyRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.APIKey, string, error) {
	key, err := s.repo.GetByIDForProfile(ctx, profileID, id)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get team profile: %w", err)
	}
	if key == nil {
		return nil, "", httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team profile with id '%s' not found", id.String()))
	}

	name := key.GetProfileName()
	rawKey, err := crypto.GenerateRawKeyForProfile(name)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate raw key: %w", err)
	}
	keyHash, err := crypto.HashKey(rawKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to hash key: %w", err)
	}

	rows, err := s.repo.RotateForProfile(ctx, profileID, id, keyHash, crypto.GetKeyPrefix(rawKey), crypto.GetKeySuffix(rawKey), req.ExpiresAt)
	if err != nil {
		return nil, "", fmt.Errorf("failed to rotate team profile key: %w", err)
	}
	if rows == 0 {
		return nil, "", httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team profile with id '%s' not found", id.String()))
	}
	if s.sessionInvalidator != nil {
		if err := s.sessionInvalidator.InvalidateKeySessions(ctx, profileID.String(), id.String()); err != nil {
			s.logger.Warn("session_invalidation_failed", slog.String("error", err.Error()), slog.String("team_id", id.String()))
		}
	}

	key.KeyHash = ""
	key.KeyPrefix = crypto.GetKeyPrefix(rawKey)
	key.KeySuffix = crypto.GetKeySuffix(rawKey)
	key.ExpiresAt = req.ExpiresAt
	key.LastUsedAt = nil
	key.RevokedAt = nil

	profileIDStr := profileID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:  &profileIDStr,
		Operation:  "ROTATE_KEY",
		EntityType: "team_profile",
		EntityID:   key.ID.String(),
		AfterPayload: map[string]interface{}{
			"team_id":      profileID.String(),
			"profile_id":   key.ID.String(),
			"profile_name": key.GetProfileName(),
		},
		ActorKeyID:    actorKeyID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}); err != nil {
		s.logAuditError(err, "ROTATE", key.ID.String(), correlationID)
	}

	return key, rawKey, nil
}

// ListByProfile retrieves API keys for a profile.
// Never returns the key_hash field.
func (s *APIKeyServiceImpl) ListByProfile(ctx context.Context, profileID uuid.UUID, limit, offset int) ([]*domain.APIKey, error) {
	return s.repo.ListByProfile(ctx, profileID, limit, offset)
}

// CountByProfile returns the total number of API keys for a profile (used for pagination totals).
func (s *APIKeyServiceImpl) CountByProfile(ctx context.Context, profileID uuid.UUID) (int64, error) {
	return s.repo.CountByProfile(ctx, profileID)
}

// GetByIDForProfile retrieves an API key by ID scoped to the caller's profile.
// Returns NOT_FOUND when the id/profile combination does not match (prevents existence oracle).
func (s *APIKeyServiceImpl) GetByIDForProfile(ctx context.Context, profileID, id uuid.UUID) (*domain.APIKey, error) {
	key, err := s.repo.GetByIDForProfile(ctx, profileID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get api key for profile: %w", err)
	}
	if key == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("api key with id '%s' not found", id.String()))
	}
	return key, nil
}

func (s *APIKeyServiceImpl) GetSSOOwnedKey(ctx context.Context, profileID, identityID uuid.UUID) (*domain.APIKey, error) {
	key, err := s.repo.GetSSOOwnedKey(ctx, profileID, identityID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sso-owned api key for profile: %w", err)
	}
	return key, nil
}

// RevokeForProfile revokes an API key scoped to the caller's profile.
// Returns NOT_FOUND when the id/profile combination does not match.
// Invalidates active sessions and writes audit.
func (s *APIKeyServiceImpl) RevokeForProfile(ctx context.Context, profileID, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	// Fetch under profile scope for audit before-payload and to verify ownership.
	key, err := s.repo.GetByIDForProfile(ctx, profileID, id)
	if err != nil {
		return fmt.Errorf("failed to get api key for profile: %w", err)
	}
	if key == nil {
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("api key with id '%s' not found", id.String()))
	}
	if key.RevokedAt != nil {
		return httperr.New(httperr.CONFLICT, "api key is already revoked")
	}

	beforePayload := map[string]interface{}{
		"id":         key.ID.String(),
		"team_id":    key.ProfileID.String(),
		"label":      key.Label,
		"scopes":     key.Scopes,
		"rate_limit": key.RateLimit,
		"revoked_at": nil,
	}

	rows, err := s.repo.RevokeForProfile(ctx, profileID, id)
	if err != nil {
		return fmt.Errorf("failed to revoke api key for profile: %w", err)
	}
	if rows == 0 {
		// Race: key was revoked or reassigned between check and update.
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("api key with id '%s' not found", id.String()))
	}

	if s.sessionInvalidator != nil {
		if err := s.sessionInvalidator.InvalidateKeySessions(ctx, profileID.String(), id.String()); err != nil {
			s.logger.Warn("session_invalidation_failed", slog.String("error", err.Error()), slog.String("key_id", id.String()))
		}
	}

	profileIDStr := profileID.String()
	if err := s.auditService.APIKeyRevoked(ctx, &profileIDStr, key.ID.String(), beforePayload, actorKeyID, actorRole, clientIP, correlationID); err != nil {
		s.logAuditError(err, "REVOKE", key.ID.String(), correlationID)
	}

	return nil
}

// DeleteForProfile hard-deletes an API key scoped to the caller's profile.
// Returns NOT_FOUND when the id/profile combination does not match.
// Invalidates active sessions and writes an append-only audit event.
func (s *APIKeyServiceImpl) DeleteForProfile(ctx context.Context, profileID, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	key, err := s.repo.GetByIDForProfile(ctx, profileID, id)
	if err != nil {
		return fmt.Errorf("failed to get api key for profile: %w", err)
	}
	if key == nil {
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("api key with id '%s' not found", id.String()))
	}

	beforePayload := map[string]interface{}{
		"id":           key.ID.String(),
		"team_id":      key.ProfileID.String(),
		"rate_limit":   key.RateLimit,
		"last_used_at": key.LastUsedAt,
		"expires_at":   key.ExpiresAt,
		"created_at":   key.CreatedAt,
		"revoked_at":   key.RevokedAt,
	}

	rows, err := s.repo.DeleteForProfile(ctx, profileID, id)
	if err != nil {
		return fmt.Errorf("failed to delete api key for profile: %w", err)
	}
	if rows == 0 {
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("api key with id '%s' not found", id.String()))
	}

	if s.sessionInvalidator != nil {
		if err := s.sessionInvalidator.InvalidateKeySessions(ctx, profileID.String(), id.String()); err != nil {
			s.logger.Warn("session_invalidation_failed", slog.String("error", err.Error()), slog.String("key_id", id.String()))
		}
	}

	profileIDStr := profileID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:     &profileIDStr,
		Operation:     "DELETE",
		EntityType:    "api_key",
		EntityID:      key.ID.String(),
		BeforePayload: beforePayload,
		ActorKeyID:    actorKeyID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}); err != nil {
		s.logAuditError(err, "DELETE", key.ID.String(), correlationID)
	}

	return nil
}
