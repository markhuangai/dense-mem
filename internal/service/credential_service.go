package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

// CreateCredentialRequest represents a request to create a standard API credential.
// Every standard credential belongs to one credential. Scopes are normalized into
// canonical read/read-write sets with optional feedback read access.
type CreateCredentialRequest struct {
	Name            string     `json:"name"`
	RateLimit       int        `json:"rate_limit"`
	ExpiresAt       *time.Time `json:"expires_at"`
	Scopes          []string   `json:"scopes"`
	Role            string     `json:"role"`
	OwnerIdentityID *uuid.UUID `json:"-"`
}

const (
	CredentialScopeRead         = "read"
	CredentialScopeWrite        = "write"
	CredentialScopeFeedbackRead = "feedback:read"
	CredentialRoleManager       = "manager"
	CredentialRoleMember        = "member"
)

var standardCredentialScopes = []string{CredentialScopeRead, CredentialScopeWrite}

const credentialScopeValidationMessage = "api credential scopes must be one of [read], [read, write], [read, feedback:read], or [read, write, feedback:read]"

// StandardCredentialScopes returns the default read/write scope set.
func StandardCredentialScopes() []string {
	return append([]string(nil), standardCredentialScopes...)
}

// CredentialScopeValidationMessage returns the user-facing validation message for
// supported API-credential scope sets.
func CredentialScopeValidationMessage() string {
	return credentialScopeValidationMessage
}

// NormalizeCredentialScopes returns the canonical scope set for a credential creation
// request. Empty input preserves the historical default: read/write.
func NormalizeCredentialScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return StandardCredentialScopes(), nil
	}

	var hasRead, hasWrite, hasFeedbackRead bool
	for _, raw := range scopes {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case CredentialScopeRead:
			hasRead = true
		case CredentialScopeWrite:
			hasWrite = true
		case CredentialScopeFeedbackRead:
			hasFeedbackRead = true
		default:
			return nil, httperr.New(httperr.VALIDATION_ERROR, credentialScopeValidationMessage)
		}
	}

	if !hasRead {
		return nil, httperr.New(httperr.VALIDATION_ERROR, credentialScopeValidationMessage)
	}

	normalized := []string{CredentialScopeRead}
	if hasWrite {
		normalized = append(normalized, CredentialScopeWrite)
	}
	if hasFeedbackRead {
		normalized = append(normalized, CredentialScopeFeedbackRead)
	}
	return normalized, nil
}

func managerCredentialScopes(scopes []string) []string {
	normalized := StandardCredentialScopes()
	if hasCredentialScope(scopes, CredentialScopeFeedbackRead) {
		normalized = append(normalized, CredentialScopeFeedbackRead)
	}
	return normalized
}

func hasCredentialScope(scopes []string, target string) bool {
	for _, scope := range scopes {
		if scope == target {
			return true
		}
	}
	return false
}

func NormalizeCredentialRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "":
		return "", nil
	case CredentialRoleManager:
		return CredentialRoleManager, nil
	case CredentialRoleMember:
		return CredentialRoleMember, nil
	default:
		return "", httperr.New(httperr.VALIDATION_ERROR, "api credential role must be either manager or member")
	}
}

func normalizeCredentialName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", httperr.New(httperr.VALIDATION_ERROR, "credential name is required")
	}
	if len([]rune(trimmed)) > 100 {
		return "", httperr.New(httperr.VALIDATION_ERROR, "credential name must be at most 100 characters")
	}
	return trimmed, nil
}

func credentialNameConflict(err error, name string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return httperr.New(httperr.CONFLICT, fmt.Sprintf("credential with name '%s' already exists for this team", name))
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return httperr.New(httperr.CONFLICT, fmt.Sprintf("credential with name '%s' already exists for this team", name))
	}
	return nil
}

