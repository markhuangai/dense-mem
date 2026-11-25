package memorypack

import (
	"context"
	"errors"
	"github.com/markhuangai/dense-mem/internal/observability"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"time"
)

const (
	MemoryPackFormat     = "dense-mem.memory-pack.v3.0"
	MemoryPackSourceType = "memory_pack"
	MemoryPackLabel      = "memory_pack_export"
)

var ErrMemoryPackAuthContext = errors.New("memory pack: authenticated actor context is required")

type MemoryPackService interface {
	Export(context.Context, ExportRequest) (*ExportResult, error)
}
type MemoryPackDependencies struct {
	Semantic MemoryPackSemanticReader
	Now      func() time.Time
	Logger   observability.LogProvider
}
type MemoryPackSemanticReader interface {
	TraceEvidence(context.Context, tracecontract.Input) (*tracecontract.EvidenceTraceResult, error)
}
type memoryPackService struct {
	deps MemoryPackDependencies
	now  func() time.Time
}
type ExportRequest struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	EvidenceIDs     []string `json:"evidence_ids"`
	IncludeEvidence *bool    `json:"include_evidence,omitempty"`
}
type ExportResult struct {
	Artifact      MemoryPackArtifact `json:"artifact"`
	CanonicalJSON string             `json:"canonical_json"`
	SHA256        string             `json:"sha256"`
	ItemCount     int                `json:"item_count"`
	Filename      string             `json:"filename"`
	ContentType   string             `json:"content_type"`
	Omissions     []string           `json:"omissions"`
}
type MemoryPackArtifact struct {
	Format        string               `json:"format"`
	PackID        string               `json:"pack_id"`
	Name          string               `json:"name"`
	Description   string               `json:"description,omitempty"`
	CreatedAt     string               `json:"created_at"`
	Source        MemoryPackSource     `json:"source"`
	Evidence      []MemoryPackEvidence `json:"evidence"`
	Omissions     []string             `json:"omissions"`
	ContentSHA256 string               `json:"content_sha256,omitempty"`
}
type MemoryPackSource struct {
	InstallationID string `json:"installation_id,omitempty"`
	TeamID         string `json:"team_id"`
	ExportedBy     string `json:"exported_by"`
}
type MemoryPackEvidence struct {
	tracecontract.TraceEvidenceFragment
	Occurrences     []tracecontract.TraceEvidenceFragment       `json:"occurrences"`
	SourceRevisions []tracecontract.TraceSourceRevision         `json:"source_revisions"`
	Decisions       []tracecontract.TraceEvidenceDecision       `json:"decisions"`
	Lifecycle       []tracecontract.TraceEvidenceLifecycleEvent `json:"lifecycle"`
	Omissions       []string                                    `json:"omissions"`
}
