// Package service keeps the historical audit import path as a compatibility
// facade. Audit policy and persistence live in internal/audit.
package service

import (
	"context"
	"log/slog"
	"os"

	"gorm.io/gorm"

	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// AuditLogEntry and AuditService remain source-compatible aliases for callers
// that have not yet migrated to the capability-owned API.
type AuditLogEntry = accessservice.AuditLogEntry
type AuditService = accessservice.AuditService

// AuditServiceImpl forwards the historical constructor and methods to the
// capability owner. Its fields are retained only for old in-package tests and
// compatibility callers that configure the legacy SQL seam.
type AuditServiceImpl struct {
	db     *gorm.DB
	logger *slog.Logger
	rls    storagepostgres.RLSHelper
	inner  *auditapp.Service
}

var _ AuditService = (*AuditServiceImpl)(nil)

// NewAuditService creates the compatibility facade around the native audit
// service and PostgreSQL adapter.
func NewAuditService(db *gorm.DB) *AuditServiceImpl {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rls := storagepostgres.NewRLS()
	return &AuditServiceImpl{db: db, logger: logger, rls: rls, inner: auditapp.New(auditpostgres.NewStore(db, rls))}
}

// NewAuditServiceWithLogger preserves the historical test and integration
// constructor while forwarding all behavior to internal/audit.
func NewAuditServiceWithLogger(db *gorm.DB, logger *slog.Logger) *AuditServiceImpl {
	rls := storagepostgres.NewRLS()
	return &AuditServiceImpl{db: db, logger: logger, rls: rls, inner: auditapp.New(auditpostgres.NewStore(db, rls))}
}

func (s *AuditServiceImpl) native() *auditapp.Service {
	if s == nil {
		return auditapp.New(nil)
	}
	if s.inner == nil {
		if s.db == nil {
			s.inner = auditapp.New(nil)
		} else {
			s.inner = auditapp.New(auditpostgres.NewStore(s.db, s.rls))
		}
	}
	return s.inner
}

func (s *AuditServiceImpl) Append(ctx context.Context, entry AuditLogEntry) error {
	return s.native().Append(ctx, entry)
}

func (s *AuditServiceImpl) List(ctx context.Context, teamID string, limit, offset int) ([]AuditLogEntry, int, error) {
	return s.native().List(ctx, teamID, limit, offset)
}

func (s *AuditServiceImpl) TeamCreated(ctx context.Context, teamID string, afterPayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	return s.native().TeamCreated(ctx, teamID, afterPayload, actorCredentialID, actorRole, clientIP, correlationID)
}

func (s *AuditServiceImpl) TeamUpdated(ctx context.Context, teamID string, beforePayload, afterPayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	return s.native().TeamUpdated(ctx, teamID, beforePayload, afterPayload, actorCredentialID, actorRole, clientIP, correlationID)
}

func (s *AuditServiceImpl) TeamDeleteBlocked(ctx context.Context, teamID string, beforePayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string, reason string) error {
	return s.native().TeamDeleteBlocked(ctx, teamID, beforePayload, actorCredentialID, actorRole, clientIP, correlationID, reason)
}

func (s *AuditServiceImpl) TeamDeleted(ctx context.Context, teamID string, beforePayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	return s.native().TeamDeleted(ctx, teamID, beforePayload, actorCredentialID, actorRole, clientIP, correlationID)
}

func (s *AuditServiceImpl) CredentialCreated(ctx context.Context, teamID *string, credentialID string, afterPayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	return s.native().CredentialCreated(ctx, teamID, credentialID, afterPayload, actorCredentialID, actorRole, clientIP, correlationID)
}

func (s *AuditServiceImpl) CredentialRevoked(ctx context.Context, teamID *string, credentialID string, beforePayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	return s.native().CredentialRevoked(ctx, teamID, credentialID, beforePayload, actorCredentialID, actorRole, clientIP, correlationID)
}

func (s *AuditServiceImpl) AuthFailure(ctx context.Context, profileID *string, entityType, entityID string, metadata map[string]interface{}, clientIP, correlationID string) error {
	return s.native().AuthFailure(ctx, profileID, entityType, entityID, metadata, clientIP, correlationID)
}

func (s *AuditServiceImpl) CrossTeamDenied(ctx context.Context, actorTeamID, targetTeamID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	return s.native().CrossTeamDenied(ctx, actorTeamID, targetTeamID, operation, metadata, clientIP, correlationID)
}

func (s *AuditServiceImpl) RateLimited(ctx context.Context, profileID *string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	return s.native().RateLimited(ctx, profileID, operation, metadata, clientIP, correlationID)
}

func (s *AuditServiceImpl) SystemQuery(ctx context.Context, queryType string, metadata map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return s.native().SystemQuery(ctx, queryType, metadata, actorKeyID, actorRole, clientIP, correlationID)
}

func (s *AuditServiceImpl) InvariantViolation(ctx context.Context, entityType, entityID string, violation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	return s.native().InvariantViolation(ctx, entityType, entityID, violation, metadata, clientIP, correlationID)
}

// These helpers preserve the old package's narrow in-package test seam while
// delegating redaction and request-context policy to the native owner.
func redactPayload(payload map[string]interface{}) map[string]interface{} {
	return auditapp.RedactPayload(payload)
}

func auditClientIPValue(ctx context.Context, entry AuditLogEntry) any {
	return auditapp.AuditClientIPValue(ctx, entry)
}