func credentialCreateConflict(err error, name string) error {
	if uniqueViolationName(err) == "idx_credentials_owner_team_active_unique" {
		return httperr.New(httperr.CONFLICT, "sso-owned api credential already exists for this team")
	}
	return credentialNameConflict(err, name)
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

// CredentialSessionInvalidator invalidates credential-bound sessions. Redis-backed deployments
// delete matching session credentials; no-Redis deployments inject a non-nil no-op.
type CredentialSessionInvalidator interface {
	InvalidateCredentialSessions(ctx context.Context, teamID, credentialID string) error
}

// CredentialService is the companion interface for API credential business logic.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type CredentialService interface {
	CreateCredential(ctx context.Context, teamID uuid.UUID, req CreateCredentialRequest, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, string, error)
	ListByTeam(ctx context.Context, teamID uuid.UUID, limit, offset int) ([]*domain.Credential, error)
	CountByTeam(ctx context.Context, teamID uuid.UUID) (int64, error)
	// GetByIDForTeam returns the credential only when it belongs to teamID; NOT_FOUND otherwise (no existence oracle).
	GetByIDForTeam(ctx context.Context, teamID, id uuid.UUID) (*domain.Credential, error)
	GetSSOOwnedCredential(ctx context.Context, teamID, identityID uuid.UUID) (*domain.Credential, error)
	// RevokeForTeam revokes the credential only when it belongs to teamID; NOT_FOUND otherwise.
	RevokeForTeam(ctx context.Context, teamID, id uuid.UUID, actorCredentialID *string, actorRole, clientIP, correlationID string) error
	// DeleteForTeam hard-deletes the credential only when it belongs to teamID; NOT_FOUND otherwise.
	DeleteForTeam(ctx context.Context, teamID, id uuid.UUID, actorCredentialID *string, actorRole, clientIP, correlationID string) error
	// UpdateNameForTeam renames the credential without changing credential material.
	UpdateNameForTeam(ctx context.Context, teamID, id uuid.UUID, name string, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, error)
	// UpdateRoleForTeam changes the credential role without changing credential material.
	// Manager targets persist read/write scopes.
	UpdateRoleForTeam(ctx context.Context, teamID, id uuid.UUID, role string, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, error)
	// UpdateScopesForTeam changes the credential scopes without changing credential material.
	UpdateScopesForTeam(ctx context.Context, teamID, id uuid.UUID, scopes []string, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, error)
	// RotateForTeam rotates credential material in place for a named credential.
	RotateForTeam(ctx context.Context, teamID, id uuid.UUID, req CreateCredentialRequest, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, string, error)
}

// CredentialServiceImpl implements the CredentialService interface.
type CredentialServiceImpl struct {
	repo               repository.CredentialRepository
	teamService        TeamService
	auditService       AuditService
	sessionInvalidator CredentialSessionInvalidator
	logger             *slog.Logger
}

// Ensure CredentialServiceImpl implements CredentialService
var _ CredentialService = (*CredentialServiceImpl)(nil)

// NewCredentialService creates a new API credential service instance.
func NewCredentialService(
	repo repository.CredentialRepository,
	teamService TeamService,
	auditService AuditService,
	sessionInvalidator CredentialSessionInvalidator,
) *CredentialServiceImpl {
	return &CredentialServiceImpl{
		repo:               repo,
		teamService:        teamService,
		auditService:       auditService,
		sessionInvalidator: sessionInvalidator,
		logger:             slog.Default(),
	}
}

// NewCredentialServiceWithLogger creates a new API credential service instance with a custom logger.
func NewCredentialServiceWithLogger(
	repo repository.CredentialRepository,
	teamService TeamService,
	auditService AuditService,
	sessionInvalidator CredentialSessionInvalidator,
	logger *slog.Logger,
) *CredentialServiceImpl {
	return &CredentialServiceImpl{
		repo:               repo,
		teamService:        teamService,
		auditService:       auditService,
		sessionInvalidator: sessionInvalidator,
		logger:             logger,
	}
}

// logAuditError logs an audit service error with structured logging.
func (s *CredentialServiceImpl) logAuditError(err error, operation, credentialID, correlationID string) {
	if s.logger == nil {
		return
	}
	s.logger.Error("audit_log_write_failed",
		slog.String("error", err.Error()),
		slog.String("operation", operation),
		slog.String("key_id", credentialID),
		slog.String("correlation_id", correlationID),
	)
}

