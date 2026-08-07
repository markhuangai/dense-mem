package skillpackservice

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	MemoryPackFormat     = "dense-mem.memory-pack.v2.4"
	MemoryPackSourceType = "memory_pack"
	MemoryPackLabel      = "memory_pack_export"
)

var ErrMemoryPackAuthContext = errors.New("memory pack: authenticated actor context is required")

// MemoryPackService exposes the intentionally narrow export-only memory-pack workflow.
type MemoryPackService interface {
	Export(ctx context.Context, req ExportRequest) (*ExportResult, error)
}

type MemoryPackDependencies struct {
	Semantic MemoryPackSemanticReader
	Now      func() time.Time
}

type MemoryPackSemanticReader interface {
	TraceRelationship(ctx context.Context, input repository.TraceRelationshipInput) (*repository.RelationshipTraceResult, error)
}

type memoryPackService struct {
	deps MemoryPackDependencies
	now  func() time.Time
}

type ExportRequest struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	RelationshipIDs []string `json:"relationship_ids,omitempty"`
	IncludeSupport  *bool    `json:"include_evidence,omitempty"`
}

type ExportResult struct {
	Artifact      MemoryPackArtifact `json:"artifact"`
	CanonicalJSON string             `json:"canonical_json"`
	SHA256        string             `json:"sha256"`
	ItemCount     int                `json:"item_count"`
	Filename      string             `json:"filename"`
	ContentType   string             `json:"content_type"`
	Omissions     []string           `json:"omissions,omitempty"`
}

type MemoryPackArtifact struct {
	Format           string                      `json:"format"`
	PackID           string                      `json:"pack_id"`
	Name             string                      `json:"name"`
	Description      string                      `json:"description,omitempty"`
	CreatedAt        string                      `json:"created_at"`
	Source           MemoryPackSource            `json:"source"`
	Relationships    []MemoryPackRelationship    `json:"relationships"`
	Evidence         []MemoryPackEvidence        `json:"evidence,omitempty"`
	EvidenceSupports []MemoryPackEvidenceSupport `json:"evidence_supports,omitempty"`
	Extensions       map[string]any              `json:"extensions,omitempty"`
	ContentSHA256    string                      `json:"content_sha256,omitempty"`
}

type MemoryPackSource struct {
	InstallationID string `json:"installation_id,omitempty"`
	TeamID         string `json:"team_id,omitempty"`
	ExportedBy     string `json:"exported_by,omitempty"`
}

type MemoryPackRelationship struct {
	ItemID                    string             `json:"item_id"`
	SourceRelationshipID      string             `json:"source_relationship_id,omitempty"`
	SourceRelationshipVersion int                `json:"source_relationship_version,omitempty"`
	SourceOwnerProfileID      string             `json:"source_owner_profile_id,omitempty"`
	Subject                   MemoryPackEndpoint `json:"subject"`
	PredicateKey              string             `json:"predicate_key"`
	PredicateVersion          int                `json:"predicate_version"`
	Object                    MemoryPackEndpoint `json:"object"`
	Polarity                  string             `json:"polarity,omitempty"`
	ScopeKey                  string             `json:"scope_key,omitempty"`
	Status                    string             `json:"status,omitempty"`
	SupportEvidenceIDs        []string           `json:"support_evidence_ids,omitempty"`
	Metadata                  map[string]any     `json:"metadata,omitempty"`
}

type MemoryPackEndpoint struct {
	Ref         string `json:"ref"`
	Kind        string `json:"kind"`
	SourceID    string `json:"source_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	ValueType   string `json:"value_type,omitempty"`
	Value       string `json:"value,omitempty"`
}

type MemoryPackEvidence struct {
	EvidenceID       string         `json:"evidence_id"`
	Content          string         `json:"content"`
	ContentHash      string         `json:"content_hash,omitempty"`
	SourceType       string         `json:"source_type,omitempty"`
	Authority        string         `json:"authority,omitempty"`
	SourceRef        string         `json:"source_ref,omitempty"`
	SourceKey        string         `json:"source_key,omitempty"`
	SourceRevisionID string         `json:"source_revision_id,omitempty"`
	Labels           []string       `json:"labels,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type MemoryPackEvidenceSupport struct {
	RelationshipItemID string         `json:"relationship_item_id"`
	EvidenceID         string         `json:"evidence_id"`
	Quote              string         `json:"quote,omitempty"`
	SpanStart          int            `json:"span_start,omitempty"`
	SpanEnd            int            `json:"span_end,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}
