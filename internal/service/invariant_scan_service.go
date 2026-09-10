package service

// This compatibility facade preserves the historical invariant-scan API while
// the retired-operation policy lives in internal/operations.

import operations "github.com/markhuangai/dense-mem/internal/operations"

type InvariantFinding = operations.InvariantFinding
type InvariantScanResult = operations.InvariantScanResult
type InvariantScanService = operations.InvariantScanService

var ErrInvariantScanRemoved = operations.ErrInvariantScanRemoved

func NewInvariantScanService(repository any, auditSvc AuditService) InvariantScanService {
	return operations.NewInvariantScanService(repository, auditSvc)
}