// CreateCredential creates a new standard API credential for a team.
// Returns the created credential and the plaintext raw credential (returned exactly once).
func (s *CredentialServiceImpl) CreateCredential(ctx context.Context, teamID uuid.UUID, req CreateCredentialRequest, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, string, error) {
	// Verify team exists
	_, err := s.teamService.Get(ctx, teamID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to verify team: %w", err)
	}

	scopes, err := NormalizeCredentialScopes(req.Scopes)
	if err != nil {
		return nil, "", err
	}

	role, err := NormalizeCredentialRole(req.Role)
	if err != nil {
		return nil, "", err
	}
	if role == "" {
		count, err := s.repo.CountByTeam(ctx, teamID)
		if err != nil {
			return nil, "", fmt.Errorf("failed to count credentials: %w", err)
		}
		if count == 0 {
			role = CredentialRoleManager
		} else {
			role = CredentialRoleMember
		}
	}
	if role == CredentialRoleManager {
		scopes = managerCredentialScopes(scopes)
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "default credential"
	}

	// Generate raw credential
	rawKey, err := crypto.GenerateRawKeyForCredential(name)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate raw credential: %w", err)
	}

	// Hash the credential
	keyHash, err := crypto.HashKey(rawKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to hash credential: %w", err)
	}

	// Create the credential record
	credential := &domain.Credential{
		TeamID:          teamID,
		Name:            name,
		KeyHash:         keyHash,
		KeyPrefix:       crypto.GetKeyPrefix(rawKey),
		KeySuffix:       crypto.GetKeySuffix(rawKey),
		Scopes:          scopes,
		Role:            role,
		RateLimit:       req.RateLimit,
		ExpiresAt:       req.ExpiresAt,
		OwnerIdentityID: req.OwnerIdentityID,
	}

	if err := s.repo.CreateCredential(ctx, credential); err != nil {
		if conflict := credentialCreateConflict(err, name); conflict != nil {
			return nil, "", conflict
		}
		if errors.Is(err, repository.ErrTeamInactive) {
			return nil, "", httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team with id '%s' not found", teamID.String()))
		}
		return nil, "", fmt.Errorf("failed to create api credential: %w", err)
	}

	// Audit the creation (without the raw credential or hash)
	afterPayload := map[string]interface{}{
		"id":              credential.ID.String(),
		"team_id":         credential.TeamID.String(),
		"credential_name": credential.Name,
		"scopes":          credential.Scopes,
		"rate_limit":      credential.RateLimit,
		"role":            credential.GetRole(),
	}
	if credential.ExpiresAt != nil {
		afterPayload["expires_at"] = credential.ExpiresAt.Format(time.RFC3339)
	}

	teamIDStr := teamID.String()
	if err := s.auditService.CredentialCreated(ctx, &teamIDStr, credential.ID.String(), afterPayload, actorCredentialID, actorRole, clientIP, correlationID); err != nil {
		// Log the audit failure but don't fail the operation
		s.logAuditError(err, "CREATE", credential.ID.String(), correlationID)
	}

	// Return the credential (without hash) and the raw credential (shown exactly once)
	credential.KeyHash = ""
	return credential, rawKey, nil
}

// UpdateRoleForTeam changes a credential's administrative role without
// changing credential material. Manager teams always keep read/write access.
func (s *CredentialServiceImpl) UpdateRoleForTeam(ctx context.Context, teamID, id uuid.UUID, role string, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, error) {
	normalizedRole, err := NormalizeCredentialRole(role)
	if err != nil {
		return nil, err
	}
	if normalizedRole == "" {
		return nil, httperr.New(httperr.VALIDATION_ERROR, "api credential role is required")
	}

	credential, err := s.repo.GetByIDForTeam(ctx, teamID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get credential: %w", err)
	}
	if credential == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("credential with id '%s' not found", id.String()))
	}
	currentRole := credential.GetRole()
	nextScopes := append([]string(nil), credential.Scopes...)
	if normalizedRole == CredentialRoleManager {
		nextScopes = managerCredentialScopes(nextScopes)
	}
	if currentRole == normalizedRole && slices.Equal(credential.Scopes, nextScopes) {
		credential.KeyHash = ""
		credential.Role = normalizedRole
		credential.Scopes = nextScopes
		return credential, nil
	}

	beforePayload := map[string]interface{}{
		"team_id":    teamID.String(),
		"profile_id": credential.ID.String(),
		"role":       currentRole,
		"scopes":     append([]string(nil), credential.Scopes...),
	}

	rows, err := s.repo.UpdateRoleForTeam(ctx, teamID, id, normalizedRole, nextScopes)
	if err != nil {
		return nil, fmt.Errorf("failed to update credential role: %w", err)
	}
	if rows == 0 {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("credential with id '%s' not found", id.String()))
	}

	credential.KeyHash = ""
	credential.Role = normalizedRole
	credential.Scopes = nextScopes

	teamIDStr := teamID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:     &teamIDStr,
		Operation:     "UPDATE",
		EntityType:    "api_key",
		EntityID:      credential.ID.String(),
		BeforePayload: beforePayload,
		AfterPayload: map[string]interface{}{
			"team_id":    teamID.String(),
			"profile_id": credential.ID.String(),
			"role":       normalizedRole,
			"scopes":     nextScopes,
		},
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}); err != nil {
		s.logAuditError(err, "UPDATE_ROLE", credential.ID.String(), correlationID)
	}

	return credential, nil
}

