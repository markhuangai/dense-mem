package skillpackservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func v2RememberRequestFromPack(importID string, loaded v2LoadedArtifact, mode string, selected map[string]bool, decisions map[string]string) (memoryservice.V2RememberRequest, []V2ImportItemResult) {
	fragmentByID := v2MemoryPackFragmentsByID(loaded.artifact)
	evidence := []memoryservice.V2RememberEvidenceInput{}
	relationshipHints := []map[string]any{}
	results := make([]V2ImportItemResult, 0, len(loaded.artifact.Relationships))
	for _, item := range loaded.artifact.Relationships {
		result := V2ImportItemResult{
			ItemID:               item.ItemID,
			SourceRelationshipID: item.SourceRelationshipID,
			EvidenceIndex:        -1,
		}
		if !selected[item.ItemID] {
			result.Status = "skipped"
			result.Decision = DecisionSkip
			results = append(results, result)
			continue
		}
		if decisions[item.ItemID] == DecisionSkip {
			result.Status = "skipped"
			result.Decision = DecisionSkip
			results = append(results, result)
			continue
		}
		evidenceIndex := len(evidence)
		result.EvidenceIndex = evidenceIndex
		result.Status = "staged"
		content := v2MemoryPackEvidenceContent(loaded.artifact, item, fragmentByID)
		evidence = append(evidence, memoryservice.V2RememberEvidenceInput{
			Content:        content,
			SourceType:     v2MemoryPackSourceType,
			Source:         v2MemoryPackSourceRef(loaded),
			Authority:      v2MemoryPackAuthority(mode),
			SourceKey:      "memory_pack:" + loaded.hash,
			SourceRevision: loaded.hash,
			IdempotencyKey: importID + ":" + item.ItemID,
			Labels:         []string{v2MemoryPackLabel, "v2"},
			Metadata: map[string]any{
				"memory_pack_import_id":            importID,
				"memory_pack_item_id":              item.ItemID,
				"memory_pack_hash":                 loaded.hash,
				"memory_pack_mode":                 mode,
				"source_relationship_id":           item.SourceRelationshipID,
				"source_relationship_version":      item.SourceRelationshipVersion,
				"source_owner_profile_id":          item.SourceOwnerProfileID,
				"source_team_id":                   loaded.artifact.Source.TeamID,
				"source_author_is_provenance_only": true,
				"trusted_mode_forces_status":       false,
			},
		})
		relationshipHints = append(relationshipHints, v2MemoryPackRelationshipHint(item, evidenceIndex))
		results = append(results, result)
	}
	return memoryservice.V2RememberRequest{
		ContractVersion:   domain.V2ContractVersion,
		Evidence:          evidence,
		RelationshipHints: relationshipHints,
		IdempotencyKey:    "memory-pack:" + importID,
	}, results
}

