package contract

import (
	"context"
	"time"

	audit "github.com/markhuangai/dense-mem/internal/audit/contract"
)

const MaxPreviewExamples = 10

type ManagementEdit struct {
	Action          string         `json:"action"`
	RecordID        string         `json:"record_id,omitempty"`
	ExpectedVersion int64          `json:"expected_version"`
	Kind            Kind           `json:"kind,omitempty"`
	Definition      *Definition    `json:"definition,omitempty"`
	DefinitionID    string         `json:"definition_id,omitempty"`
	Source          *SourceHandle  `json:"source,omitempty"`
	Members         []SourceHandle `json:"members,omitempty"`
}

type SaveDraftInput struct {
	OperationKey     string           `json:"operation_key"`
	ExpectedVersion  int64            `json:"expected_version"`
	ExpectedRevision int64            `json:"expected_revision"`
	Reason           string           `json:"reason"`
	Edits            []ManagementEdit `json:"edits"`
}

type DraftCommand struct {
	OperationKey     string `json:"operation_key"`
	ExpectedVersion  int64  `json:"expected_version"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}

type ManagementDraft struct {
	ID           string             `json:"id"`
	Version      int64              `json:"version"`
	State        string             `json:"state"`
	BaseRevision int64              `json:"base_revision"`
	Reason       string             `json:"reason"`
	Edits        []ManagementEdit   `json:"edits"`
	Publication  *PublicationResult `json:"publication,omitempty"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

type DraftPage struct {
	Drafts     []ManagementDraft `json:"drafts"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type ManagementContext struct {
	DraftID  string
	Revision int64
	Records  map[string]Record
	Sources  map[string]SourceSnapshot
}

type ManagementSource struct {
	SourceHandle
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	OwnerID   string `json:"owner_id,omitempty"`
}

type ManagementSourcePage struct {
	Sources    []ManagementSource `json:"sources"`
	NextCursor string             `json:"next_cursor,omitempty"`
	Revision   int64              `json:"revision"`
}

type ManagementRecordPage struct {
	Records    []RecordView `json:"records"`
	NextCursor string       `json:"next_cursor,omitempty"`
	Revision   int64        `json:"revision"`
}

type ImpactPreview struct {
	DraftID              string             `json:"draft_id"`
	DraftVersion         int64              `json:"draft_version"`
	Revision             int64              `json:"revision"`
	ChangedRecords       int                `json:"changed_records"`
	AffectedRecords      int64              `json:"affected_records"`
	AffectedSources      int64              `json:"affected_sources"`
	RequiresReassessment bool               `json:"requires_reassessment"`
	Examples             []ManagementSource `json:"examples"`
	ExamplesTruncated    bool               `json:"examples_truncated"`
	Complete             bool               `json:"complete"`
}

type TeamOntologyStatus struct {
	ObservedAt        time.Time         `json:"observed_at"`
	Revision          int64             `json:"revision"`
	DiscoveryComplete bool              `json:"discovery_complete"`
	CoverageComplete  bool              `json:"coverage_complete"`
	PendingMarkers    int64             `json:"pending_markers"`
	Counts            MaintenanceCounts `json:"counts"`
}

type ManagementPublication struct {
	PublicationResult
	Origin      string       `json:"origin"`
	ActorID     string       `json:"actor_id"`
	Reason      string       `json:"reason"`
	CreatedAt   time.Time    `json:"created_at"`
	RollbackOf  string       `json:"rollback_of,omitempty"`
	RecordsView []RecordView `json:"record_views,omitempty"`
}

type ManagementPublicationPage struct {
	Publications []ManagementPublication `json:"publications"`
	NextRevision int64                   `json:"next_revision,omitempty"`
}

type ManagementAuditEvent struct {
	Operation string
	EntityID  string
	Reason    string
	Version   int64
	Revision  int64
}

type ManagementAudit func(ManagementAuditEvent) (audit.Entry, error)
type ManagementPrepare func(ManagementContext) (Publication, error)

type ManagementRepository interface {
	GetRecord(context.Context, string, string, int64) (RecordView, error)
	ListRecords(context.Context, string, Kind, string, int) (Page, error)
	ListManagementSources(context.Context, string, SourceKind, string, string, int) (ManagementSourcePage, error)
	TeamOntologyStatus(context.Context, string) (TeamOntologyStatus, error)
	SaveManagementDraft(context.Context, string, string, SaveDraftInput, ManagementPrepare, ManagementAudit) (ManagementDraft, error)
	GetManagementDraft(context.Context, string, string) (ManagementDraft, error)
	ListManagementDrafts(context.Context, string, string, int) (DraftPage, error)
	PreviewManagementDraft(context.Context, string, string, int64) (ImpactPreview, error)
	CommandManagementDraft(context.Context, string, string, string, DraftCommand, ManagementAudit) (ManagementDraft, error)
	ListManagementPublications(context.Context, string, int64, int) (ManagementPublicationPage, error)
	GetManagementPublication(context.Context, string, string) (ManagementPublication, error)
	RollbackManagementPublication(context.Context, string, string, DraftCommand, ManagementAudit) (PublicationResult, error)
}
