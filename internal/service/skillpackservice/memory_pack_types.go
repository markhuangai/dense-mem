// Package skillpackservice preserves the historical memory-pack application
// import path while the implementation lives in internal/memorypack.
package skillpackservice

import memorypackapp "github.com/markhuangai/dense-mem/internal/memorypack"

const (
	MemoryPackFormat     = memorypackapp.MemoryPackFormat
	MemoryPackSourceType = memorypackapp.MemoryPackSourceType
	MemoryPackLabel      = memorypackapp.MemoryPackLabel
)

var (
	ErrMemoryPackAuthContext           = memorypackapp.ErrMemoryPackAuthContext
	ErrMemoryPackRelationshipNotActive = memorypackapp.ErrMemoryPackRelationshipNotActive
)

type (
	MemoryPackService         = memorypackapp.MemoryPackService
	MemoryPackDependencies    = memorypackapp.MemoryPackDependencies
	MemoryPackSemanticReader  = memorypackapp.MemoryPackSemanticReader
	ExportRequest             = memorypackapp.ExportRequest
	ExportResult              = memorypackapp.ExportResult
	MemoryPackArtifact        = memorypackapp.MemoryPackArtifact
	MemoryPackSource          = memorypackapp.MemoryPackSource
	MemoryPackRelationship    = memorypackapp.MemoryPackRelationship
	MemoryPackEndpoint        = memorypackapp.MemoryPackEndpoint
	MemoryPackEvidence        = memorypackapp.MemoryPackEvidence
	MemoryPackEvidenceSupport = memorypackapp.MemoryPackEvidenceSupport
)
