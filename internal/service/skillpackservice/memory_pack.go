package skillpackservice

import (
	"context"
	"errors"

	memorypackapp "github.com/markhuangai/dense-mem/internal/memorypack"
)

// memoryPackService is a single-hop compatibility wrapper for callers that
// still construct the former service package.
type memoryPackService struct {
	deps     MemoryPackDependencies
	delegate memorypackapp.MemoryPackService
}

var _ MemoryPackService = (*memoryPackService)(nil)

func NewMemoryPackService(deps MemoryPackDependencies) MemoryPackService {
	return &memoryPackService{deps: deps, delegate: memorypackapp.NewMemoryPackService(deps)}
}

func (s *memoryPackService) Export(ctx context.Context, req ExportRequest) (*ExportResult, error) {
	if s == nil || s.delegate == nil {
		return nil, errors.New("memory pack export: service is unavailable")
	}
	return s.delegate.Export(ctx, req)
}
