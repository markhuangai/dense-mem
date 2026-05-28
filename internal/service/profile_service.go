package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
)

// ProfileService is the companion interface for profile business logic.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type ProfileService interface {
	Create(ctx context.Context, req CreateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.Profile, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error)
	List(ctx context.Context, limit, offset int) ([]*domain.Profile, error)
	Count(ctx context.Context) (int64, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error)
	Delete(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error
}

// CreateProfileRequest represents a request to create a new profile.
type CreateProfileRequest struct {
	Name        string
	Description string
	Metadata    map[string]any
	Config      map[string]any
}

// UpdateProfileRequest represents a request to update an existing profile.
// Uses pointer fields for PATCH semantics - only non-nil fields are updated.
type UpdateProfileRequest struct {
	Name        *string
	Description *string
	Metadata    map[string]any
	Config      map[string]any
}

// ProfileServiceImpl implements the ProfileService interface.
type ProfileServiceImpl struct {
	repo         repository.ProfileRepository
	auditService AuditService
	statePurger  ProfileStatePurger
	dataPurger   ProfileDataPurger
	logger       *slog.Logger
}

// Ensure ProfileServiceImpl implements ProfileService
var _ ProfileService = (*ProfileServiceImpl)(nil)

// NewProfileService creates a new profile service instance.
func NewProfileService(repo repository.ProfileRepository, auditService AuditService, statePurger ProfileStatePurger) *ProfileServiceImpl {
	return &ProfileServiceImpl{
		repo:         repo,
		auditService: auditService,
		statePurger:  statePurger,
		logger:       slog.Default(),
	}
}

// NewProfileServiceWithDataPurger creates a profile service that also purges
// profile-owned non-Postgres state during profile deletion.
func NewProfileServiceWithDataPurger(repo repository.ProfileRepository, auditService AuditService, statePurger ProfileStatePurger, dataPurger ProfileDataPurger) *ProfileServiceImpl {
	svc := NewProfileService(repo, auditService, statePurger)
	svc.dataPurger = dataPurger
	return svc
}

// NewProfileServiceWithLogger creates a new profile service instance with a custom logger.
func NewProfileServiceWithLogger(repo repository.ProfileRepository, auditService AuditService, statePurger ProfileStatePurger, logger *slog.Logger) *ProfileServiceImpl {
	return &ProfileServiceImpl{
		repo:         repo,
		auditService: auditService,
		statePurger:  statePurger,
		logger:       logger,
	}
}

// logAuditError logs an audit service error with structured logging.
func (s *ProfileServiceImpl) logAuditError(err error, operation, profileID, correlationID string) {
	if s.logger == nil {
		return
	}
	s.logger.Error("audit_log_write_failed",
		slog.String("error", err.Error()),
		slog.String("operation", operation),
		slog.String("team_id", profileID),
		slog.String("correlation_id", correlationID),
	)
}

// Create creates a new profile with server-side UUID generation.
// Enforces unique lower(name) among non-deleted rows and sets status=active.
func (s *ProfileServiceImpl) Create(ctx context.Context, req CreateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
	// Create the profile
	profile := &domain.Profile{
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
		Config:      req.Config,
	}

	if err := s.repo.Create(ctx, profile); err != nil {
		// Check for unique constraint violation (23505 is PostgreSQL unique constraint error code)
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return nil, httperr.New(httperr.CONFLICT, fmt.Sprintf("profile with name '%s' already exists", req.Name))
		}
		return nil, fmt.Errorf("failed to create profile: %w", err)
	}

	// Audit the creation
	afterPayload := map[string]interface{}{
		"id":          profile.ID.String(),
		"name":        profile.Name,
		"description": profile.Description,
		"metadata":    profile.Metadata,
		"config":      profile.Config,
		"status":      "active",
	}

	if err := s.auditService.ProfileCreated(ctx, profile.ID.String(), afterPayload, actorKeyID, actorRole, clientIP, correlationID); err != nil {
		// Log the audit failure but don't fail the operation
		s.logAuditError(err, "CREATE", profile.ID.String(), correlationID)
	}

	return profile, nil
}

// Get retrieves a profile by ID. Deleted profiles return 404.
func (s *ProfileServiceImpl) Get(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	profile, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get profile: %w", err)
	}
	if profile == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("profile with id '%s' not found", id.String()))
	}
	return profile, nil
}

// List retrieves profiles with pagination.
// Default limit=20, max limit=100, excludes deleted profiles.
func (s *ProfileServiceImpl) List(ctx context.Context, limit, offset int) ([]*domain.Profile, error) {
	return s.repo.List(ctx, limit, offset)
}

// GetByID retrieves a profile by ID. Deleted profiles return nil, nil.
// This is for internal use (e.g., profile resolution middleware) without audit logging.
func (s *ProfileServiceImpl) GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	return s.repo.GetByID(ctx, id)
}

// Count returns the total number of non-deleted profiles.
func (s *ProfileServiceImpl) Count(ctx context.Context) (int64, error) {
	return s.repo.Count(ctx)
}