func (s *v2Service) appendV2ImportChanges(ctx context.Context, teamID, importID, ingestID string, items []V2ImportItemResult) error {
	if ingestID == "" {
		return nil
	}
	now := s.now().UTC()
	if err := s.deps.Ledger.AppendChange(ctx, domain.SkillPackImportChange{
		ChangeID:   uuid.NewString(),
		ImportID:   importID,
		TeamID:     teamID,
		EntityType: "v2_ingest",
		EntityID:   ingestID,
		Action:     domain.SkillPackChangeActionLinked,
		AfterState: map[string]any{
			"ingest_id": ingestID,
		},
		CreatedAt: now,
	}); err != nil {
		return err
	}
	for _, item := range items {
		if item.PlacementItemID == "" {
			continue
		}
		if err := s.deps.Ledger.AppendChange(ctx, domain.SkillPackImportChange{
			ChangeID:   uuid.NewString(),
			ImportID:   importID,
			TeamID:     teamID,
			EntityType: "v2_placement_item",
			EntityID:   item.PlacementItemID,
			Action:     domain.SkillPackChangeActionLinked,
			AfterState: map[string]any{
				"ingest_id":              ingestID,
				"placement_item_id":      item.PlacementItemID,
				"memory_pack_item_id":    item.ItemID,
				"source_relationship_id": item.SourceRelationshipID,
				"evidence_index":         item.EvidenceIndex,
			},
			CreatedAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func v2AttachPlacementItems(items []V2ImportItemResult, remember *memoryservice.V2RememberResult) {
	if remember == nil {
		return
	}
	byEvidence := map[int]memoryservice.V2RememberItemResult{}
	for _, item := range remember.Items {
		byEvidence[item.EvidenceIndex] = item
	}
	for i := range items {
		if items[i].EvidenceIndex < 0 {
			continue
		}
		placement, ok := byEvidence[items[i].EvidenceIndex]
		if !ok {
			continue
		}
		items[i].PlacementItemID = placement.ItemID
	}
}

func v2MemoryPackImportSummary(loaded v2LoadedArtifact, mode string, ingestID string, items []V2ImportItemResult) map[string]any {
	out := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"artifact_format":  loaded.artifact.Format,
		"artifact_hash":    loaded.hash,
		"mode":             mode,
		"source_url":       loaded.source,
		"legacy_artifact":  loaded.legacy,
		"items":            items,
	}
	if ingestID != "" {
		out["ingest_id"] = ingestID
	}
	return out
}

func v2ImportResultFromExisting(record *domain.SkillPackImport, hash string, mode string) *V2ImportResult {
	result := &V2ImportResult{
		ImportID:     record.ImportID,
		ArtifactHash: hash,
		Mode:         mode,
		Status:       record.Status,
		IngestID:     record.IngestID,
		AppliedCount: record.AppliedCount,
		SkippedCount: record.SkippedCount,
	}
	if ingestID, _ := record.Summary["ingest_id"].(string); result.IngestID == "" {
		result.IngestID = ingestID
	}
	return result
}

func v2MemoryPackImportCounts(items []V2ImportItemResult) (int, int) {
	applied := 0
	skipped := 0
	for _, item := range items {
		if item.Status == "skipped" {
			skipped++
		} else {
			applied++
		}
	}
	return applied, skipped
}

func v2MemoryPackSelectedItemSet(selected []string, items []V2MemoryPackRelationship) map[string]bool {
	out := map[string]bool{}
	if len(selected) == 0 {
		for _, item := range items {
			out[item.ItemID] = true
		}
		return out
	}
	for _, value := range uniqueStrings(selected) {
		out[value] = true
	}
	return out
}

func validateV2MemoryPackImportSelections(artifact V2MemoryPackArtifact, selected []string, decisions []V2ImportItemDecision) error {
	items := map[string]struct{}{}
	for _, item := range artifact.Relationships {
		items[item.ItemID] = struct{}{}
	}
	for _, itemID := range uniqueStrings(selected) {
		if _, ok := items[itemID]; !ok {
			return fmt.Errorf("v2 memory pack import: selected item %q is not present in artifact", itemID)
		}
	}
	for _, decision := range decisions {
		itemID := strings.TrimSpace(decision.ItemID)
		if itemID == "" {
			return errors.New("v2 memory pack import: conflict decision item_id is required")
		}
		if _, ok := items[itemID]; !ok {
			return fmt.Errorf("v2 memory pack import: conflict decision item %q is not present in artifact", itemID)
		}
		switch strings.TrimSpace(decision.Action) {
		case DecisionSkip, DecisionImportAnyway, "import_for_review":
		default:
			return fmt.Errorf("v2 memory pack import: unsupported conflict decision action %q", decision.Action)
		}
	}
	return nil
}

func v2MemoryPackDecisionSet(decisions []V2ImportItemDecision) map[string]string {
	out := map[string]string{}
	for _, decision := range decisions {
		itemID := strings.TrimSpace(decision.ItemID)
		action := strings.TrimSpace(decision.Action)
		if itemID != "" && action != "" {
			out[itemID] = action
		}
	}
	return out
}

func v2RollbackConflicts(record *domain.SkillPackImport, changes []domain.SkillPackImportChange) []string {
	if record.Status == domain.SkillPackImportStatusRolledBack {
		return []string{"import is already rolled back"}
	}
	for _, change := range changes {
		if change.EntityType == "relationship" || change.EntityType == "fact" || change.EntityType == "claim" {
			return []string{"import has semantic graph effects; rollback requires lifecycle dependency checks"}
		}
	}
	return nil
}

func v2MemoryPackEvidenceContent(artifact V2MemoryPackArtifact, item V2MemoryPackRelationship, fragments map[string]V2MemoryPackEvidenceFragment) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "Memory pack %q proposes a relationship: %s %s %s.", artifact.Name, item.Subject.DisplayName, item.PredicateKey, v2MemoryPackEndpointText(item.Object))
	if item.SourceRelationshipID != "" {
		_, _ = fmt.Fprintf(&b, "\nSource relationship %s version %d is provenance only.", item.SourceRelationshipID, item.SourceRelationshipVersion)
	}
	for _, fragmentID := range item.SupportFragmentIDs {
		fragment, ok := fragments[fragmentID]
		if !ok || strings.TrimSpace(fragment.Content) == "" {
			continue
		}
		_, _ = fmt.Fprintf(&b, "\nSupport fragment %s: %s", fragmentID, fragment.Content)
	}
	return b.String()
}

func v2MemoryPackRelationshipHint(item V2MemoryPackRelationship, evidenceIndex int) map[string]any {
	hint := map[string]any{
		"ref":                         item.ItemID,
		"subject_ref":                 item.Subject.Ref,
		"subject_name":                item.Subject.DisplayName,
		"predicate":                   item.PredicateKey,
		"predicate_version":           item.PredicateVersion,
		"source_relationship_id":      item.SourceRelationshipID,
		"source_relationship_version": item.SourceRelationshipVersion,
		"source_owner_profile_id":     item.SourceOwnerProfileID,
		"evidence": []map[string]any{{
			"evidence_index": evidenceIndex,
			"quote":          v2MemoryPackEndpointText(item.Object),
		}},
	}
	if item.Object.Kind == "value" {
		hint["object_value"] = map[string]any{
			"ref":       item.Object.Ref,
			"type":      item.Object.ValueType,
			"value":     item.Object.Value,
			"source_id": item.Object.SourceID,
		}
	} else {
		hint["object_ref"] = item.Object.Ref
		hint["object_name"] = item.Object.DisplayName
	}
	return hint
}

func v2MemoryPackFragmentsByID(artifact V2MemoryPackArtifact) map[string]V2MemoryPackEvidenceFragment {
	out := map[string]V2MemoryPackEvidenceFragment{}
	for _, fragment := range artifact.EvidenceFragments {
		out[fragment.FragmentID] = fragment
	}
	return out
}

func v2MemoryPackSourceRef(loaded v2LoadedArtifact) string {
	if loaded.source != "" {
		return loaded.source
	}
	if loaded.artifact.Source.InstallationID != "" {
		return loaded.artifact.Source.InstallationID
	}
	return "memory_pack:" + loaded.hash
}

func v2MemoryPackAuthority(mode string) string {
	if mode == ModeTrusted {
		return "authoritative"
	}
	return "secondary"
}

func v2MemoryPackImportID(teamID, ownerID, hash, mode string) string {
	value := strings.Join([]string{teamID, ownerID, hash, mode}, "\x00")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("dense-mem:v2-memory-pack-import:"+value)).String()
}
