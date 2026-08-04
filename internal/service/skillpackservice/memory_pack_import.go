package skillpackservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func rememberRequestFromPack(importID string, loaded loadedArtifact, mode string, selected map[string]bool, decisions map[string]string) (memoryservice.RememberRequest, []ImportItemResult, error) {
	evidenceByID := MemoryPackEvidenceByID(loaded.artifact)
	evidence := []memoryservice.RememberEvidenceInput{}
	relationshipHints := []map[string]any{}
	results := make([]ImportItemResult, 0, len(loaded.artifact.Relationships))
	for _, item := range loaded.artifact.Relationships {
		result := ImportItemResult{
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
		content := MemoryPackEvidenceContent(loaded.artifact, item, evidenceByID)
		if contentRunes := utf8.RuneCountInString(content); contentRunes > 999 {
			return memoryservice.RememberRequest{}, results, fmt.Errorf(
				"memory pack item %q produces evidence with %d code points; max is 999",
				item.ItemID,
				contentRunes,
			)
		}
		evidence = append(evidence, memoryservice.RememberEvidenceInput{
			Content:        content,
			SourceType:     MemoryPackSourceType,
			Source:         MemoryPackSourceRef(loaded),
			Authority:      MemoryPackAuthority(mode),
			SourceKey:      "memory_pack:" + loaded.hash,
			SourceRevision: loaded.hash,
			IdempotencyKey: importID + ":" + item.ItemID,
			Labels:         []string{MemoryPackLabel},
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
		hint, err := MemoryPackRelationshipHint(loaded.artifact, item, evidenceIndex)
		if err != nil {
			return memoryservice.RememberRequest{}, results, err
		}
		relationshipHints = append(relationshipHints, hint)
		results = append(results, result)
	}
	return memoryservice.RememberRequest{
		ContractVersion:   domain.ContractVersion,
		Evidence:          evidence,
		RelationshipHints: relationshipHints,
		IdempotencyKey:    "memory-pack:" + importID,
	}, results, nil
}

func (s *memoryPackService) appendImportChanges(ctx context.Context, teamID, importID, ingestID string, items []ImportItemResult) error {
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

func MemoryPackImportSummary(loaded loadedArtifact, mode string, ingestID string, items []ImportItemResult) map[string]any {
	out := map[string]any{
		"contract_version": domain.ContractVersion,
		"artifact_format":  loaded.format,
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

func importResultFromExisting(record *domain.SkillPackImport, hash string, mode string) *ImportResult {
	result := &ImportResult{
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

func MemoryPackImportCounts(items []ImportItemResult) (int, int) {
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

func MemoryPackSelectedItemSet(selected []string, items []MemoryPackRelationship) map[string]bool {
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

func validateMemoryPackImportSelections(artifact MemoryPackArtifact, selected []string, decisions []ImportItemDecision) error {
	items := map[string]struct{}{}
	for _, item := range artifact.Relationships {
		items[item.ItemID] = struct{}{}
	}
	for _, itemID := range uniqueStrings(selected) {
		if _, ok := items[itemID]; !ok {
			return fmt.Errorf("memory pack import: selected item %q is not present in artifact", itemID)
		}
	}
	for _, decision := range decisions {
		itemID := strings.TrimSpace(decision.ItemID)
		if itemID == "" {
			return errors.New("memory pack import: conflict decision item_id is required")
		}
		if _, ok := items[itemID]; !ok {
			return fmt.Errorf("memory pack import: conflict decision item %q is not present in artifact", itemID)
		}
		switch strings.TrimSpace(decision.Action) {
		case DecisionSkip, DecisionImportAnyway, "import_for_review", "map_entity", "confirm_new_entity", "accept_source_authority":
		default:
			return fmt.Errorf("memory pack import: unsupported conflict decision action %q", decision.Action)
		}
	}
	return nil
}

func MemoryPackDecisionSet(decisions []ImportItemDecision) map[string]string {
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

func rollbackConflicts(record *domain.SkillPackImport, changes []domain.SkillPackImportChange) []string {
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

func rollbackImpactToken(record *domain.SkillPackImport, changes []domain.SkillPackImportChange) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "%s\x00%s\x00%s\x00%s\x00%d", record.TeamID, record.ImportID, record.Status, record.UpdatedAt.UTC().Format(time.RFC3339Nano), len(changes))
	for _, change := range changes {
		_, _ = fmt.Fprintf(&b, "\x00%s:%s:%s:%s", change.ChangeID, change.EntityType, change.EntityID, change.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func MemoryPackEvidenceContent(artifact MemoryPackArtifact, item MemoryPackRelationship, evidence map[string]MemoryPackEvidence) string {
	var b strings.Builder
	b.WriteString(memoryPackRelationshipStatement(artifact, item))
	if item.SourceRelationshipID != "" {
		_, _ = fmt.Fprintf(&b, "\nSource relationship %s version %d is provenance only.", item.SourceRelationshipID, item.SourceRelationshipVersion)
	}
	for _, evidenceID := range item.SupportEvidenceIDs {
		supportEvidence, ok := evidence[evidenceID]
		if !ok || strings.TrimSpace(supportEvidence.Content) == "" {
			continue
		}
		_, _ = fmt.Fprintf(&b, "\nSupport evidence %s: %s", evidenceID, supportEvidence.Content)
	}
	return b.String()
}

func memoryPackRelationshipStatement(artifact MemoryPackArtifact, item MemoryPackRelationship) string {
	return fmt.Sprintf(
		"Memory pack %q proposes a relationship: %s %s %s.",
		artifact.Name,
		item.Subject.DisplayName,
		item.PredicateKey,
		MemoryPackEndpointText(item.Object),
	)
}

func MemoryPackRelationshipHint(artifact MemoryPackArtifact, item MemoryPackRelationship, evidenceIndex int) (map[string]any, error) {
	prefix := fmt.Sprintf("Memory pack %q proposes a relationship: ", artifact.Name)
	subject := item.Subject.DisplayName
	predicate := item.PredicateKey
	objectText := MemoryPackEndpointText(item.Object)
	subjectStart := len([]rune(prefix))
	subjectEnd := subjectStart + len([]rune(subject))
	predicateStart := subjectEnd + 1
	predicateEnd := predicateStart + len([]rune(predicate))
	objectStart := predicateEnd + 1
	objectEnd := objectStart + len([]rune(objectText))
	polarity := strings.TrimSpace(item.Polarity)
	if polarity == "" {
		polarity = "+"
	}
	span := func(start, end int) map[string]any {
		return map[string]any{"evidence_index": evidenceIndex, "start": start, "end": end}
	}
	hint := map[string]any{
		"ref": item.ItemID,
		"subject": map[string]any{
			"name":        subject,
			"entity_kind": string(domain.EntityKindOther),
			"span":        span(subjectStart, subjectEnd),
		},
		"predicate": map[string]any{
			"proposed_key": predicate,
			"surface":      predicate,
			"span":         span(predicateStart, predicateEnd),
		},
		"polarity": polarity,
		"modality": "proposal",
		"supports": []map[string]any{span(subjectStart, objectEnd)},
	}
	if item.Object.Kind == "value" {
		canonicalValue, err := memoryPackCanonicalValue(item.Object)
		if err != nil {
			return nil, err
		}
		hint["object"] = map[string]any{"value": map[string]any{
			"type":    item.Object.ValueType,
			"value":   canonicalValue,
			"surface": objectText,
			"span":    span(objectStart, objectEnd),
		}}
	} else {
		hint["object"] = map[string]any{"entity": map[string]any{
			"name":        objectText,
			"entity_kind": string(domain.EntityKindOther),
			"span":        span(objectStart, objectEnd),
		}}
	}
	return hint, nil
}

func memoryPackCanonicalValue(endpoint MemoryPackEndpoint) (any, error) {
	switch endpoint.ValueType {
	case string(domain.ValueTypeString), string(domain.ValueTypeDate), string(domain.ValueTypeDateTime):
		return endpoint.Value, nil
	case string(domain.ValueTypeNumber):
		value, err := strconv.ParseFloat(endpoint.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("memory pack value %q must be a number", endpoint.Value)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("memory pack value %q must be finite", endpoint.Value)
		}
		return value, nil
	case string(domain.ValueTypeBoolean):
		value, err := strconv.ParseBool(endpoint.Value)
		if err != nil {
			return nil, fmt.Errorf("memory pack value %q must be a boolean", endpoint.Value)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("memory pack value type %q is unsupported", endpoint.ValueType)
	}
}

func MemoryPackEvidenceByID(artifact MemoryPackArtifact) map[string]MemoryPackEvidence {
	out := map[string]MemoryPackEvidence{}
	for _, item := range artifact.Evidence {
		out[item.EvidenceID] = item
	}
	return out
}

func MemoryPackSourceRef(loaded loadedArtifact) string {
	if loaded.source != "" {
		return loaded.source
	}
	if loaded.artifact.Source.InstallationID != "" {
		return loaded.artifact.Source.InstallationID
	}
	return "memory_pack:" + loaded.hash
}

func MemoryPackAuthority(mode string) string {
	if mode == ModeTrusted {
		return "authoritative"
	}
	return "secondary"
}

func MemoryPackImportID(teamID, ownerID, hash, mode string) string {
	value := strings.Join([]string{teamID, ownerID, hash, mode}, "\x00")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("dense-mem:v2-memory-pack-import:"+value)).String()
}