// UpdateScopesForTeam changes a credential's data scopes without rotating
// its credential. Manager teams always keep read/write and may only toggle feedback access.
func (s *CredentialServiceImpl) UpdateScopesForTeam(ctx context.Context, teamID, id uuid.UUID, scopes []string, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, error) {
	if len(scopes) == 0 {
		return nil, httperr.New(httperr.VALIDATION_ERROR, credentialScopeValidationMessage)
	}
	normalizedScopes, err := NormalizeCredentialScopes(scopes)
	if err != nil {
		return nil, err
	}

	credential, err := s.repo.GetByIDForTeam(ctx, teamID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get credential: %w", err)
	}
	if credential == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("credential with id '%s' not found", id.String()))
	}
	if credential.GetRole() == CredentialRoleManager {
		normalizedScopes = managerCredentialScopes(normalizedScopes)
	}
	if slices.Equal(credential.Scopes, normalizedScopes) {
		credential.KeyHash = ""
		credential.Scopes = normalizedScopes
		return credential, nil
	}

	beforePayload := map[string]interface{}{
		"team_id":    teamID.String(),
		"profile_id": credential.ID.String(),
		"scopes":     append([]string{}, credential.Scopes...),
	}

	rows, err := s.repo.UpdateScopesForTeam(ctx, teamID, id, normalizedScopes)
	if err != nil {
		return nil, fmt.Errorf("failed to update credential scopes: %w", err)
	}
	if rows == 0 {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("credential with id '%s' not found", id.String()))
	}

	credential.KeyHash = ""
	credential.Scopes = normalizedScopes

	teamIDStr := teamID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:     &teamIDStr,
		Operation:     "UPDATE",
		EntityType:    "api_key",
		EntityID:      credential.ID.String(),
		BeforePayload: beforePayload,
		AfterPayload: map[string]interface{}{
			"team_id":    teamID.String(),
			"profile_id": credential.ID.String(),
			"scopes":     normalizedScopes,
		},
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}); err != nil {
		s.logAuditError(err, "UPDATE_SCOPES", credential.ID.String(), correlationID)
	}

	return credential, nil
}

// UpdateNameForTeam renames a credential without rotating its credential.
func (s *CredentialServiceImpl) UpdateNameForTeam(ctx context.Context, teamID, id uuid.UUID, name string, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, error) {
	normalizedName, err := normalizeCredentialName(name)
	if err != nil {
		return nil, err
	}

	credential, err := s.repo.GetByIDForTeam(ctx, teamID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get credential: %w", err)
	}
	if credential == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("credential with id '%s' not found", id.String()))
	}

	currentName := credential.Name
	if currentName == normalizedName {
		credential.KeyHash = ""
		credential.Name = normalizedName
		return credential, nil
	}

	beforePayload := map[string]interface{}{
		"team_id":      teamID.String(),
		"profile_id":   credential.ID.String(),
		"profile_name": currentName,
	}

	rows, err := s.repo.UpdateNameForTeam(ctx, teamID, id, normalizedName)
	if err != nil {
		if conflict := credentialNameConflict(err, normalizedName); conflict != nil {
			return nil, conflict
		}
		return nil, fmt.Errorf("failed to update credential name: %w", err)
	}
	if rows == 0 {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("credential with id '%s' not found", id.String()))
	}

	credential.KeyHash = ""
	credential.Name = normalizedName

	teamIDStr := teamID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:     &teamIDStr,
		Operation:     "UPDATE",
		EntityType:    "api_key",
		EntityID:      credential.ID.String(),
		BeforePayload: beforePayload,
		AfterPayload: map[string]interface{}{
			"team_id":      teamID.String(),
			"profile_id":   credential.ID.String(),
			"profile_name": normalizedName,
		},
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}); err != nil {
		s.logAuditError(err, "UPDATE", credential.ID.String(), correlationID)
	}

	return credential, nil
}

