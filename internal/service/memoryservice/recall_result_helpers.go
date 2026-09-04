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

func recallEvidenceConflictSummaries(records []repository.EvidenceConflictCaseRecord) []RecallConflictSummary {
	out := make([]RecallConflictSummary, 0, len(records))
	for _, record := range records {
		positionsTruncated := len(record.Positions) > recallConflictPositionLimit
		positionRecords := record.Positions
		if positionsTruncated {
			positionRecords = positionRecords[:recallConflictPositionLimit]
		}
		positions := make([]RecallConflictPosition, 0, len(positionRecords))
		for _, position := range positionRecords {
			positions = append(positions, RecallConflictPosition{
				PositionID: position.PositionID, Disposition: "candidate",
				EvidenceID: position.CanonicalEvidenceID, OccurrenceID: position.OccurrenceID,
				Quote: position.Quote, SpanStart: position.SpanStart, SpanEnd: position.SpanEnd,
				Authority: position.Authority, Submitted: position.Submitted,
			})
		}
		preferred := strings.TrimSpace(record.PreferredPositionID)
		for index := range positions {
			if preferred != "" && positions[index].PositionID == preferred {
				positions[index].Disposition = "preferred"
			}
		}
		out = append(out, RecallConflictSummary{
			ConflictID: record.ConflictID, Version: record.Version, Kind: "evidence_conflict",
			Status: record.Status, PreferredPositionID: preferred, Positions: positions,
			PositionsTruncated: positionsTruncated,
		})
	}
	return out
}

func limitRecallConflictSummaries(values []RecallConflictSummary, limit int) []RecallConflictSummary {
	if limit <= 0 {
		return []RecallConflictSummary{}
	}
	seen := make(map[string]struct{}, len(values))
	capacity := len(values)
	if capacity > limit {
		capacity = limit
	}
	out := make([]RecallConflictSummary, 0, capacity)
	// Relationship conflicts remain first for compatibility with the existing
	// conflict queue projection; evidence conflicts fill the shared remainder.
	appendKind := func(kind string) {
		for _, value := range values {
			if (kind == "evidence_conflict" && value.Kind != "evidence_conflict") ||
				(kind == "relationship" && value.Kind == "evidence_conflict") {
				continue
			}
			if _, exists := seen[value.ConflictID]; exists {
				continue
			}
			seen[value.ConflictID] = struct{}{}
			out = append(out, value)
			if len(out) == limit {
				return
			}
		}
	}
	appendKind("relationship")
	appendKind("evidence_conflict")
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

const (
	recallConflictPositionLimit         = 10
	recallConflictSupporterLimit        = 20
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
		supportersTruncated := record.SupportersTruncated || len(record.Supporters) > recallConflictSupporterLimit
		out = append(out, RecallConflictPosition{
			PositionID:          record.PositionID,
			Disposition:         record.Disposition,
			SupporterCount:      record.SupporterCount,
			SupportersTruncated: supportersTruncated,
			Supporters:          recallConflictSupporters(record.Supporters),
			RelationshipIDs:     limitStrings(record.RelationshipIDs, recallConflictRelationshipIDLimit),
			OwnerProfileIDs:     limitStrings(record.OwnerProfileIDs, recallConflictOwnerProfileIDLimit),
			ResultEvidenceIDs:   limitStrings(record.EvidenceIDs, recallConflictResultEvidenceIDLimit),
		})
	}
	return out
}

func recallConflictSupporters(records []repository.RelationshipConflictSupporterRecord) []RecallConflictSupporter {
	if len(records) > recallConflictSupporterLimit {
		records = records[:recallConflictSupporterLimit]
	}
	out := make([]RecallConflictSupporter, 0, len(records))
	for _, record := range records {
		out = append(out, RecallConflictSupporter{
			ProfileID:          record.ProfileID,
			ProfileName:        record.ProfileName,
			StrongestAuthority: record.StrongestAuthority,
			EvidenceID:         record.EvidenceID,
			AcceptedAt:         record.AcceptedAt,
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
