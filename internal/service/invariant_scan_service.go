package service

import (
	"context"
	"errors"
)

var ErrInvariantScanRemoved = errors.New("legacy graph invariant scan was removed after V2 cutover")

// InvariantFinding represents a single relationship isolation violation.
type InvariantFinding struct {
	FromProfileID string `json:"from_team_id"`
	RelType       string `json:"rel_type"`
	ToProfileID   string `json:"to_team_id"`
}

// InvariantScanResult represents the result of an invariant scan.
type InvariantScanResult struct {
	Violations int                `json:"violations"`
	Status     string             `json:"status"`
	Findings   []InvariantFinding `json:"findings,omitempty"`
}

// InvariantScanService is retained for control-plane compatibility.
type InvariantScanService interface {
	Scan(ctx context.Context) (*InvariantScanResult, error)
	ScanWithAudit(ctx context.Context, actorKeyID *string, actorRole, clientIP, correlationID string) (*InvariantScanResult, error)
}

type invariantScanService struct {
	auditSvc AuditService
}

var _ InvariantScanService = (*invariantScanService)(nil)

func NewInvariantScanService(_ any, auditSvc AuditService) InvariantScanService {
	return &invariantScanService{auditSvc: auditSvc}
}

func (s *invariantScanService) Scan(context.Context) (*InvariantScanResult, error) {
	return &InvariantScanResult{Status: "removed"}, ErrInvariantScanRemoved
}

func (s *invariantScanService) ScanWithAudit(ctx context.Context, actorKeyID *string, actorRole, clientIP, correlationID string) (*InvariantScanResult, error) {
	result, err := s.Scan(ctx)
	if s != nil && s.auditSvc != nil {
		_ = s.auditSvc.SystemQuery(ctx, "invariant_scan", map[string]any{
			"status":  result.Status,
			"success": false,
			"error":   err.Error(),
		}, actorKeyID, actorRole, clientIP, correlationID)
	}
	return result, err
}
