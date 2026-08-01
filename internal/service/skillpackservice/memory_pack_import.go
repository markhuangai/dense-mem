package skillpackservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func rememberRequestFromPack(importID string, loaded loadedArtifact, mode string, selected map[string]bool, decisions map[string]string) (memoryservice.RememberRequest, []ImportItemResult, error) {
	evidenceByID := MemoryPackEvidenceByID(loaded.artifact)
	evidence := []memoryservice.RememberEvidenceInput{}
	entityHints := []map[string]any{}
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
		content, entities, relationship, err := memoryPackSubmissionMaterial(importID, loaded.artifact, item, evidenceByID, evidenceIndex)
		if err != nil {
			return memoryservice.RememberRequest{}, results, fmt.Errorf("memory pack import item %s: %w", item.ItemID, err)
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
		entityHints = append(entityHints, entities...)
		relationshipHints = append(relationshipHints, relationship)
		results = append(results, result)
	}
	return memoryservice.RememberRequest{
		ContractVersion:   domain.ContractVersion,
		Evidence:          evidence,
		EntityHints:       entityHints,
		RelationshipHints: relationshipHints,
		IdempotencyKey:    "memory-pack:" + importID,
	}, results, nil
}

func (s *memoryPackService) appendImportChanges(ctx context.Context, teamID, importID, submissionID string) error {
	if submissionID == "" {
		return nil
	}
	now := s.now().UTC()
	if err := s.deps.Ledger.AppendChange(ctx, domain.SkillPackImportChange{
		ChangeID:   uuid.NewString(),
		ImportID:   importID,
		TeamID:     teamID,
		EntityType: "submission",
		EntityID:   submissionID,
		Action:     domain.SkillPackChangeActionLinked,
		AfterState: map[string]any{
			"submission_id": submissionID,
		},
		CreatedAt: now,
	}); err != nil {
		return err
	}
	return nil
}

func MemoryPackImportSummary(loaded loadedArtifact, mode string, submissionID string, items []ImportItemResult) map[string]any {
	out := map[string]any{
		"contract_version": domain.ContractVersion,
		"artifact_format":  loaded.format,
		"artifact_hash":    loaded.hash,
		"mode":             mode,
		"source_url":       loaded.source,
		"legacy_artifact":  loaded.legacy,
		"items":            items,
	}
	if submissionID != "" {
		out["submission_id"] = submissionID
	}
	return out
}

func importResultFromExisting(record *domain.SkillPackImport, hash string, mode string) *ImportResult {
	result := &ImportResult{
		ImportID:     record.ImportID,
		ArtifactHash: hash,
		Mode:         mode,
		Status:       record.Status,
		SubmissionID: record.SubmissionID,
		AppliedCount: record.AppliedCount,
		SkippedCount: record.SkippedCount,
	}
	if submissionID, _ := record.Summary["submission_id"].(string); result.SubmissionID == "" {
		result.SubmissionID = submissionID
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
	if record.Status == domain.SkillPackImportStatusSubmitted {
		return []string{"import is still represented by a submission; check its submission status before lifecycle changes"}
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
	b.WriteString(memoryPackRelationshipLead(item))
	if item.SourceRelationshipID != "" {
		_, _ = fmt.Fprintf(&b, "\nMemory pack %q records source relationship %s version %d as provenance only.", artifact.Name, item.SourceRelationshipID, item.SourceRelationshipVersion)
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

func memoryPackSubmissionMaterial(
	importID string,
	artifact MemoryPackArtifact,
	item MemoryPackRelationship,
	evidence map[string]MemoryPackEvidence,
	evidenceIndex int,
) (string, []map[string]any, map[string]any, error) {
	subject := strings.TrimSpace(item.Subject.DisplayName)
	predicate := strings.TrimSpace(item.PredicateKey)
	object := strings.TrimSpace(MemoryPackEndpointText(item.Object))
	if subject == "" || predicate == "" || object == "" {
		return "", nil, nil, errors.New("subject, predicate, and object text are required")
	}
	lead := memoryPackRelationshipLead(item)
	content := MemoryPackEvidenceContent(artifact, item, evidence)
	subjectStart := 0
	subjectEnd := len([]rune(subject))
	predicateStart := subjectEnd + 1
	predicateEnd := predicateStart + len([]rune(predicate))
	objectStart := predicateEnd + 1
	objectEnd := objectStart + len([]rune(object))
	leadEnd := len([]rune(lead))

	subjectRef := "memory_pack:" + importID + ":" + item.ItemID + ":subject"
	entities := []map[string]any{memoryPackEntityProposal(subjectRef, subject, item.Subject.SourceID, evidenceIndex, subjectStart, subjectEnd)}
	relationship := map[string]any{
		"proposal_id": item.ItemID,
		"subject_ref": subjectRef,
		"predicate": map[string]any{
			"surface":        predicate,
			"evidence_index": evidenceIndex,
			"start":          predicateStart,
			"end":            predicateEnd,
		},
		"polarity": memoryPackPolarity(item.Polarity),
		"modality": "statement",
		"evidence": []any{map[string]any{
			"evidence_index": evidenceIndex,
			"start":          0,
			"end":            leadEnd,
		}},
	}
	if item.Object.Kind == "value" {
		value, err := memoryPackObjectValue(item.Object)
		if err != nil {
			return "", nil, nil, err
		}
		relationship["object_value"] = value
	} else {
		objectRef := "memory_pack:" + importID + ":" + item.ItemID + ":object"
		entities = append(entities, memoryPackEntityProposal(objectRef, object, item.Object.SourceID, evidenceIndex, objectStart, objectEnd))
		relationship["object_ref"] = objectRef
	}
	return content, entities, relationship, nil
}

func memoryPackRelationshipLead(item MemoryPackRelationship) string {
	return strings.TrimSpace(item.Subject.DisplayName) + " " + strings.TrimSpace(item.PredicateKey) + " " + strings.TrimSpace(MemoryPackEndpointText(item.Object)) + "."
}

func memoryPackEntityProposal(ref, name, knownEntityID string, evidenceIndex, start, end int) map[string]any {
	proposal := map[string]any{
		"ref":  ref,
		"name": name,
		"evidence": []any{map[string]any{
			"evidence_index": evidenceIndex,
			"start":          start,
			"end":            end,
		}},
	}
	if _, err := uuid.Parse(strings.TrimSpace(knownEntityID)); err == nil {
		proposal["known_entity_id"] = strings.TrimSpace(knownEntityID)
	}
	return proposal
}

func memoryPackObjectValue(endpoint MemoryPackEndpoint) (map[string]any, error) {
	valueType := strings.TrimSpace(endpoint.ValueType)
	value := strings.TrimSpace(endpoint.Value)
	if valueType == "" || value == "" {
		return nil, errors.New("value type and value are required")
	}
	var parsed any = value
	switch valueType {
	case string(domain.ValueTypeNumber):
		parsedNumber, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("number value is invalid: %w", err)
		}
		parsed = parsedNumber
	case string(domain.ValueTypeBoolean):
		parsedBoolean, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("boolean value is invalid: %w", err)
		}
		parsed = parsedBoolean
	case string(domain.ValueTypeString), string(domain.ValueTypeDate), string(domain.ValueTypeDateTime):
	default:
		return nil, fmt.Errorf("value type %q is unsupported", valueType)
	}
	return map[string]any{
		"type":    valueType,
		"value":   parsed,
		"display": value,
	}, nil
}

func memoryPackPolarity(value string) string {
	if strings.TrimSpace(value) == "-" {
		return "-"
	}
	return "+"
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
