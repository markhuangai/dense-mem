package ontology

import (
	"context"
	"slices"

	"github.com/google/uuid"
	audit "github.com/markhuangai/dense-mem/internal/audit/contract"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	access "github.com/markhuangai/dense-mem/internal/service/access"
)

type ManagementEdit = contract.ManagementEdit
type SaveDraftInput = contract.SaveDraftInput
type DraftCommand = contract.DraftCommand
type Kind = contract.Kind
type SourceKind = contract.SourceKind

const MaxPageSize = contract.MaxPageSize
const EvidenceSource = contract.EvidenceSource

var (
	ErrInvalid      = contract.ErrInvalid
	ErrUnauthorized = contract.ErrUnauthorized
	ErrNotFound     = contract.ErrNotFound
	ErrConflict     = contract.ErrConflict
	ErrSourceStale  = contract.ErrSourceStale
	ErrOverride     = contract.ErrOverride
)

type ManagementService struct {
	repository contract.ManagementRepository
	audit      MaintenanceAuditPreparer
}

type managementAuditContextKey struct{}
type managementAuditContext struct{ clientIP, correlationID string }

func WithManagementAuditContext(ctx context.Context, clientIP, correlationID string) context.Context {
	return context.WithValue(ctx, managementAuditContextKey{}, managementAuditContext{clientIP, correlationID})
}

func NewManagementService(repository contract.ManagementRepository, audit MaintenanceAuditPreparer) *ManagementService {
	return &ManagementService{repository: repository, audit: audit}
}

func (s *ManagementService) actor(ctx context.Context, write bool) (requestctx.Actor, error) {
	actor, ok := requestctx.ActorFromContext(ctx)
	grant := "read"
	if write {
		grant = "write"
	}
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil || actor.Role != "manager" || !slices.Contains(actor.Grants, grant) {
		return actor, contract.ErrUnauthorized
	}
	if s == nil || s.repository == nil {
		return actor, &OrganizationError{Code: "configuration_invalid"}
	}
	if write && s.audit == nil {
		return actor, &OrganizationError{Code: "audit_unavailable"}
	}
	return actor, nil
}

func (s *ManagementService) prepareAudit(ctx context.Context, actor requestctx.Actor) contract.ManagementAudit {
	return func(event contract.ManagementAuditEvent) (audit.Entry, error) {
		team := actor.TeamID.String()
		metadata, _ := ctx.Value(managementAuditContextKey{}).(managementAuditContext)
		ownerID := actor.OwnerID.String()
		entity := "ontology_draft"
		if event.Operation == "ONTOLOGY_PUBLISH" || event.Operation == "ONTOLOGY_ROLLBACK" {
			entity = "ontology_publication"
		}
		return s.audit.Prepare(ctx, access.AuditLogEntry{ProfileID: &team, Operation: event.Operation, EntityType: entity, EntityID: event.EntityID,
			ActorKeyID: &ownerID, ActorRole: actor.Role, ClientIP: metadata.clientIP, CorrelationID: metadata.correlationID,
			Metadata: map[string]any{"reason": event.Reason, "version": event.Version, "revision": event.Revision}})
	}
}

func (s *ManagementService) Records(ctx context.Context, kind contract.Kind, cursor string, limit int) (contract.ManagementRecordPage, error) {
	actor, err := s.actor(ctx, false)
	if err != nil {
		return contract.ManagementRecordPage{}, err
	}
	if kind != "" && !slices.Contains([]contract.Kind{contract.EntityClass, contract.PredicateConcept, contract.Topic, contract.AssignmentKind, contract.EvidenceGroup, contract.RelationshipGroup, contract.OverrideKind}, kind) {
		return contract.ManagementRecordPage{}, contract.ErrInvalid
	}
	page, err := s.repository.ListRecords(ctx, actor.TeamID.String(), kind, cursor, limit)
	return contract.ManagementRecordPage{Records: page.Records, NextCursor: page.NextID, Revision: page.Revision}, err
}

func (s *ManagementService) Record(ctx context.Context, id string, version int64) (contract.RecordView, error) {
	actor, err := s.actor(ctx, false)
	if err != nil {
		return contract.RecordView{}, err
	}
	return s.repository.GetRecord(ctx, actor.TeamID.String(), id, version)
}

