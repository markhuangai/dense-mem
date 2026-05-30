package service

import (
	"context"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// InvariantFinding represents a single cross-profile relationship violation.
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

// InvariantScanService is the companion interface for invariant scanning.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type InvariantScanService interface {
	Scan(ctx context.Context) (*InvariantScanResult, error)
	ScanWithAudit(ctx context.Context, actorKeyID *string, actorRole, clientIP, correlationID string) (*InvariantScanResult, error)
}

// Neo4jClientInterface defines the interface for unscoped Neo4j operations.
// This interface allows direct ExecuteRead without profile scoping.
// IMPORTANT: This interface is deliberately different from ScopedReader
// because invariant scans must bypass profile filtering.
type Neo4jClientInterface interface {
	ExecuteRead(ctx context.Context, fn neo4j.ManagedTransactionWork) (any, error)
}

// invariantScanService implements InvariantScanService.
// It scans for cross-profile relationship violations in the graph database.
//
// IMPORTANT: This service executes queries that cross profile boundaries by design.
// It bypasses the ScopedRead profile filter to detect invariant violations.
// This is a deliberate operator-only exception that requires audit logging.
type invariantScanService struct {
	client   Neo4jClientInterface
	auditSvc AuditService
}

// Ensure invariantScanService implements InvariantScanService
var _ InvariantScanService = (*invariantScanService)(nil)

// NewInvariantScanService creates a new invariant scan service.
func NewInvariantScanService(client Neo4jClientInterface, auditSvc AuditService) InvariantScanService {
	return &invariantScanService{
		client:   client,
		auditSvc: auditSvc,
	}
}

// Scan executes the invariant scan query to detect cross-profile relationships.
//
// The query crosses profile boundaries by design to find violations where:
// - A relationship exists between nodes of different profiles
// - This is an invariant violation that should never occur in normal operation
//
// IMPORTANT: This query must not be subject to ScopedRead's profile filter.
// It must execute at the database level without team_id scoping.
//
// Audit logging fires regardless of whether violations are found.
func (s *invariantScanService) Scan(ctx context.Context) (*InvariantScanResult, error) {
	// Query to find cross-profile relationships
	// This query intentionally does NOT use $profileId placeholder
	// because it must scan across all profiles
	query := `
		MATCH (a)-[r]->(b)
		WHERE a.team_id <> b.team_id
		RETURN a.team_id AS from_team_id, type(r) AS rel_type, b.team_id AS to_team_id
		LIMIT 100
	`

	var findings []InvariantFinding

	// Execute directly via ExecuteRead without profile scoping
	// This is the deliberate exception for operator-level invariant scanning.
	_, err := s.client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, query, nil)
		if err != nil {
			return nil, err
		}

		// Collect all records
		records, err := result.Collect(ctx)
		if err != nil {
			return nil, err
		}

		// Convert records to findings
		findings = make([]InvariantFinding, 0, len(records))
		for _, record := range records {
			fromProfileID, _ := record.Get("from_team_id")
			relType, _ := record.Get("rel_type")
			toProfileID, _ := record.Get("to_team_id")

			findings = append(findings, InvariantFinding{
				FromProfileID: fmt.Sprintf("%v", fromProfileID),
				RelType:       fmt.Sprintf("%v", relType),
				ToProfileID:   fmt.Sprintf("%v", toProfileID),
			})
		}

		return nil, nil
	})

	if err != nil {
		return nil, fmt.Errorf("invariant scan failed: %w", err)
	}

	// Build result
	result := &InvariantScanResult{
		Violations: len(findings),
	}

	if len(findings) == 0 {
		result.Status = "clean"
	} else {
		result.Status = "violations_found"
		result.Findings = findings
	}

	return result, nil
}

// ScanWithAudit executes the invariant scan and logs the result to the audit log.
// This ensures audit logging happens regardless of whether violations are found.
func (s *invariantScanService) ScanWithAudit(ctx context.Context, actorKeyID *string, actorRole, clientIP, correlationID string) (*InvariantScanResult, error) {
	// Execute the scan
	result, err := s.Scan(ctx)
	if result == nil {
		result = &InvariantScanResult{Status: "error"}
	}

	// Build audit metadata
	metadata := map[string]interface{}{
		"violations": result.Violations,
		"status":     result.Status,
		"success":    err == nil,
	}

	if err != nil {
		metadata["error"] = err.Error()
	} else if result.Violations > 0 {
		// Log each violation in the audit metadata (up to a reasonable limit)
		if len(result.Findings) <= 10 {
			findingsMeta := make([]map[string]interface{}, len(result.Findings))
			for i, f := range result.Findings {
				findingsMeta[i] = map[string]interface{}{
					"from_team_id": f.FromProfileID,
					"rel_type":     f.RelType,
					"to_team_id":   f.ToProfileID,
				}
			}
			metadata["findings"] = findingsMeta
		} else {
			// If too many findings, just log the count
			metadata["findings_count"] = len(result.Findings)
		}
	}

	// Log audit event for every scan execution
	// This is done synchronously to ensure it's recorded
	auditErr := s.auditSvc.SystemQuery(ctx, "invariant_scan", metadata, actorKeyID, actorRole, clientIP, correlationID)
	if auditErr != nil {
		// Log the audit error but don't fail the operation
		// In production, this would be logged to the observability system
	}

	return result, err
}