// RotateForTeam rotates a credential's API credential in place.
func (s *CredentialServiceImpl) RotateForTeam(ctx context.Context, teamID, id uuid.UUID, req CreateCredentialRequest, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, string, error) {
	credential, err := s.repo.GetByIDForTeam(ctx, teamID, id)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get credential: %w", err)
	}
	if credential == nil {
		return nil, "", httperr.New(httperr.NOT_FOUND, fmt.Sprintf("credential with id '%s' not found", id.String()))
	}

	name := credential.Name
	rawKey, err := crypto.GenerateRawKeyForCredential(name)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate raw credential: %w", err)
	}
	keyHash, err := crypto.HashKey(rawKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to hash credential: %w", err)
	}

	rows, err := s.repo.RotateForTeam(ctx, teamID, id, keyHash, crypto.GetKeyPrefix(rawKey), crypto.GetKeySuffix(rawKey), req.ExpiresAt)
	if err != nil {
		return nil, "", fmt.Errorf("failed to rotate credential credential: %w", err)
	}
	if rows == 0 {
		return nil, "", httperr.New(httperr.NOT_FOUND, fmt.Sprintf("credential with id '%s' not found", id.String()))
	}
	if s.sessionInvalidator != nil {
		if err := s.sessionInvalidator.InvalidateCredentialSessions(ctx, teamID.String(), id.String()); err != nil {
			s.logger.Warn("session_invalidation_failed", slog.String("error", err.Error()), slog.String("team_id", id.String()))
		}
	}

	credential.KeyHash = ""
	credential.KeyPrefix = crypto.GetKeyPrefix(rawKey)
	credential.KeySuffix = crypto.GetKeySuffix(rawKey)
	credential.ExpiresAt = req.ExpiresAt
	credential.LastUsedAt = nil
	credential.RevokedAt = nil

	teamIDStr := teamID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:  &teamIDStr,
		Operation:  "ROTATE_KEY",
		EntityType: "api_key",
		EntityID:   credential.ID.String(),
		AfterPayload: map[string]interface{}{
			"team_id":         teamID.String(),
			"profile_id":      credential.ID.String(),
			"credential_name": credential.Name,
		},
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}); err != nil {
		s.logAuditError(err, "ROTATE", credential.ID.String(), correlationID)
	}

	return credential, rawKey, nil
}

// ListByTeam retrieves API credentials for a team.
// Never returns the key_hash field.
func (s *CredentialServiceImpl) ListByTeam(ctx context.Context, teamID uuid.UUID, limit, offset int) ([]*domain.Credential, error) {
	return s.repo.ListByTeam(ctx, teamID, limit, offset)
}

// CountByTeam returns the total number of API credentials for a team (used for pagination totals).
func (s *CredentialServiceImpl) CountByTeam(ctx context.Context, teamID uuid.UUID) (int64, error) {
	return s.repo.CountByTeam(ctx, teamID)
}

// GetByIDForTeam retrieves an API credential by ID scoped to the caller's team.
// Returns NOT_FOUND when the id/team combination does not match (prevents existence oracle).
func (s *CredentialServiceImpl) GetByIDForTeam(ctx context.Context, teamID, id uuid.UUID) (*domain.Credential, error) {
	credential, err := s.repo.GetByIDForTeam(ctx, teamID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get api credential for team: %w", err)
	}
	if credential == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("api credential with id '%s' not found", id.String()))
	}
	return credential, nil
}

