package memoryservice

import (
	"strings"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func recallConflictSummaries(records []repository.RelationshipConflictCaseRecord) []RecallConflictSummary {
	out := make([]RecallConflictSummary, 0, len(records))
	for _, record := range records {
		reviewDueAt := record.ReviewDueAt
		positions := recallConflictPositions(record.Positions)
		summary := RecallConflictSummary{
			ConflictID:          record.ConflictID,
			Version:             record.Version,
			Kind:                record.Kind,
			Status:              record.Status,
			Question:            record.Question,
			ReviewDueAt:         &reviewDueAt,
			EffectiveAt:         record.EffectiveAt,
			EffectiveTimeBasis:  record.EffectiveTimeBasis,
			PreferredPositionID: record.PreferredPositionID,
			Positions:           positions,
			PositionsTruncated:  len(record.Positions) > recallConflictPositionLimit,
		}
		out = append(out, summary)
	}
	return out
}

const (
	recallConflictPositionLimit         = 10
	recallConflictRelationshipIDLimit   = 20
	recallConflictOwnerProfileIDLimit   = 20
	recallConflictResultEvidenceIDLimit = 50
)

func recallConflictPositions(records []repository.RelationshipConflictPositionRecord) []RecallConflictPosition {
	if len(records) > recallConflictPositionLimit {
		records = records[:recallConflictPositionLimit]
	}
	out := make([]RecallConflictPosition, 0, len(records))
	for _, record := range records {
		out = append(out, RecallConflictPosition{
			PositionID:        record.PositionID,
			Disposition:       record.Disposition,
			RelationshipIDs:   limitStrings(record.RelationshipIDs, recallConflictRelationshipIDLimit),
			OwnerProfileIDs:   limitStrings(record.OwnerProfileIDs, recallConflictOwnerProfileIDLimit),
			ResultEvidenceIDs: limitStrings(record.EvidenceIDs, recallConflictResultEvidenceIDLimit),
		})
	}
	return out
}

func limitStrings(values []string, limit int) []string {
	if limit >= 0 && len(values) > limit {
		values = values[:limit]
	}
	return append([]string(nil), values...)
}

func relatedHypothesisSummaries(records []repository.HypothesisRecord) []RelatedHypothesisSummary {
	out := make([]RelatedHypothesisSummary, 0, len(records))
	for _, record := range records {
		out = append(out, RelatedHypothesisSummary{
			HypothesisID:          record.HypothesisID,
			SubjectEntityID:       record.SubjectEntityID,
			PredicateKey:          record.PredicateKey,
			ObjectEntityID:        record.ObjectEntityID,
			ObjectValueID:         record.ObjectValueID,
			Statement:             record.Statement,
			Status:                record.Status,
			SourceRelationshipIDs: relatedHypothesisSourceIDs(record.SourceRefs),
			GeneratorKind:         publicHypothesisGeneratorKind(record.GeneratorKind),
			GeneratorVersion:      record.GeneratorVersion,
			CreatedAt:             record.CreatedAt,
		})
	}
	return out
}

func relatedHypothesisSourceIDs(refs []map[string]any) []string {
	out := []string{}
	for _, ref := range refs {
		id, _ := ref["id"].(string)
		id = strings.TrimSpace(id)
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func publicHypothesisGeneratorKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "provider":
		return "provider"
	default:
		return "deterministic"
	}
}

func filterRelatedRelationshipsByGroups(values []RelatedRelationshipSummary, excludedGroups map[string]struct{}) []RelatedRelationshipSummary {
	if len(values) == 0 || len(excludedGroups) == 0 {
		return values
	}
	filtered := make([]RelatedRelationshipSummary, 0, len(values))
	for _, value := range values {
		if _, excluded := excludedGroups[value.SemanticGroupKey]; excluded {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}