// Update updates an existing profile using PATCH semantics.
// Only non-nil fields in the request are updated.
func (s *ProfileServiceImpl) Update(ctx context.Context, id uuid.UUID, req UpdateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
	// Get the existing profile
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get profile: %w", err)
	}
	if existing == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("profile with id '%s' not found", id.String()))
	}

	// Build before payload for audit
	beforePayload := map[string]interface{}{
		"id":          existing.ID.String(),
		"name":        existing.Name,
		"description": existing.Description,
		"metadata":    existing.Metadata,
		"config":      existing.Config,
	}

	// Apply PATCH semantics - only update non-nil fields
	if req.Name != nil {
		// Check for name conflict if name is being changed
		if *req.Name != existing.Name {
			exists, err := s.repo.NameExists(ctx, *req.Name)
			if err != nil {
				return nil, fmt.Errorf("failed to check name existence: %w", err)
			}
			if exists {
				return nil, httperr.New(httperr.CONFLICT, fmt.Sprintf("profile with name '%s' already exists", *req.Name))
			}
			existing.Name = *req.Name
		}
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.Metadata != nil {
		existing.Metadata = req.Metadata
	}
	if req.Config != nil {
		existing.Config = req.Config
	}

	// Save the changes
	if err := s.repo.Update(ctx, existing); err != nil {
		// Check for unique constraint violation (23505 is PostgreSQL unique constraint error code)
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			name := existing.Name
			if req.Name != nil {
				name = *req.Name
			}
			return nil, httperr.New(httperr.CONFLICT, fmt.Sprintf("profile with name '%s' already exists", name))
		}
		return nil, fmt.Errorf("failed to update profile: %w", err)
	}

	// Build after payload for audit
	afterPayload := map[string]interface{}{
		"id":          existing.ID.String(),
		"name":        existing.Name,
		"description": existing.Description,
		"metadata":    existing.Metadata,
		"config":      existing.Config,
	}

	// Audit the update
	if err := s.auditService.ProfileUpdated(ctx, existing.ID.String(), beforePayload, afterPayload, actorKeyID, actorRole, clientIP, correlationID); err != nil {
		// Log the audit failure but don't fail the operation
		s.logAuditError(err, "UPDATE", existing.ID.String(), correlationID)
	}

	return existing, nil
}

// PROFILE_HAS_ACTIVE_KEYS is kept for backward-compat with callers referencing the const.
// Profile deletion no longer blocks on active keys; current deletion removes
// profile-owned API keys as part of the hard-delete path.
const PROFILE_HAS_ACTIVE_KEYS = string(httperr.PROFILE_HAS_ACTIVE_KEYS)

// ProfileHasActiveKeysError represents a 409 conflict when profile has active keys.
// Retained for typed error matching in service-layer callers; HTTP handlers should rely
// on the typed httperr.APIError returned from Delete().
type ProfileHasActiveKeysError struct {
	ProfileID uuid.UUID
}

func (e *ProfileHasActiveKeysError) Error() string {
	return fmt.Sprintf("profile %s has active keys and cannot be deleted", e.ProfileID.String())
}

// Delete hard-deletes a profile and profile-owned database rows.
// audit_log remains append-only and stores historical entity IDs without live FKs.
func (s *ProfileServiceImpl) Delete(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	// Get the existing profile
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get profile: %w", err)
	}
	if existing == nil {
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("profile with id '%s' not found", id.String()))
	}

	// Build before payload for audit
	beforePayload := map[string]interface{}{
		"id":          existing.ID.String(),
		"name":        existing.Name,
		"description": existing.Description,
		"metadata":    existing.Metadata,
		"config":      existing.Config,
		"status":      "active",
	}

	// Delete Postgres profile-owned rows. audit_log is not deleted or mutated.
	if err := s.repo.HardDelete(ctx, id); err != nil {
		return fmt.Errorf("failed to delete profile: %w", err)
	}

	profileIDStr := existing.ID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:     &profileIDStr,
		Operation:     "DELETE",
		EntityType:    "profile",
		EntityID:      existing.ID.String(),
		BeforePayload: beforePayload,
		ActorKeyID:    actorKeyID,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
	}); err != nil {
		// Log the audit failure but don't fail the operation
		s.logAuditError(err, "DELETE", existing.ID.String(), correlationID)
	}

	// Purge all profile state (cache, session, stream) after hard-delete succeeds (nil-safe)
	if s.statePurger != nil {
		if err := s.statePurger.PurgeProfileState(ctx, id.String()); err != nil {
			// Log but don't fail the operation
			s.logger.Warn("profile_state_purge_failed", slog.String("error", err.Error()), slog.String("team_id", id.String()))
		}
	}

	if s.dataPurger != nil {
		if err := s.dataPurger.PurgeProfileData(ctx, id.String()); err != nil {
			return fmt.Errorf("profile row deleted but failed to purge profile data: %w", err)
		}
	}

	return nil
}
