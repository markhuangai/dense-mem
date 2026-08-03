package skillpackservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

var _ MemoryPackService = (*memoryPackService)(nil)

func NewMemoryPackService(deps MemoryPackDependencies) MemoryPackService {
	retain := defaultHistoryRetention
	if deps.HistoryDays > 0 {
		retain = time.Duration(deps.HistoryDays) * 24 * time.Hour
	}
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &memoryPackService{deps: deps, retain: retain, now: now}
}

func (s *memoryPackService) FindCandidates(ctx context.Context, req FindCandidatesRequest) (*FindCandidatesResult, error) {
	actor, err := memoryPackActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.Semantic == nil {
		return nil, errors.New("memory pack candidates: semantic reader is required")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, errors.New("memory pack candidates: query is required")
	}
	limit := clampLimit(req.Limit, 20, 100)
	graph, err := s.deps.Semantic.SemanticGraph(ctx, repository.SemanticGraphQuery{
		TeamID: actor.TeamID.String(),
		Query:  query,
		Types:  []string{"entity", "value"},
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	nodes := memoryPackGraphNodes(graph.Nodes)
	out := &FindCandidatesResult{Candidates: []MemoryPackCandidate{}}
	for _, edge := range graph.Edges {
		if len(out.Candidates) >= limit {
			break
		}
		out.Candidates = append(out.Candidates, memoryPackCandidateFromEdge(edge, nodes))
	}
	return out, nil
}

func (s *memoryPackService) Export(ctx context.Context, req ExportRequest) (*ExportResult, error) {
	actor, err := memoryPackActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.Semantic == nil {
		return nil, errors.New("memory pack export: semantic reader is required")
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("memory pack export: name is required")
	}
	relationshipIDs := uniqueStrings(req.RelationshipIDs)
	if len(relationshipIDs) == 0 {
		return nil, errors.New("memory pack export: relationship_ids is required")
	}
	includeSupport := req.IncludeSupport == nil || *req.IncludeSupport
	now := s.now().UTC()
	artifact := MemoryPackArtifact{
		Format:      MemoryPackFormat,
		PackID:      "pack_" + memoryPackShortHash(strings.Join(relationshipIDs, "\x00")+now.Format(time.RFC3339Nano)),
		Name:        strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description),
		CreatedAt:   now.Format(time.RFC3339Nano),
		Source: MemoryPackSource{
			TeamID:     actor.TeamID.String(),
			ExportedBy: actor.ProfileID.String(),
		},
		Relationships: []MemoryPackRelationship{},
	}
	evidence := map[string]MemoryPackEvidence{}
	supports := []MemoryPackEvidenceSupport{}
	for _, relationshipID := range relationshipIDs {
		trace, err := s.deps.Semantic.TraceRelationship(ctx, repository.TraceRelationshipInput{
			TeamID:                  actor.TeamID.String(),
			RelationshipID:          relationshipID,
			IncludeEvidenceContent:  boolPtr(includeSupport),
			MaxEvents:               100,
			MaxFragmentContentRunes: 8000,
		})
		if err != nil {
			return nil, err
		}
		if trace.Relationship == nil {
			return nil, fmt.Errorf("memory pack export: relationship %s not found", relationshipID)
		}
		if trace.Relationship.Status != string(domain.RelationshipStatusActive) {
			return nil, fmt.Errorf("memory pack export: relationship %s is not active", relationshipID)
		}
		item := memoryPackRelationshipFromTrace(trace.Relationship)
		if includeSupport {
			for _, support := range trace.EvidenceSupports {
				if support.FragmentID != "" {
					item.SupportEvidenceIDs = append(item.SupportEvidenceIDs, support.FragmentID)
				}
				supports = append(supports, MemoryPackEvidenceSupport{
					RelationshipItemID: item.ItemID,
					EvidenceID:         support.FragmentID,
					Quote:              support.Quote,
					SpanStart:          support.SpanStart,
					SpanEnd:            support.SpanEnd,
					Metadata:           MemoryPackCopyMap(support.Metadata),
				})
			}
			for _, fragment := range trace.EvidenceFragments {
				if fragment.FragmentID == "" {
					continue
				}
				evidence[fragment.FragmentID] = MemoryPackEvidence{
					EvidenceID:       fragment.FragmentID,
					Content:          fragment.Content,
					ContentHash:      fragment.ContentHash,
					SourceType:       fragment.SourceType,
					Authority:        fragment.Authority,
					SourceRef:        fragment.SourceRef,
					SourceKey:        fragment.SourceKey,
					SourceRevisionID: fragment.SourceRevisionID,
					Labels:           append([]string(nil), fragment.Labels...),
					Metadata:         MemoryPackCopyMap(fragment.Metadata),
				}
			}
		}
		item.SupportEvidenceIDs = uniqueStrings(item.SupportEvidenceIDs)
		artifact.Relationships = append(artifact.Relationships, item)
	}
	if includeSupport {
		for _, id := range MemoryPackSortedEvidenceIDs(evidence) {
			artifact.Evidence = append(artifact.Evidence, evidence[id])
		}
		artifact.EvidenceSupports = supports
	}
	canonical, hash, err := canonicalMemoryPackArtifact(artifact)
	if err != nil {
		return nil, err
	}
	artifact.ContentSHA256 = hash
	canonicalWithHash, err := marshalMemoryPackArtifact(artifact)
	if err != nil {
		return nil, err
	}
	return &ExportResult{
		Artifact:      artifact,
		CanonicalJSON: string(canonicalWithHash),
		SHA256:        hash,
		ItemCount:     len(artifact.Relationships),
		Filename:      skillPackFilename(artifact.Name),
		ContentType:   "application/json",
		Omissions:     MemoryPackSupportOmissions(includeSupport, canonical),
	}, nil
}

func (s *memoryPackService) Inspect(ctx context.Context, req InspectRequest) (*InspectResult, error) {
	if _, err := memoryPackActor(ctx); err != nil {
		return nil, err
	}
	loaded, err := s.loadArtifact(ctx, req.ArtifactJSON, req.URL, req.ExpectedSHA256)
	if err != nil {
		return nil, err
	}
	return inspectMemoryPack(loaded.artifact, loaded.hash, loaded.format, loaded.source), nil
}

func (s *memoryPackService) Import(ctx context.Context, req ImportRequest) (*ImportResult, error) {
	actor, err := memoryPackActor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Mode != ModeReview && req.Mode != ModeTrusted {
		return nil, errors.New("memory pack import: mode must be review or trusted")
	}
	if s.deps.Remember == nil {
		return nil, errors.New("memory pack import: remember service is required")
	}
	if s.deps.Ledger == nil {
		return nil, errors.New("memory pack import: import ledger is required")
	}
	loaded, err := s.loadArtifact(ctx, req.ArtifactJSON, req.URL, req.ExpectedSHA256)
	if err != nil {
		return nil, err
	}
	inspection := inspectMemoryPack(loaded.artifact, loaded.hash, loaded.format, loaded.source)
	if err := validateMemoryPackImportSelections(loaded.artifact, req.SelectedItemIDs, req.ConflictDecisions); err != nil {
		return nil, err
	}
	selected := MemoryPackSelectedItemSet(req.SelectedItemIDs, loaded.artifact.Relationships)
	decisions := MemoryPackDecisionSet(req.ConflictDecisions)
	importID := MemoryPackImportID(actor.TeamID.String(), actor.ProfileID.String(), loaded.hash, req.Mode)
	if existing, err := s.deps.Ledger.GetImport(ctx, actor.TeamID.String(), importID); err == nil && existing != nil && existing.ImportID != "" {
		return importResultFromExisting(existing, loaded.hash, req.Mode), nil
	}

	now := s.now().UTC()
	record := domain.SkillPackImport{
		ImportID:           importID,
		TeamID:             actor.TeamID.String(),
		OwnerProfileID:     actor.ProfileID.String(),
		ArtifactHash:       loaded.hash,
		SourceURL:          loaded.source,
		SchemaVersion:      loaded.format,
		Name:               loaded.artifact.Name,
		Mode:               req.Mode,
		Status:             domain.SkillPackImportStatusInspecting,
		ItemCount:          len(loaded.artifact.Relationships),
		RetentionExpiresAt: now.Add(s.retain),
		CreatedAt:          now,
		UpdatedAt:          now,
		Summary: map[string]any{
			"contract_version": domain.ContractVersion,
			"artifact_format":  loaded.format,
			"owner_profile_id": actor.ProfileID.String(),
		},
	}
	if err := s.deps.Ledger.CreateImport(ctx, record); err != nil {
		return nil, err
	}

	rememberReq, itemResults, err := rememberRequestFromPack(importID, loaded, req.Mode, selected, decisions)
	if err != nil {
		summary := MemoryPackImportSummary(loaded, req.Mode, "", itemResults)
		summary["error"] = err.Error()
		if statusErr := s.deps.Ledger.UpdateImportStatus(ctx, actor.TeamID.String(), importID, domain.SkillPackImportStatusFailed, 0, len(itemResults), summary); statusErr != nil {
			return nil, errors.Join(err, statusErr)
		}
		return nil, err
	}
	if len(rememberReq.Evidence) == 0 {
		status := domain.SkillPackImportStatusNeedsReview
		summary := MemoryPackImportSummary(loaded, req.Mode, "", itemResults)
		if err := s.deps.Ledger.UpdateImportStatus(ctx, actor.TeamID.String(), importID, status, 0, len(loaded.artifact.Relationships), summary); err != nil {
			return nil, err
		}
		return &ImportResult{
			ImportID:     importID,
			ArtifactHash: loaded.hash,
			Mode:         req.Mode,
			Status:       status,
			SkippedCount: len(loaded.artifact.Relationships),
			Items:        itemResults,
		}, nil
	}
	remember, err := s.deps.Remember.Remember(ctx, rememberReq)
	if err != nil {
		status := domain.SkillPackImportStatusFailed
		summary := MemoryPackImportSummary(loaded, req.Mode, "", itemResults)
		summary["error"] = err.Error()
		_ = s.deps.Ledger.UpdateImportStatus(ctx, actor.TeamID.String(), importID, status, 0, len(itemResults), summary)
		return &ImportResult{ImportID: importID, ArtifactHash: loaded.hash, Mode: req.Mode, Status: status, Error: err.Error(), Items: itemResults}, nil
	}
	applied, skipped := MemoryPackImportCounts(itemResults)
	summary := MemoryPackImportSummary(loaded, req.Mode, remember.IngestID, itemResults)
	status := domain.SkillPackImportStatusApplied
	if applied == 0 {
		status = domain.SkillPackImportStatusNeedsReview
	}
	if err := s.deps.Ledger.UpdateImportStatus(ctx, actor.TeamID.String(), importID, status, applied, skipped, summary); err != nil {
		return &ImportResult{
			ImportID:     importID,
			ArtifactHash: loaded.hash,
			Mode:         req.Mode,
			Status:       "status_update_failed",
			IngestID:     remember.IngestID,
			Error:        err.Error(),
			Items:        itemResults,
		}, nil
	}
	if err := s.appendImportChanges(ctx, actor.TeamID.String(), importID, remember.IngestID, itemResults); err != nil {
		return &ImportResult{
			ImportID:     importID,
			ArtifactHash: loaded.hash,
			Mode:         req.Mode,
			Status:       "change_ledger_failed",
			IngestID:     remember.IngestID,
			Error:        err.Error(),
			Items:        itemResults,
		}, nil
	}
	return &ImportResult{
		ImportID:          importID,
		ArtifactHash:      loaded.hash,
		Mode:              req.Mode,
		Status:            status,
		IngestID:          remember.IngestID,
		CheckAfterSeconds: remember.CheckAfterSeconds,
		StatusTool:        remember.StatusTool,
		AppliedCount:      applied,
		SkippedCount:      skipped,
		Items:             itemResults,
		DecisionsRequired: inspection.DecisionsRequired,
	}, nil
}

func (s *memoryPackService) Rollback(ctx context.Context, req RollbackRequest) (*RollbackResult, error) {
	actor, err := memoryPackActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.Ledger == nil {
		return nil, errors.New("memory pack rollback: import ledger is required")
	}
	importID := strings.TrimSpace(req.ImportID)
	if importID == "" {
		return nil, errors.New("memory pack rollback: import_id is required")
	}
	record, err := s.deps.Ledger.GetImport(ctx, actor.TeamID.String(), importID)
	if err != nil {
		return nil, err
	}
	if record.OwnerProfileID != "" && record.OwnerProfileID != actor.ProfileID.String() {
		return nil, errors.New("memory pack rollback: import owner mismatch")
	}
	changes, err := s.deps.Ledger.ListChanges(ctx, actor.TeamID.String(), importID)
	if err != nil {
		return nil, err
	}
	conflicts := rollbackConflicts(record, changes)
	if len(conflicts) > 0 {
		return &RollbackResult{ImportID: importID, Status: "blocked", DryRun: true, Conflicts: conflicts}, nil
	}
	impactToken := rollbackImpactToken(record, changes)
	result := &RollbackResult{
		ImportID:      importID,
		Status:        "safe",
		DryRun:        true,
		ImpactSummary: "rollback can mark this memory-pack import as rolled back; staged placement evidence remains append-only and semantic effects are not deleted",
		ImpactToken:   impactToken,
	}
	if req.DryRun || !req.Confirm {
		return result, nil
	}
	if strings.TrimSpace(req.ImpactToken) != impactToken {
		return nil, errors.New("memory pack rollback: impact_token does not match current import state")
	}
	if err := s.deps.Ledger.MarkRolledBack(ctx, actor.TeamID.String(), importID); err != nil {
		return nil, err
	}
	result.Status = domain.SkillPackImportStatusRolledBack
	result.DryRun = false
	result.RevertedCount = len(changes)
	return result, nil
}