func (s *CredentialServiceImpl) GetSSOOwnedCredential(ctx context.Context, teamID, identityID uuid.UUID) (*domain.Credential, error) {
	credential, err := s.repo.GetSSOOwnedCredential(ctx, teamID, identityID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sso-owned api credential for team: %w", err)
	}
	return credential, nil
}

// RevokeForTeam revokes an API credential scoped to the caller's team.
// Returns NOT_FOUND when the id/team combination does not match.
// Invalidates active sessions and writes audit.
func (s *CredentialServiceImpl) RevokeForTeam(ctx context.Context, teamID, id uuid.UUID, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	// Fetch under team scope for audit before-payload and to verify ownership.
	credential, err := s.repo.GetByIDForTeam(ctx, teamID, id)
	if err != nil {
		return fmt.Errorf("failed to get api credential for team: %w", err)
	}
	if credential == nil {
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("api credential with id '%s' not found", id.String()))
	}
	if credential.RevokedAt != nil {
		return httperr.New(httperr.CONFLICT, "api credential is already revoked")
	}

	beforePayload := map[string]interface{}{
		"id":         credential.ID.String(),
		"team_id":    credential.TeamID.String(),
		"name":       credential.Name,
		"scopes":     credential.Scopes,
		"rate_limit": credential.RateLimit,
		"revoked_at": nil,
	}

	rows, err := s.repo.RevokeForTeam(ctx, teamID, id)
	if err != nil {
		return fmt.Errorf("failed to revoke api credential for team: %w", err)
	}
	if rows == 0 {
		// Race: credential was revoked or reassigned between check and update.
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("api credential with id '%s' not found", id.String()))
	}

	if s.sessionInvalidator != nil {
		if err := s.sessionInvalidator.InvalidateCredentialSessions(ctx, teamID.String(), id.String()); err != nil {
			s.logger.Warn("session_invalidation_failed", slog.String("error", err.Error()), slog.String("key_id", id.String()))
		}
	}

	teamIDStr := teamID.String()
	if err := s.auditService.CredentialRevoked(ctx, &teamIDStr, credential.ID.String(), beforePayload, actorCredentialID, actorRole, clientIP, correlationID); err != nil {
		s.logAuditError(err, "REVOKE", credential.ID.String(), correlationID)
	}

	return nil
}

// DeleteForTeam hard-deletes an API credential scoped to the caller's team.
// Returns NOT_FOUND when the id/team combination does not match.
// Invalidates active sessions and writes an append-only audit event.
func (s *CredentialServiceImpl) DeleteForTeam(ctx context.Context, teamID, id uuid.UUID, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	credential, err := s.repo.GetByIDForTeam(ctx, teamID, id)
	if err != nil {
		return fmt.Errorf("failed to get api credential for team: %w", err)
	}
	if credential == nil {
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("api credential with id '%s' not found", id.String()))
	}

	beforePayload := map[string]interface{}{
		"id":           credential.ID.String(),
		"team_id":      credential.TeamID.String(),
		"rate_limit":   credential.RateLimit,
		"last_used_at": credential.LastUsedAt,
		"expires_at":   credential.ExpiresAt,
		"created_at":   credential.CreatedAt,
		"revoked_at":   credential.RevokedAt,
	}

	rows, err := s.repo.DeleteForTeam(ctx, teamID, id)
	if err != nil {
		return fmt.Errorf("failed to delete api credential for team: %w", err)
	}
	if rows == 0 {
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("api credential with id '%s' not found", id.String()))
	}

	if s.sessionInvalidator != nil {
		if err := s.sessionInvalidator.InvalidateCredentialSessions(ctx, teamID.String(), id.String()); err != nil {
			s.logger.Warn("session_invalidation_failed", slog.String("error", err.Error()), slog.String("key_id", id.String()))
		}
	}

	teamIDStr := teamID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:     &teamIDStr,
		Operation:     "DELETE",
		EntityType:    "api_key",
		EntityID:      credential.ID.String(),
		BeforePayload: beforePayload,
		ActorKeyID:    actorCredentialID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}); err != nil {
		s.logAuditError(err, "DELETE", credential.ID.String(), correlationID)
	}

	return nil
}
