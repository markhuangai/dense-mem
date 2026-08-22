package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

// TeamService is the companion interface for team business logic.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type TeamService interface {
	Create(ctx context.Context, req CreateTeamRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Team, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.Team, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error)
	List(ctx context.Context, limit, offset int) ([]*domain.Team, error)
	Count(ctx context.Context) (int64, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateTeamRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Team, error)
	Delete(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error
}

// CreateTeamRequest represents a request to create a new team.
type CreateTeamRequest struct {
	Name        string
	Description string
	Metadata    map[string]any
	Config      map[string]any
}

// UpdateTeamRequest represents a request to update an existing team.
// Uses pointer fields for PATCH semantics - only non-nil fields are updated.
type UpdateTeamRequest struct {
	Name        *string
	Description *string
	Metadata    map[string]any
	Config      map[string]any
}

// TeamServiceImpl implements the TeamService interface.
type TeamServiceImpl struct {
	repo         TeamStore
	auditService AuditService
	statePurger  TeamStatePurger
	logger       *slog.Logger
}

// Ensure TeamServiceImpl implements TeamService
var _ TeamService = (*TeamServiceImpl)(nil)

// NewTeamService creates a new team service instance.
func NewTeamService(repo TeamStore, auditService AuditService, statePurger TeamStatePurger) *TeamServiceImpl {
	return &TeamServiceImpl{
		repo:         repo,
		auditService: auditService,
		statePurger:  statePurger,
		logger:       slog.Default(),
	}
}

// NewTeamServiceWithLogger creates a new team service instance with a custom logger.
func NewTeamServiceWithLogger(repo TeamStore, auditService AuditService, statePurger TeamStatePurger, logger *slog.Logger) *TeamServiceImpl {
	return &TeamServiceImpl{
		repo:         repo,
		auditService: auditService,
		statePurger:  statePurger,
		logger:       logger,
	}
}

// logAuditError logs an audit service error with structured logging.
func (s *TeamServiceImpl) logAuditError(err error, operation, teamID, correlationID string) {
	if s.logger == nil {
		return
	}
	s.logger.Error("audit_log_write_failed",
		slog.String("error", err.Error()),
		slog.String("operation", operation),
		slog.String("team_id", teamID),
		slog.String("correlation_id", correlationID),
	)
}

// Create creates a new team with server-side UUID generation.
// Enforces unique lower(name) among non-deleted rows and sets status=active.
func (s *TeamServiceImpl) Create(ctx context.Context, req CreateTeamRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Team, error) {
	if err := validateMemoryWriteConfig(req.Config); err != nil {
		return nil, err
	}

	// Create the team
	team := &domain.Team{
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
		Config:      req.Config,
	}

	if err := s.repo.Create(ctx, team); err != nil {
		// Check for unique constraint violation (23505 is PostgreSQL unique constraint error code)
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return nil, httperr.New(httperr.CONFLICT, fmt.Sprintf("team with name '%s' already exists", req.Name))
		}
		return nil, fmt.Errorf("failed to create team: %w", err)
	}

	// Audit the creation
	afterPayload := map[string]interface{}{
		"id":          team.ID.String(),
		"name":        team.Name,
		"description": team.Description,
		"metadata":    team.Metadata,
		"config":      team.Config,
		"status":      "active",
	}

	if err := s.auditService.TeamCreated(ctx, team.ID.String(), afterPayload, actorKeyID, actorRole, clientIP, correlationID); err != nil {
		// Log the audit failure but don't fail the operation
		s.logAuditError(err, "CREATE", team.ID.String(), correlationID)
	}

	return team, nil
}

// Get retrieves a team by ID. Deleted teams return 404.
func (s *TeamServiceImpl) Get(ctx context.Context, id uuid.UUID) (*domain.Team, error) {
	team, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get team: %w", err)
	}
	if team == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team with id '%s' not found", id.String()))
	}
	return team, nil
}

// List retrieves teams with pagination.
// Default limit=20, max limit=100, excludes deleted teams.
func (s *TeamServiceImpl) List(ctx context.Context, limit, offset int) ([]*domain.Team, error) {
	return s.repo.List(ctx, limit, offset)
}

// GetByID retrieves a team by ID. Deleted teams return nil, nil.
// This is for internal use (e.g., team resolution middleware) without audit logging.
func (s *TeamServiceImpl) GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error) {
	return s.repo.GetByID(ctx, id)
}

// Count returns the total number of non-deleted teams.
func (s *TeamServiceImpl) Count(ctx context.Context) (int64, error) {
	return s.repo.Count(ctx)
}

// Update updates an existing team using PATCH semantics.
// Only non-nil fields in the request are updated.
func (s *TeamServiceImpl) Update(ctx context.Context, id uuid.UUID, req UpdateTeamRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Team, error) {
	// Get the existing team
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get team: %w", err)
	}
	if existing == nil {
		return nil, httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team with id '%s' not found", id.String()))
	}
	if err := validateMemoryWriteConfig(req.Config); err != nil {
		return nil, err
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
				return nil, httperr.New(httperr.CONFLICT, fmt.Sprintf("team with name '%s' already exists", *req.Name))
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
			return nil, httperr.New(httperr.CONFLICT, fmt.Sprintf("team with name '%s' already exists", name))
		}
		return nil, fmt.Errorf("failed to update team: %w", err)
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
	if err := s.auditService.TeamUpdated(ctx, existing.ID.String(), beforePayload, afterPayload, actorKeyID, actorRole, clientIP, correlationID); err != nil {
		// Log the audit failure but don't fail the operation
		s.logAuditError(err, "UPDATE", existing.ID.String(), correlationID)
	}

	return existing, nil
}

func validateMemoryWriteConfig(config map[string]any) error {
	if config == nil {
		return nil
	}
	memoryWrite, exists := config["memory_write"]
	if !exists || memoryWrite == nil {
		return nil
	}
	section, ok := memoryWrite.(map[string]any)
	if !ok {
		return httperr.New(httperr.VALIDATION_ERROR, "memory_write must be an object")
	}
	rawThreshold, exists := section["auto_write_confidence_threshold"]
	if !exists || rawThreshold == nil {
		return nil
	}
	threshold, ok := memoryWriteConfidenceThreshold(rawThreshold)
	if !ok || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
		return httperr.New(httperr.VALIDATION_ERROR, "memory_write.auto_write_confidence_threshold must be a number between 0 and 1")
	}
	return nil
}

func memoryWriteConfidenceThreshold(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int8:
		return float64(value), true
	case int16:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint8:
		return float64(value), true
	case uint16:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

// Delete tombstones a team and revokes its active API keys.
// Knowledge, team, and audit rows remain available for historical trace.
func (s *TeamServiceImpl) Delete(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	// Get the existing team
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get team: %w", err)
	}
	if existing == nil {
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team with id '%s' not found", id.String()))
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

	if err := s.repo.SoftDelete(ctx, id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("team with id '%s' not found", id.String()))
		}
		return fmt.Errorf("failed to delete team: %w", err)
	}

	teamIDStr := existing.ID.String()
	if err := s.auditService.Append(ctx, AuditLogEntry{
		ProfileID:     &teamIDStr,
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

	// Purge all team state (cache, session, stream) after hard-delete succeeds (nil-safe)
	if s.statePurger != nil {
		if err := s.statePurger.PurgeTeamState(ctx, id.String()); err != nil {
			// Log but don't fail the operation
			s.logger.Warn("team_state_purge_failed", slog.String("error", err.Error()), slog.String("team_id", id.String()))
		}
	}

	return nil
}
