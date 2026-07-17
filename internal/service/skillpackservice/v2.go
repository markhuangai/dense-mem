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

var _ V2Service = (*v2Service)(nil)

func NewV2(deps V2Dependencies) V2Service {
	retain := defaultHistoryRetention
	if deps.HistoryDays > 0 {
		retain = time.Duration(deps.HistoryDays) * 24 * time.Hour
	}
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &v2Service{deps: deps, retain: retain, now: now}
}

func (s *v2Service) FindCandidatesV2(ctx context.Context, req V2FindCandidatesRequest) (*V2FindCandidatesResult, error) {
	actor, err := v2MemoryPackActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.Semantic == nil {
		return nil, errors.New("v2 memory pack candidates: semantic reader is required")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, errors.New("v2 memory pack candidates: query is required")
	}
	limit := clampLimit(req.Limit, 20, 100)
	graph, err := s.deps.Semantic.SemanticGraph(ctx, repository.V2SemanticGraphQuery{
		TeamID: actor.TeamID.String(),
		Query:  query,
		Types:  []string{"entity", "value"},
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	nodes := v2MemoryPackGraphNodes(graph.Nodes)
	out := &V2FindCandidatesResult{Candidates: []V2MemoryPackCandidate{}}
	for _, edge := range graph.Edges {
		if len(out.Candidates) >= limit {
			break
		}
		out.Candidates = append(out.Candidates, V2MemoryPackCandidate{
			RelationshipID:   edge.RelationshipID,
			PredicateKey:     edge.Relationship,
			Subject:          nodes[edge.Source],
			Object:           nodes[edge.Target],
			Tier:             edge.Tier,
			SupportCount:     edge.SupportCount,
			SourceGroupCount: edge.SourceGroupCount,
		})
	}
	return out, nil
}

func (s *v2Service) ExportV2(ctx context.Context, req V2ExportRequest) (*V2ExportResult, error) {
	actor, err := v2MemoryPackActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.Semantic == nil {
		return nil, errors.New("v2 memory pack export: semantic reader is required")
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("v2 memory pack export: name is required")
	}
	relationshipIDs := uniqueStrings(req.RelationshipIDs)
	if len(relationshipIDs) == 0 {
		return nil, errors.New("v2 memory pack export: relationship_ids is required")
	}
	includeSupport := req.IncludeSupport == nil || *req.IncludeSupport
	now := s.now().UTC()
	artifact := V2MemoryPackArtifact{
		Format:      V2MemoryPackFormat,
		PackID:      "pack_" + v2MemoryPackShortHash(strings.Join(relationshipIDs, "\x00")+now.Format(time.RFC3339Nano)),
		Name:        strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description),
		CreatedAt:   now.Format(time.RFC3339Nano),
		Source: V2MemoryPackSource{
			TeamID:     actor.TeamID.String(),
			ExportedBy: actor.ProfileID.String(),
		},
		Relationships: []V2MemoryPackRelationship{},
	}
	fragments := map[string]V2MemoryPackEvidenceFragment{}
	supports := []V2MemoryPackEvidenceSupport{}
	for _, relationshipID := range relationshipIDs {
		trace, err := s.deps.Semantic.TraceRelationship(ctx, repository.V2TraceRelationshipInput{
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
			return nil, fmt.Errorf("v2 memory pack export: relationship %s not found", relationshipID)
		}
		if trace.Relationship.Status != string(domain.V2RelationshipStatusActive) {
			return nil, fmt.Errorf("v2 memory pack export: relationship %s is not active", relationshipID)
		}
		item := v2MemoryPackRelationshipFromTrace(trace.Relationship)
		if includeSupport {
			for _, support := range trace.EvidenceSupports {
				if support.FragmentID != "" {
					item.SupportFragmentIDs = append(item.SupportFragmentIDs, support.FragmentID)
				}
				supports = append(supports, V2MemoryPackEvidenceSupport{
					RelationshipItemID: item.ItemID,
					FragmentID:         support.FragmentID,
					Quote:              support.Quote,
					SpanStart:          support.SpanStart,
					SpanEnd:            support.SpanEnd,
					Metadata:           v2MemoryPackCopyMap(support.Metadata),
				})
			}
			for _, fragment := range trace.EvidenceFragments {
				if fragment.FragmentID == "" {
					continue
				}
				fragments[fragment.FragmentID] = V2MemoryPackEvidenceFragment{
					FragmentID:       fragment.FragmentID,
					Content:          fragment.Content,
					ContentHash:      fragment.ContentHash,
					SourceType:       fragment.SourceType,
					Authority:        fragment.Authority,
					SourceRef:        fragment.SourceRef,
					SourceKey:        fragment.SourceKey,
					SourceRevisionID: fragment.SourceRevisionID,
					Labels:           append([]string(nil), fragment.Labels...),
					Metadata:         v2MemoryPackCopyMap(fragment.Metadata),
				}
			}
		}
		item.SupportFragmentIDs = uniqueStrings(item.SupportFragmentIDs)
		artifact.Relationships = append(artifact.Relationships, item)
	}
	if includeSupport {
		for _, id := range v2MemoryPackSortedKeys(fragments) {
			artifact.EvidenceFragments = append(artifact.EvidenceFragments, fragments[id])
		}
		artifact.EvidenceSupports = supports
	}
	canonical, hash, err := canonicalV2MemoryPackArtifact(artifact)
	if err != nil {
		return nil, err
	}
	artifact.ContentSHA256 = hash
	canonicalWithHash, err := marshalV2MemoryPackArtifact(artifact)
	if err != nil {
		return nil, err
	}
	return &V2ExportResult{
		Artifact:      artifact,
		CanonicalJSON: string(canonicalWithHash),
		SHA256:        hash,
		ItemCount:     len(artifact.Relationships),
		Filename:      skillPackFilename(artifact.Name),
		ContentType:   "application/json",
		Omissions:     v2MemoryPackSupportOmissions(includeSupport, canonical),
	}, nil
}

func (s *v2Service) InspectV2(ctx context.Context, req V2InspectRequest) (*V2InspectResult, error) {
	if _, err := v2MemoryPackActor(ctx); err != nil {
		return nil, err
	}
	loaded, err := s.loadV2Artifact(ctx, req.ArtifactJSON, req.URL, req.ExpectedSHA256)
	if err != nil {
		return nil, err
	}
	return inspectV2MemoryPack(loaded.artifact, loaded.hash, loaded.source), nil
}

func (s *v2Service) ImportV2(ctx context.Context, req V2ImportRequest) (*V2ImportResult, error) {
	actor, err := v2MemoryPackActor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Mode != ModeReview && req.Mode != ModeTrusted {
		return nil, errors.New("v2 memory pack import: mode must be review or trusted")
	}
	if s.deps.Remember == nil {
		return nil, errors.New("v2 memory pack import: remember service is required")
	}
	if s.deps.Ledger == nil {
		return nil, errors.New("v2 memory pack import: import ledger is required")
	}
	loaded, err := s.loadV2Artifact(ctx, req.ArtifactJSON, req.URL, req.ExpectedSHA256)
	if err != nil {
		return nil, err
	}
	inspection := inspectV2MemoryPack(loaded.artifact, loaded.hash, loaded.source)
	if err := validateV2MemoryPackImportSelections(loaded.artifact, req.SelectedItemIDs, req.ConflictDecisions); err != nil {
		return nil, err
	}
	selected := v2MemoryPackSelectedItemSet(req.SelectedItemIDs, loaded.artifact.Relationships)
	decisions := v2MemoryPackDecisionSet(req.ConflictDecisions)
	importID := v2MemoryPackImportID(actor.TeamID.String(), actor.ProfileID.String(), loaded.hash, req.Mode)
	if existing, err := s.deps.Ledger.GetImport(ctx, actor.TeamID.String(), importID); err == nil && existing != nil && existing.ImportID != "" {
		return v2ImportResultFromExisting(existing, loaded.hash, req.Mode), nil
	}

	now := s.now().UTC()
	record := domain.SkillPackImport{
		ImportID:           importID,
		TeamID:             actor.TeamID.String(),
		OwnerProfileID:     actor.ProfileID.String(),
		ArtifactHash:       loaded.hash,
		SourceURL:          loaded.source,
		SchemaVersion:      loaded.artifact.Format,
		Name:               loaded.artifact.Name,
		Mode:               req.Mode,
		Status:             domain.SkillPackImportStatusInspecting,
		ItemCount:          len(loaded.artifact.Relationships),
		RetentionExpiresAt: now.Add(s.retain),
		CreatedAt:          now,
		UpdatedAt:          now,
		Summary: map[string]any{
			"contract_version": domain.V2ContractVersion,
			"artifact_format":  loaded.artifact.Format,
			"owner_profile_id": actor.ProfileID.String(),
		},
	}
	if err := s.deps.Ledger.CreateImport(ctx, record); err != nil {
		return nil, err
	}

	rememberReq, itemResults := v2RememberRequestFromPack(importID, loaded, req.Mode, selected, decisions)
	if len(rememberReq.Evidence) == 0 {
		status := domain.SkillPackImportStatusNeedsReview
		summary := v2MemoryPackImportSummary(loaded, req.Mode, "", itemResults)
		if err := s.deps.Ledger.UpdateImportStatus(ctx, actor.TeamID.String(), importID, status, 0, len(loaded.artifact.Relationships), summary); err != nil {
			return nil, err
		}
		return &V2ImportResult{
			ImportID:     importID,
			ArtifactHash: loaded.hash,
			Mode:         req.Mode,
			Status:       status,
			SkippedCount: len(loaded.artifact.Relationships),
			Items:        itemResults,
		}, nil
	}
	remember, err := s.deps.Remember.RememberV2(ctx, rememberReq)
	if err != nil {
		status := domain.SkillPackImportStatusFailed
		summary := v2MemoryPackImportSummary(loaded, req.Mode, "", itemResults)
		summary["error"] = err.Error()
		_ = s.deps.Ledger.UpdateImportStatus(ctx, actor.TeamID.String(), importID, status, 0, len(itemResults), summary)
		return &V2ImportResult{ImportID: importID, ArtifactHash: loaded.hash, Mode: req.Mode, Status: status, Error: err.Error(), Items: itemResults}, nil
	}
	v2AttachPlacementItems(itemResults, remember)
	applied, skipped := v2MemoryPackImportCounts(itemResults)
	summary := v2MemoryPackImportSummary(loaded, req.Mode, remember.IngestID, itemResults)
	status := domain.SkillPackImportStatusApplied
	if applied == 0 {
		status = domain.SkillPackImportStatusNeedsReview
	}
	if err := s.deps.Ledger.UpdateImportStatus(ctx, actor.TeamID.String(), importID, status, applied, skipped, summary); err != nil {
		return &V2ImportResult{
			ImportID:     importID,
			ArtifactHash: loaded.hash,
			Mode:         req.Mode,
			Status:       "status_update_failed",
			IngestID:     remember.IngestID,
			Error:        err.Error(),
			Items:        itemResults,
		}, nil
	}
	if err := s.appendV2ImportChanges(ctx, actor.TeamID.String(), importID, remember.IngestID, itemResults); err != nil {
		return &V2ImportResult{
			ImportID:     importID,
			ArtifactHash: loaded.hash,
			Mode:         req.Mode,
			Status:       "change_ledger_failed",
			IngestID:     remember.IngestID,
			Error:        err.Error(),
			Items:        itemResults,
		}, nil
	}
	return &V2ImportResult{
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

func (s *v2Service) RollbackV2(ctx context.Context, req V2RollbackRequest) (*V2RollbackResult, error) {
	actor, err := v2MemoryPackActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.Ledger == nil {
		return nil, errors.New("v2 memory pack rollback: import ledger is required")
	}
	importID := strings.TrimSpace(req.ImportID)
	if importID == "" {
		return nil, errors.New("v2 memory pack rollback: import_id is required")
	}
	record, err := s.deps.Ledger.GetImport(ctx, actor.TeamID.String(), importID)
	if err != nil {
		return nil, err
	}
	if record.OwnerProfileID != "" && record.OwnerProfileID != actor.ProfileID.String() {
		return nil, errors.New("v2 memory pack rollback: import owner mismatch")
	}
	changes, err := s.deps.Ledger.ListChanges(ctx, actor.TeamID.String(), importID)
	if err != nil {
		return nil, err
	}
	conflicts := v2RollbackConflicts(record, changes)
	if len(conflicts) > 0 {
		return &V2RollbackResult{ImportID: importID, Status: "blocked", DryRun: true, Conflicts: conflicts}, nil
	}
	result := &V2RollbackResult{
		ImportID:      importID,
		Status:        "safe",
		DryRun:        true,
		ImpactSummary: "rollback can mark this V2 memory-pack import as rolled back; staged placement evidence remains append-only and semantic effects are not deleted",
	}
	if req.DryRun || !req.Confirm {
		return result, nil
	}
	if err := s.deps.Ledger.MarkRolledBack(ctx, actor.TeamID.String(), importID); err != nil {
		return nil, err
	}
	result.Status = domain.SkillPackImportStatusRolledBack
	result.DryRun = false
	result.RevertedCount = len(changes)
	return result, nil
}