func (s *ManagementService) Sources(ctx context.Context, kind contract.SourceKind, cursor, query string, limit int) (contract.ManagementSourcePage, error) {
	actor, err := s.actor(ctx, false)
	if err != nil {
		return contract.ManagementSourcePage{}, err
	}
	return s.repository.ListManagementSources(ctx, actor.TeamID.String(), kind, cursor, query, limit)
}

func (s *ManagementService) Status(ctx context.Context) (contract.TeamOntologyStatus, error) {
	actor, err := s.actor(ctx, false)
	if err != nil {
		return contract.TeamOntologyStatus{}, err
	}
	return s.repository.TeamOntologyStatus(ctx, actor.TeamID.String())
}

func (s *ManagementService) SaveDraft(ctx context.Context, id string, input contract.SaveDraftInput) (contract.ManagementDraft, error) {
	actor, err := s.actor(ctx, true)
	if err != nil {
		return contract.ManagementDraft{}, err
	}
	if err := contract.ValidateSaveDraft(input); err != nil {
		return contract.ManagementDraft{}, err
	}
	return s.repository.SaveManagementDraft(ctx, actor.TeamID.String(), id, input, func(current contract.ManagementContext) (contract.Publication, error) {
		return contract.CompileManagementEdits(input, current)
	}, s.prepareAudit(ctx, actor))
}

func (s *ManagementService) Draft(ctx context.Context, id string) (contract.ManagementDraft, error) {
	actor, err := s.actor(ctx, false)
	if err != nil {
		return contract.ManagementDraft{}, err
	}
	return s.repository.GetManagementDraft(ctx, actor.TeamID.String(), id)
}

func (s *ManagementService) Drafts(ctx context.Context, cursor string, limit int) (contract.DraftPage, error) {
	actor, err := s.actor(ctx, false)
	if err != nil {
		return contract.DraftPage{}, err
	}
	return s.repository.ListManagementDrafts(ctx, actor.TeamID.String(), cursor, limit)
}

func (s *ManagementService) Preview(ctx context.Context, id string, version int64) (contract.ImpactPreview, error) {
	actor, err := s.actor(ctx, false)
	if err != nil {
		return contract.ImpactPreview{}, err
	}
	if version < 1 {
		return contract.ImpactPreview{}, contract.ErrInvalid
	}
	return s.repository.PreviewManagementDraft(ctx, actor.TeamID.String(), id, version)
}

func (s *ManagementService) CommandDraft(ctx context.Context, id, action string, input contract.DraftCommand) (contract.ManagementDraft, error) {
	actor, err := s.actor(ctx, true)
	if err != nil {
		return contract.ManagementDraft{}, err
	}
	if action != "publish" && action != "discard" {
		return contract.ManagementDraft{}, contract.ErrInvalid
	}
	if err := contract.ValidateOperation(input.OperationKey, input.ExpectedRevision, input.Reason); err != nil {
		return contract.ManagementDraft{}, err
	}
	if input.ExpectedVersion < 1 {
		return contract.ManagementDraft{}, contract.ErrInvalid
	}
	return s.repository.CommandManagementDraft(ctx, actor.TeamID.String(), id, action, input, s.prepareAudit(ctx, actor))
}

func (s *ManagementService) Publications(ctx context.Context, after int64, limit int) (contract.ManagementPublicationPage, error) {
	actor, err := s.actor(ctx, false)
	if err != nil {
		return contract.ManagementPublicationPage{}, err
	}
	return s.repository.ListManagementPublications(ctx, actor.TeamID.String(), after, limit)
}

func (s *ManagementService) Publication(ctx context.Context, id string) (contract.ManagementPublication, error) {
	actor, err := s.actor(ctx, false)
	if err != nil {
		return contract.ManagementPublication{}, err
	}
	return s.repository.GetManagementPublication(ctx, actor.TeamID.String(), id)
}

func (s *ManagementService) Rollback(ctx context.Context, id string, input contract.DraftCommand) (contract.PublicationResult, error) {
	actor, err := s.actor(ctx, true)
	if err != nil {
		return contract.PublicationResult{}, err
	}
	if err := contract.ValidateOperation(input.OperationKey, input.ExpectedRevision, input.Reason); err != nil {
		return contract.PublicationResult{}, err
	}
	return s.repository.RollbackManagementPublication(ctx, actor.TeamID.String(), id, input, s.prepareAudit(ctx, actor))
}
